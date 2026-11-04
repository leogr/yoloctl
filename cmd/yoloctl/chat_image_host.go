package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"image/png"
	"io"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/managedchat"
)

const chatImageDraftLimit = 32
const chatImageDraftLifetime = 10 * time.Minute

type preparedChatImage struct {
	request chatImageRequest
	link    *chatLink
	expires time.Time
}

// configureChatImages installs only host callbacks. The view invokes ReadImage
// on local Ctrl+V and UploadImage on intentional send; preparation never uploads.
// Source filenames are discarded. Records retain only bounded metadata, while
// the UI owns PNG payload/draft bounds. Each token is consumed at most once.
func configureChatImages(s *chatSession, opts *chatui.Options, enabled bool, read func(context.Context) (hostintegration.ClipboardImage, error)) {
	opts.ReadImage, opts.UploadImage = nil, nil
	if !enabled || read == nil || s.target.Operator {
		return
	}
	var mu sync.Mutex
	preparing := false
	drafts := map[string]preparedChatImage{}
	order := []string{}
	prune := func(now time.Time, link *chatLink) {
		order = slices.DeleteFunc(order, func(token string) bool {
			draft, ok := drafts[token]
			if !ok || draft.link != link || !now.Before(draft.expires) {
				delete(drafts, token)
				return true
			}
			return false
		})
	}
	opts.ReadImage = func(want chatui.ImageRequest) tea.Cmd {
		// Capture identity synchronously with the user's gesture. A delayed
		// desktop read must not acquire a newer room/channel or stream.
		s.mu.Lock()
		header, link := s.header, s.link
		s.mu.Unlock()
		return func() tea.Msg {
			result := chatui.ImagePreparedMsg{ID: want.ID, Channel: want.Channel}
			input, err := chatImageIdentity(header, want.Channel)
			if err != nil || !s.imageLinkLive(link) {
				result.Err = errors.New("image paste needs the current connected room and channel")
				return result
			}
			if s.target.DiskID != "" && s.target.DiskID != input.DiskID {
				result.Err = errors.New("image paste room disk changed; reopen the view")
				return result
			}
			mu.Lock()
			if preparing {
				mu.Unlock()
				result.Err = errors.New("another clipboard image is being read")
				return result
			}
			preparing = true
			mu.Unlock()
			defer func() { mu.Lock(); preparing = false; mu.Unlock() }()
			image, err := read(s.ctx)
			if err != nil {
				// Desktop/file errors can include a copied host pathname.
				// Keep only the no-image sentinel; do not surface that path.
				result.Err = errors.New("could not read a PNG image from the host clipboard")
				if errors.Is(err, hostintegration.ErrNoImage) {
					result.Err = hostintegration.ErrNoImage
				}
				return result
			}
			if err := validateChatImage(image.Data); err != nil {
				result.Err = err
				return result
			}
			if !s.imageLinkLive(link) || s.ctx.Err() != nil {
				result.Err = errors.New("the chat connection changed while reading the clipboard; image was not prepared")
				return result
			}
			input.PublicationToken = managedchat.NewID()
			input.Bytes, input.SHA256 = int64(len(image.Data)), chatImageHash(image.Data)
			now := time.Now()
			mu.Lock()
			prune(now, link)
			if len(order) >= chatImageDraftLimit {
				delete(drafts, order[0])
				order = order[1:]
			}
			drafts[input.PublicationToken] = preparedChatImage{request: input, link: link, expires: now.Add(chatImageDraftLifetime)}
			order = append(order, input.PublicationToken)
			mu.Unlock()
			dimensions, _ := png.DecodeConfig(bytes.NewReader(image.Data)) // validated above
			result.Token, result.Name = input.PublicationToken, input.filename()
			result.Data = image.Data
			result.Width, result.Height = dimensions.Width, dimensions.Height
			return result
		}
	}
	opts.UploadImage = func(want chatui.ImageUpload) tea.Cmd {
		return func() tea.Msg {
			result := chatui.ImageUploadedMsg{Channel: want.Channel, Token: want.Token, State: chatui.Rejected}
			mu.Lock()
			prune(time.Now(), s.current())
			draft, ok := drafts[want.Token]
			delete(drafts, want.Token)
			mu.Unlock()
			if !ok || draft.request.Channel != want.Channel || !s.imageLinkLive(draft.link) {
				result.Err = errors.New("image draft expired, was already attempted, or belongs to an earlier connection; inspect any earlier request and channel history before preparing a new image")
				return result
			}
			if err := validateChatImage(want.Data); err != nil {
				result.Err = err
				return result
			}
			if int64(len(want.Data)) != draft.request.Bytes || chatImageHash(want.Data) != draft.request.SHA256 {
				result.Err = errors.New("image draft bytes changed; paste again")
				return result
			}
			s.mu.Lock()
			header := s.header
			s.mu.Unlock()
			current, err := chatImageIdentity(header, want.Channel)
			if err != nil || current.RoomID != draft.request.RoomID || current.DiskID != draft.request.DiskID || current.HumanID != draft.request.HumanID || current.ChannelID != draft.request.ChannelID {
				result.Err = errors.New("image draft identity changed; paste again after reconnecting")
				return result
			}
			result.RequestID = rand.Text()
			ctx, cancel := context.WithTimeout(s.ctx, 50*time.Second)
			defer cancel()
			var raw bytes.Buffer
			request := draft.request
			request.Stage = want.Stage
			err = s.target.Call(ctx, controller.Request{ID: result.RequestID, Method: "chat.artifact.upload", Space: s.target.Space, Dash: -1, Payload: chatImagePayload(request)}, controller.ClientIO{
				In: chatImageFrame(want.Data), Out: &boundedChatWriter{Out: &raw, Remaining: chatImageReplyLimit},
				Err: &boundedChatWriter{Out: io.Discard, Remaining: chatImageReplyLimit},
			})
			var confirmed chatImageResult
			if err == nil {
				confirmed, err = decodeChatImageResult(raw.Bytes(), request, result.RequestID)
			}
			if err != nil {
				// A final process error alone does not prove that HTTP did
				// not commit. Never reuse this token or replay this request.
				result.State = chatui.Uncertain
				result.Err = errors.New("image publication is uncertain; inspect request " + result.RequestID + " and channel history for " + draft.request.filename() + " before any new attempt")
				if request.Stage {
					result.Err = errors.New("image staging is uncertain; inspect request " + result.RequestID + " before any new attempt (a staged copy is never announced and expires)")
				}
				return result
			}
			result.State = chatui.Confirmed
			result.ArtifactID, result.MsgID, result.Staged = confirmed.ID, confirmed.MsgID, confirmed.Staged
			result.SHA256, result.Bytes = confirmed.SHA256, confirmed.Bytes
			return result
		}
	}
}

func (s *chatSession) imageLinkLive(link *chatLink) bool {
	if link == nil || s.current() != link || link.stopping.Load() {
		return false
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	return !link.closed
}

func chatImageIdentity(header chatstream.Header, channelName string) (chatImageRequest, error) {
	input := chatImageRequest{RoomID: header.RoomID, DiskID: header.DiskID, Channel: channelName}
	for _, identity := range header.Identities {
		if identity.Account == header.SelfAccount && identity.Role == "human" && !identity.Revoked {
			input.HumanID = identity.ID
		}
	}
	channels := header.Channels
	if header.Schema == 1 {
		channels = []managedchat.Channel{{ID: managedchat.LegacyChannelID(header.RoomID), Name: header.Channel, Members: []string{input.HumanID}}}
	}
	for _, channel := range channels {
		if channel.Name == channelName && slices.Contains(channel.Members, input.HumanID) {
			input.ChannelID = channel.ID
		}
	}
	// Validate all identities before invoking the desktop; fill temporary
	// non-identity fields solely to reuse the strict request validator.
	check := input
	check.PublicationToken, check.Bytes, check.SHA256 = managedchat.NewID(), 1, chatImageHash(nil)
	return input, check.validate()
}
