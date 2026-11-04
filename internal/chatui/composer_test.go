package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/hostintegration"
)

// Images and long pastes sit inline in one editable draft, in the order
// they were inserted among the typed text, like a coding-agent prompt.
func TestInlinePlaceholdersKeepInsertionOrder(t *testing.T) {
	m, sends, h := imageModel(t)
	paste(m, "a ")
	press(m, 'v', tea.ModCtrl)
	paste(m, " b ") // typed while the host reads: it stays after the image
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 0 || !strings.Contains(m.notice, "Still reading [Image #1]") {
		t.Fatalf("nothing is sent while an image is read: %q", m.notice)
	}
	m.Update(prepared(h.reads[0], "one"))
	long := strings.Repeat("x", foldBytes)
	paste(m, long)
	paste(m, " c ")
	pasteImg(t, m, h, "two")
	paste(m, long)
	want := "a [Image #1] b " + long + " c [Image #2]" + long
	if m.Draft() != want {
		t.Fatalf("draft order: %q", m.Draft())
	}
	if imgs := m.editor.images(); len(imgs) != 2 || string(imgs[0].data) != "one" || string(imgs[1].data) != "two" {
		t.Fatal("each placeholder keeps its own payload")
	}
	_, line := lineWith(t, m, "❯ a [Image #1] b [Pasted #1")
	if !strings.Contains(line, "c [Image #2]") || strings.Contains(line, long[:20]) {
		t.Fatalf("placeholders show inline in the prompt row: %q", line)
	}
	if out := view(m); strings.Contains(out, "[image ") {
		t.Fatalf("no separate attachment strip:\n%s", out)
	}
	m.editor.cursor = len("a [Image #1]")
	press(m, 'p', tea.ModAlt)
	if !strings.Contains(m.notice, "No collapsed paste") || len(m.editor.images()) != 2 {
		t.Fatal("an image never expands into its label text")
	}

	// A failed read leaves typed commands alone.
	if err := m.SetDraft("/source"); err != nil {
		t.Fatal(err)
	}
	m.editor.cursor = len(m.editor.text)
	press(m, 'v', tea.ModCtrl)
	m.Update(ImagePreparedMsg{ID: h.reads[len(h.reads)-1].ID, Channel: "#space", Err: hostintegration.ErrNoImage})
	press(m, tea.KeyEnter, 0)
	if !m.source || len(*sends) != 0 || m.Draft() != "" {
		t.Fatalf("the command still runs: %q %+v", m.Draft(), *sends)
	}

	// A draft holding an image is a message, never a command.
	if err := m.SetDraft("/help"); err != nil {
		t.Fatal(err)
	}
	m.editor.cursor = len(m.editor.text)
	pasteImg(t, m, h, "three")
	press(m, tea.KeyEnter, 0)
	if m.overlay != nil || len(*sends) != 1 || (*sends)[0].Text != "/help" {
		t.Fatalf("the draft is sent as text then image: %+v", *sends)
	}
}

// Deleting an image is an ordinary edit: undo restores the same payload and
// redo removes it again; a cut never yanks the label back as text.
func TestImageUndoRedoAndCut(t *testing.T) {
	m, _, h := imageModel(t)
	paste(m, "x ")
	img := pasteImg(t, m, h, "png")
	paste(m, " y")
	deleteImg(t, m, img)
	if m.Draft() != "x  y" || len(m.editor.images()) != 0 {
		t.Fatalf("Backspace removes the image whole: %q", m.Draft())
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != "x [Image #1] y" || img0(m) != img || img.state != "" {
		t.Fatalf("undo restores the same image: %q", m.Draft())
	}
	press(m, 'z', tea.ModCtrl|tea.ModShift)
	if m.Draft() != "x  y" || len(m.editor.held()) != 1 {
		t.Fatal("redo removes it again; undo can still restore it")
	}
	press(m, 'z', tea.ModCtrl)
	m.editor.cursor = len(m.editor.text)
	press(m, 'u', tea.ModCtrl)
	if m.Draft() != "" || m.editor.killed != "x  y" {
		t.Fatalf("a cut keeps the text but not the placeholder: %q", m.editor.killed)
	}
	press(m, 'y', tea.ModCtrl)
	if m.Draft() != "x  y" || len(m.editor.images()) != 0 {
		t.Fatal("a yank inserts plain text, never an image")
	}
	press(m, 'z', tea.ModCtrl)
	press(m, 'z', tea.ModCtrl)
	if img0(m) != img {
		t.Fatal("undo of the cut restores the image")
	}
}

// Each channel keeps its own images; a part confirmed in a hidden channel
// continues that channel's draft without touching the visible one.
func TestImagesStayWithTheirChannelDraft(t *testing.T) {
	m, sends, h := imageModel(t)
	paste(m, "space ")
	pasteImg(t, m, h, "s1")
	alt(m, '2')
	if m.Draft() != "" {
		t.Fatal("the other channel's draft is its own")
	}
	pasteImg(t, m, h, "b1")
	if m.Draft() != "[Image #1]" {
		t.Fatalf("images are numbered per draft: %q", m.Draft())
	}
	alt(m, '1')
	press(m, tea.KeyEnter, 0)
	alt(m, '2')
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", MessageID: "s", State: Confirmed})
	if len(h.uploads) != 1 || h.uploads[0].Channel != "#space" || string(h.uploads[0].Data) != "s1" {
		t.Fatalf("the hidden channel's next part follows: %+v", h.uploads)
	}
	if m.name != "#build" || m.Draft() != "[Image #1]" || img0(m).state != "" {
		t.Fatal("the visible draft is untouched")
	}
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed})
	if m.views["#space"].editor.text != "" || len(*sends) != 1 {
		t.Fatalf("the hidden draft was sent in full: %q", m.views["#space"].editor.text)
	}
}

