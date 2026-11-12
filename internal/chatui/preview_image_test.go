package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

const previewMsgID = "66eb4b77-cfbd-4b90-aec6-538014c4108d"

type previewHost struct {
	requests []PreviewRequest
	released []string
}

// previewModel is a chat model with one image announcement selected and a
// host that records preview requests and releases.
func previewModel(t *testing.T) (*Model, *previewHost) {
	t.Helper()
	m, _, _ := imageModel(t)
	h := &previewHost{}
	m.opts.PreviewImage = func(r PreviewRequest) tea.Cmd { h.requests = append(h.requests, r); return nil }
	m.opts.ReleasePreview = func(token string) tea.Cmd { h.released = append(h.released, token); return nil }
	msg := announcement("mother", "#space", "cat.png", "PNGDATA")
	msg.ID = previewMsgID
	m.Update(IncomingMsg{Message: msg})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.selected = len(m.messages) - 1
	return m, h
}

func previewActions(m *Model) []string {
	var ids []string
	for _, a := range m.messageActions() {
		ids = append(ids, a.id+":"+a.label)
	}
	return ids
}

func TestShowImageOnlyWhenTheHostSupportsIt(t *testing.T) {
	m, h := previewModel(t)
	if strings.Contains(strings.Join(previewActions(m), ","), "image:") {
		t.Fatal("Show image offered before the host advertised support")
	}
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	if !strings.Contains(strings.Join(previewActions(m), ","), "image:Show image") {
		t.Fatalf("actions %v", previewActions(m))
	}
	m.runAction("image")
	if len(h.requests) != 1 {
		t.Fatal("no request")
	}
	r := h.requests[0]
	if r.MessageID != previewMsgID || r.Artifact.Name != "cat.png" || r.Generation != 1 || r.Chip != artifactLabel(&r.Artifact) || r.Channel != "#space" {
		t.Fatalf("request %+v", r)
	}
	// A message without a server UUID or without an artifact never asks.
	m.messages[m.selected].ID = "local-1"
	if m.canPreview(&m.messages[m.selected]) {
		t.Fatal("message without a server ID can preview")
	}
}

func TestReadyDrawsPlaceholderRowsAndHideReleases(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	m.runAction("image")
	r := h.requests[0]
	m.Update(PreviewReadyMsg{Request: r.Request, Generation: 1, Token: "tok", ID: 0x010203, Cols: 3, Rows: 2})
	out := m.View().Content
	if got := strings.Count(out, "\U0010EEEE"); got != 6 {
		t.Fatalf("%d placeholder cells drawn, want 6", got)
	}
	if !strings.Contains(out, "38;2;1;2;3") {
		t.Fatal("cells not drawn in the image ID colour")
	}
	if !strings.Contains(strings.Join(previewActions(m), ","), "image:Hide image") {
		t.Fatalf("actions %v", previewActions(m))
	}
	m.runAction("image")
	if len(h.released) != 1 || h.released[0] != "tok" || strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatalf("hide: released %v", h.released)
	}
}

func TestPreviewErrorsStaleRepliesAndNewCapability(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	m.runAction("image")
	m.Update(PreviewErrorMsg{Request: h.requests[0].Request, Code: "not_png"})
	if m.preview != nil || !strings.Contains(m.notice, "not a PNG") {
		t.Fatalf("error kept the preview: notice %q", m.notice)
	}
	// A reply for another request is freed at once and changes nothing.
	m.Update(PreviewReadyMsg{Request: 99, Generation: 1, Token: "stray", ID: 5, Cols: 1, Rows: 1})
	if len(h.released) != 1 || h.released[0] != "stray" || m.preview != nil {
		t.Fatalf("stray ready: released %v", h.released)
	}
	// A shown preview collapses when the host's capability changes.
	m.runAction("image")
	m.Update(PreviewReadyMsg{Request: h.requests[1].Request, Generation: 1, Token: "tok2", ID: 7, Cols: 1, Rows: 1})
	m.Update(PreviewCapabilityMsg{Supported: false, Generation: 2})
	if m.preview != nil || h.released[len(h.released)-1] != "tok2" {
		t.Fatalf("capability change kept the preview: released %v", h.released)
	}
}

// A message cannot draw image cells by typing placeholders: they display as
// spaces, while copy keeps the original text.
func TestLiteralPlaceholdersInMessagesDisplayAsSpaces(t *testing.T) {
	m, _ := previewModel(t)
	text := "look \U0010EEEE̅̅ here"
	m.Update(IncomingMsg{Message: Message{ID: "x1", Nick: "eve", Channel: "#space", Text: text}})
	if strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatal("literal placeholder reached the display")
	}
	if !strings.Contains(view(m), "look   here") {
		t.Fatalf("display %q", view(m))
	}
	if m.messages[len(m.messages)-1].Text != text {
		t.Fatal("message text changed")
	}
}

// shownPreview is previewModel with the selected message's image shown.
func shownPreview(t *testing.T) (*Model, *previewHost) {
	t.Helper()
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	m.runAction("image")
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "tok", ID: 9, Cols: 2, Rows: 1})
	if !strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatal("preview not drawn")
	}
	return m, h
}

// runCmd runs cmd and every command it batches.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(c)
		}
	}
}

func TestReducedColourProfileOffersOnlyTheChip(t *testing.T) {
	m, h := shownPreview(t)
	_, cmd := m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	runCmd(cmd)
	if len(h.released) != 1 || h.released[0] != "tok" || strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatalf("reduced profile kept the preview: released %v", h.released)
	}
	if strings.Contains(strings.Join(previewActions(m), ","), "image:") {
		t.Fatalf("Show image offered without truecolor: %v", previewActions(m))
	}
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if !strings.Contains(strings.Join(previewActions(m), ","), "image:Show image") {
		t.Fatalf("actions %v", previewActions(m))
	}
}

func TestLostConnectionOrMembershipCollapsesThePreview(t *testing.T) {
	m, h := shownPreview(t)
	m.Update(MembershipMsg{Channel: "#other", State: "left"})
	m.Update(MembershipMsg{Channel: "#SPACE", State: "joined"})
	if len(h.released) != 0 {
		t.Fatalf("unrelated membership released %v", h.released)
	}
	_, cmd := m.Update(MembershipMsg{Channel: "#Space", State: "kicked"})
	runCmd(cmd)
	if len(h.released) != 1 || strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatalf("kick kept the preview: released %v", h.released)
	}
	if strings.Contains(strings.Join(previewActions(m), ","), "image:") {
		t.Fatalf("Show image offered without membership: %v", previewActions(m))
	}

	m, h = shownPreview(t)
	_, cmd = m.Update(ConnectionMsg{})
	runCmd(cmd)
	if len(h.released) != 1 || strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatalf("disconnect kept the preview: released %v", h.released)
	}
	if strings.Contains(strings.Join(previewActions(m), ","), "image:") {
		t.Fatalf("Show image offered while disconnected: %v", previewActions(m))
	}
}
