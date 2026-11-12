package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// The chat pane's bridge turns a preview request into a validated frame with
// the pane's identities, and the host's frames into chat messages.
func TestChatPreviewBridge(t *testing.T) {
	_, img, room := chatImageFixture(t)
	header, err := chatstream.RoomHeader(room)
	if err != nil {
		t.Fatal(err)
	}
	hostFile, childFile, err := chatimage.ChannelPair()
	if err != nil {
		t.Fatal(err)
	}
	host := chatimage.NewConn(hostFile, true)
	t.Cleanup(func() { _ = host.Close() })
	c := &chatPreviews{conn: chatimage.NewConn(childFile, false)}
	t.Cleanup(c.close)
	s := &chatSession{header: header}
	var opts chatui.Options
	c.configure(s, &opts)
	if opts.PreviewImage == nil || opts.ReleasePreview == nil {
		t.Fatal("callbacks not installed")
	}
	req := chatui.PreviewRequest{Request: 4, Generation: 2, Channel: "#space", MessageID: "66eb4b77-cfbd-4b90-aec6-538014c4108d",
		Artifact: chatui.ArtifactRef{ID: "01234567-89ab-cdef-0123-456789abcdef", Name: "cat.png", SHA256: img.SHA256, Bytes: img.Bytes},
		Chip:     "[file cat.png · 1 KiB]"}
	if msg := opts.PreviewImage(req)(); msg != nil {
		t.Fatalf("request failed: %+v", msg)
	}
	f, err := host.Receive()
	if err != nil || f.Op != chatimage.OpPreview || f.RoomID != room.ID || f.HumanID != room.Human.ID || f.ChannelID != img.ChannelID ||
		f.MessageID != req.MessageID || f.Artifact != req.Artifact.ID || f.Chip != req.Chip || f.Generation != 2 || f.Request != 4 {
		t.Fatalf("frame %+v, %v", f, err)
	}
	_ = opts.ReleasePreview("tok")()
	if f, err := host.Receive(); err != nil || f.Op != chatimage.OpRelease || f.Token != "tok" {
		t.Fatalf("release %+v, %v", f, err)
	}
	// Host frames reach the chat model; the end of the channel withdraws support.
	got := make(chan tea.Msg, 4)
	c.relay(func(m tea.Msg) { got <- m })
	_ = host.Send(chatimage.Frame{Op: chatimage.OpCapability, Generation: 3, Supported: true, MaxCols: 60, MaxRows: 12})
	_ = host.Send(chatimage.Frame{Op: chatimage.OpReady, Generation: 3, Request: 4, Token: "tok", ID: 9, Cols: 2, Rows: 1})
	_ = host.Close()
	want := []tea.Msg{
		chatui.PreviewCapabilityMsg{Supported: true, Generation: 3, MaxCols: 60, MaxRows: 12},
		chatui.PreviewReadyMsg{Request: 4, Generation: 3, Token: "tok", ID: 9, Cols: 2, Rows: 1},
		chatui.PreviewCapabilityMsg{},
	}
	for i, w := range want {
		select {
		case m := <-got:
			if m != w {
				t.Fatalf("message %d: %+v, want %+v", i, m, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("message %d missing", i)
		}
	}
	// An unknown channel fails before anything is sent.
	if msg := opts.PreviewImage(chatui.PreviewRequest{Request: 5, Channel: "#other"})(); msg != (chatui.PreviewErrorMsg{Request: 5, Code: "failed"}) {
		t.Fatalf("unknown channel: %+v", msg)
	}
}