// One blank composer row sits above the prompt; it is padding, not draft.
func TestComposerHasPaddingAboveThePrompt(t *testing.T) {
	m, sends := readyModel()
	paste(m, "hi")
	lines := strings.Split(view(m), "\n")
	y, _ := lineWith(t, m, "❯ hi")
	if !strings.Contains(lines[y-2], "#space") || strings.TrimSpace(strings.TrimLeft(lines[y-1], "▌")) != "" {
		t.Fatalf("divider, blank padding, then the prompt:\n%s", view(m))
	}
	m.Update(tea.MouseClickMsg{X: 10, Y: y - 1, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 10, Y: y - 1, Button: tea.MouseLeft})
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != "hi" {
		t.Fatalf("padding adds nothing to the message: %+v", *sends)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	y, _ = lineWith(t, m, "❯ ")
	if !strings.Contains(strings.Split(view(m), "\n")[y-1], "#space") {
		t.Fatal("short windows keep every row for the transcript")
	}
}

// A remainder keeps its protection through clear, undo and redo: the text a
// send left behind never comes back as a command.
func TestRemainderStaysAMessageAcrossUndo(t *testing.T) {
	m, sends, h := imageModel(t)
	pasteImg(t, m, h, "png")
	paste(m, " /source")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Rejected})
	if m.Draft() != " /source" || !m.editor.remainder {
		t.Fatalf("the refused image leaves a protected remainder: %q", m.Draft())
	}
	press(m, 'u', tea.ModCtrl) // clear the draft
	press(m, 'z', tea.ModCtrl)
	press(m, 'z', tea.ModCtrl|tea.ModShift)
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != " /source" {
		t.Fatalf("undo restores the remainder: %q", m.Draft())
	}
	press(m, tea.KeyEnter, 0)
	if m.source || len(*sends) != 1 || (*sends)[0].Text != " /source" {
		t.Fatalf("the restored remainder is sent as a message: source=%v %+v", m.source, *sends)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", MessageID: "r", State: Confirmed})

	// History from before a partial send never returns: the spent image
	// takes it along, so a typed command cannot be undone back into place.
	paste(m, "/source")
	pasteImg(t, m, h, "png2")
	press(m, tea.KeyEnter, 0)
	m.Update(DeliveryMsg{ID: (*sends)[1].ID, Channel: "#space", MessageID: "s", State: Confirmed})
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[1].Token, State: Rejected})
	for range 4 {
		press(m, 'z', tea.ModCtrl)
	}
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "" || m.source || len(*sends) != 2 || (*sends)[1].Text != "/source" {
		t.Fatalf("nothing earlier returns as a command: %q source=%v %+v", m.Draft(), m.source, *sends)
	}

	// A draft the user empties and retypes is theirs again.
	paste(m, "/source")
	press(m, tea.KeyEnter, 0)
	if !m.source || len(*sends) != 2 {
		t.Fatal("a freshly typed command still runs")
	}
}

// A confirmed part ends the undo history, whatever happens to the image
// after it: published text never returns to run or be sent again (Fable's
// composer review reproductions).
func TestConfirmedPartEndsTheUndoHistory(t *testing.T) {
	m, sends, h := imageModel(t)
	paste(m, "/source ")
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", MessageID: "s", State: Confirmed})
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Uncertain})
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != "[Image #1]" {
		t.Fatalf("undo cannot reach the sent text: %q", m.Draft())
	}
	deleteImg(t, m, img0(m))
	press(m, tea.KeyEnter, 0)
	if m.source || len(*sends) != 1 || m.Draft() != "" {
		t.Fatalf("sent text neither runs nor is sent again: source=%v %+v %q", m.source, *sends, m.Draft())
	}

	m, sends, h = imageModel(t)
	paste(m, "hello ")
	pasteImg(t, m, h, "png")
	paste(m, " tail")
	press(m, tea.KeyEnter, 0)
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", MessageID: "h", State: Confirmed})
	press(m, 'z', tea.ModCtrl) // during the upload
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed})
	m.Update(DeliveryMsg{ID: (*sends)[1].ID, Channel: "#space", MessageID: "t", State: Confirmed})
	press(m, 'z', tea.ModCtrl)
	press(m, tea.KeyEnter, 0)
	var texts []string
	for _, s := range *sends {
		texts = append(texts, s.Text)
	}
	if strings.Join(texts, "|") != "hello|tail" || m.Draft() != "" {
		t.Fatalf("each part is published once: %q %q", texts, m.Draft())
	}
}
