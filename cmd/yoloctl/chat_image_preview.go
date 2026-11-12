package main

import (
	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// chatPreviews is a chat pane's end of the TUI's image control channel. The
// host fetches and draws; this end only asks, releases and relays replies.
type chatPreviews struct {
	conn *chatimage.Conn
}

// claimChatPreviews takes the inherited image channel, if the TUI gave one.
// It must run before anything else can start a helper that would inherit it.
// Without a valid channel the pane shows chips only.
func claimChatPreviews() *chatPreviews {
	f, err := chatimage.InheritedChannel()
	if err != nil || f == nil {
		return &chatPreviews{}
	}
	return &chatPreviews{conn: chatimage.NewConn(f, false)}
}

func (c *chatPreviews) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// configure installs the preview callbacks. Identities are captured when the
// person asks, like an image paste.
func (c *chatPreviews) configure(s *chatSession, opts *chatui.Options) {
	if c.conn == nil {
		return
	}
	conn := c.conn
	opts.PreviewImage = func(req chatui.PreviewRequest) tea.Cmd {
		s.mu.Lock()
		header := s.header
		s.mu.Unlock()
		return func() tea.Msg {
			frame, err := chatPreviewFrame(header, req)
			if err != nil {
				return chatui.PreviewErrorMsg{Request: req.Request, Code: "failed"}
			}
			if err := conn.Send(frame); err != nil {
				return chatui.PreviewErrorMsg{Request: req.Request, Code: "unavailable"}
			}
			return nil
		}
	}
	opts.ReleasePreview = func(token string) tea.Cmd {
		return func() tea.Msg {
			_ = conn.Send(chatimage.Frame{Op: chatimage.OpRelease, Token: token})
			return nil
		}
	}
}

// relay delivers host frames to the chat model until the channel ends; then
// previews are unsupported for this pane.
func (c *chatPreviews) relay(send func(tea.Msg)) {
	if c.conn == nil {
		return
	}
	go func() {
		defer send(chatui.PreviewCapabilityMsg{})
		for {
			f, err := c.conn.Receive()
			if err != nil {
				return
			}
			switch f.Op {
			case chatimage.OpCapability:
				send(chatui.PreviewCapabilityMsg{Supported: f.Supported, Generation: f.Generation, MaxCols: f.MaxCols, MaxRows: f.MaxRows})
			case chatimage.OpReady:
				send(chatui.PreviewReadyMsg{Request: f.Request, Generation: f.Generation, Token: f.Token, ID: f.ID, Cols: f.Cols, Rows: f.Rows})
			case chatimage.OpError:
				send(chatui.PreviewErrorMsg{Request: f.Request, Code: f.Code})
			}
		}
	}()
}

// chatPreviewFrame is the preview request for req with the room, disk, human
// and channel identities of header at the moment the person asked.
func chatPreviewFrame(header chatstream.Header, req chatui.PreviewRequest) (chatimage.Frame, error) {
	id, err := chatImageIdentity(header, req.Channel)
	if err != nil {
		return chatimage.Frame{}, err
	}
	return chatimage.Frame{Op: chatimage.OpPreview, Generation: req.Generation, Request: req.Request,
		RoomID: id.RoomID, DiskID: id.DiskID, HumanID: id.HumanID, ChannelID: id.ChannelID, Channel: req.Channel,
		MessageID: req.MessageID, Artifact: req.Artifact.ID, Bytes: req.Artifact.Bytes, SHA256: req.Artifact.SHA256,
		Chip: req.Chip}, nil
}
