package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

const chatImageLimit = 4 << 20
const chatImageReplyLimit = 8192

// chatImageRequest carries no filenames, URLs, credentials or image bytes.
// PublicationToken is caller-random correlation, not an idempotency key.
type chatImageRequest struct {
	RoomID           string `json:"room_id"`
	DiskID           string `json:"disk_id"`
	HumanID          string `json:"human_id"`
	ChannelID        string `json:"channel_id"`
	Channel          string `json:"channel"`
	PublicationToken string `json:"publication_token"`
	Bytes            int64  `json:"bytes"`
	SHA256           string `json:"sha256"`
	// Stage stores the image unannounced for one later message to publish
	// (AIRC message-attachments); nothing appears in the channel yet.
	Stage bool `json:"stage,omitempty"`
}

// chatImageResult is live-only. Controller receipts retain status, never this
// stdout payload. A lost reply must be recovered from authenticated history.
type chatImageResult struct {
	RequestID        string `json:"request_id"`
	PublicationToken string `json:"publication_token"`
	ID               string `json:"id"`
	SHA256           string `json:"sha256"`
	Bytes            int64  `json:"bytes"`
	MsgID            string `json:"msgid,omitempty"`
	Staged           bool   `json:"staged,omitempty"`
}

func (r chatImageRequest) validate() error {
	for _, id := range []string{r.RoomID, r.DiskID, r.HumanID, r.ChannelID, r.PublicationToken} {
		if layout.ValidateIdentity(id) != nil {
			return errors.New("invalid image publication identity")
		}
	}
	if err := managedchat.ValidateChannelName(r.Channel); err != nil {
		return err
	}
	if r.Bytes < 1 || r.Bytes > chatImageLimit || !chatstream.ValidArtifactHash(r.SHA256) {
		return errors.New("image publication needs a SHA256 and a nonempty PNG of at most 4 MiB")
	}
	return nil
}

func decodeChatImageRequest(req controller.Request) (chatImageRequest, error) {
	var input chatImageRequest
	if req.Method != "chat.artifact.upload" || len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View || req.Session != nil || req.Cwd != "" || req.Term != "" || req.Rows != 0 || req.Cols != 0 || len(req.Payload) > 2048 {
		return input, errors.New("image publication accepts only typed metadata and bounded stdin")
	}
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 2048); err != nil {
		return input, errors.New("invalid image publication metadata")
	}
	return input, input.validate()
}

func (r chatImageRequest) filename() string { return "paste-" + r.PublicationToken + ".png" }

func (r chatImageRequest) matchesRoom(room *managedchat.Room) error {
	if room == nil || room.ID != r.RoomID || room.DiskID != r.DiskID || room.Human.ID != r.HumanID || !room.Prepared || !room.RuntimeEnabled {
		return errors.New("image draft room, disk or human identity changed; paste again after reconnecting")
	}
	for _, channel := range room.ChannelCatalog() {
		if channel.ID == r.ChannelID && channel.Name == r.Channel && slices.Contains(channel.Members, r.HumanID) {
			return nil
		}
	}
	return errors.New("image draft channel identity or access changed; paste again after reconnecting")
}

func validateChatImage(p []byte) error {
	if len(p) == 0 || len(p) > chatImageLimit {
		return errors.New("chat image must be a nonempty PNG of at most 4 MiB")
	}
	return hostintegration.ValidateImage(p)
}

func chatImageHash(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// One length-prefixed frame and EOF prevent partial or extra data from reaching
// an upload process. The controller calls this before any publish-capable action.
func readChatImage(in io.Reader, req chatImageRequest) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(in, prefix[:]); err != nil {
		return nil, errors.New("incomplete image publication frame")
	}
	size := int64(binary.BigEndian.Uint32(prefix[:]))
	if size < 1 || size > chatImageLimit || size != req.Bytes {
		return nil, errors.New("image publication frame exceeds its bound or mismatches metadata")
	}
	p := make([]byte, int(size))
	if _, err := io.ReadFull(in, p); err != nil {
		return nil, errors.New("incomplete image publication bytes")
	}
	var extra [1]byte
	if n, err := in.Read(extra[:]); n != 0 || err != io.EOF {
		return nil, errors.New("image publication requires exactly one complete frame")
	}
	if err := validateChatImage(p); err != nil {
		return nil, err
	}
	if chatImageHash(p) != req.SHA256 {
		return nil, errors.New("image publication bytes do not match their SHA256")
	}
	return p, nil
}

func chatImageFrame(p []byte) io.Reader {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(p)))
	return io.MultiReader(bytes.NewReader(prefix[:]), bytes.NewReader(p))
}

// decodeChatImageUploadReply deliberately discards the fixed-origin URL after
// checking it. Nothing returned to the view can select an arbitrary fetch URL.
func decodeChatImageUploadReply(raw []byte, req chatImageRequest, requestID string) (chatImageResult, error) {
	var wire struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
		MsgID  string `json:"msgid"`
	}
	if err := decodeBoundedJSON(bytes.NewReader(raw), &wire, chatImageReplyLimit); err != nil {
		return chatImageResult{}, errors.New("invalid artifact upload reply")
	}
	if !chatstream.ValidArtifactID(wire.ID) || !chatstream.ValidArtifactID(wire.MsgID) || wire.URL != "http://airc.local/v1/artifacts/"+wire.ID || wire.SHA256 != req.SHA256 || wire.Bytes != req.Bytes {
		return chatImageResult{}, errors.New("artifact upload reply does not match this publication")
	}
	return chatImageResult{RequestID: requestID, PublicationToken: req.PublicationToken, ID: wire.ID, SHA256: wire.SHA256, Bytes: wire.Bytes, MsgID: wire.MsgID}, nil
}

// decodeChatImageStageReply accepts only a staged reply: no announcement
// message and no URL, with the requested size and digest.
func decodeChatImageStageReply(raw []byte, req chatImageRequest, requestID string) (chatImageResult, error) {
	var wire struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
		Staged bool   `json:"staged"`
	}
	if err := decodeBoundedJSON(bytes.NewReader(raw), &wire, chatImageReplyLimit); err != nil {
		return chatImageResult{}, errors.New("invalid artifact staging reply")
	}
	if !chatstream.ValidArtifactID(wire.ID) || !wire.Staged || wire.SHA256 != req.SHA256 || wire.Bytes != req.Bytes {
		return chatImageResult{}, errors.New("artifact staging reply does not match this image")
	}
	return chatImageResult{RequestID: requestID, PublicationToken: req.PublicationToken, ID: wire.ID, SHA256: wire.SHA256, Bytes: wire.Bytes, Staged: true}, nil
}

func decodeChatImageResult(raw []byte, req chatImageRequest, requestID string) (chatImageResult, error) {
	var result chatImageResult
	if err := decodeBoundedJSON(bytes.NewReader(raw), &result, chatImageReplyLimit); err != nil {
		return result, errors.New("invalid image publication result")
	}
	announced := chatstream.ValidArtifactID(result.MsgID) && !result.Staged
	staged := result.MsgID == "" && result.Staged
	if result.RequestID != requestID || result.PublicationToken != req.PublicationToken || result.SHA256 != req.SHA256 || result.Bytes != req.Bytes || !chatstream.ValidArtifactID(result.ID) || (req.Stage && !staged) || (!req.Stage && !announced) {
		return chatImageResult{}, fmt.Errorf("image publication result does not match request %s", requestID)
	}
	return result, nil
}

func chatImagePayload(req chatImageRequest) []byte {
	raw, _ := json.Marshal(req) // only strings and an integer
	return raw
}
