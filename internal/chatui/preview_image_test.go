package chatui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
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

// chipActions are the preview actions on the transcript's runs: the texts
// they cover, keyed by message ID.
func chipActions(m *Model) map[string]string { return chipActionsAt(m, 100) }

func chipActionsAt(m *Model, width int) map[string]string {
	out := map[string]string{}
	for _, row := range m.transcript(width) {
		if row.kind != rowBody {
			continue
		}
		for _, r := range row.runs {
			if r.Act.Kind == actPreview {
				out[r.Act.Value] += r.Text
			}
		}
	}
	return out
}

func TestClickingTheFileChipTogglesThePreview(t *testing.T) {
	m, h := previewModel(t)
	if got := chipActions(m); len(got) != 0 {
		t.Fatalf("chip clickable before the host supports previews: %v", got)
	}
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	got := chipActions(m)
	if got[previewMsgID] != "[file cat.png · 7 B]" {
		t.Fatalf("chip actions %q", got)
	}
	runCmd(m.activate(chipAction(previewMsgID, 0)))
	if len(h.requests) != 1 || h.requests[0].MessageID != previewMsgID {
		t.Fatalf("chip click requests %v", h.requests)
	}
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "tok", ID: 9, Cols: 2, Rows: 1})
	if chipActions(m)[previewMsgID] == "" {
		t.Fatal("the chip of a shown preview is not clickable")
	}
	runCmd(m.activate(chipAction(previewMsgID, 0)))
	if len(h.released) != 1 || h.released[0] != "tok" || strings.Contains(m.View().Content, "\U0010EEEE") {
		t.Fatalf("second chip click: released %v", h.released)
	}
	// A reduced colour profile withdraws the button with the action.
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if got := chipActions(m); len(got) != 0 {
		t.Fatalf("chip clickable without truecolor: %v", got)
	}
}

// attachmentMessage is a message whose one published attachment sits at
// the [Image #1] marker of text.
func attachmentMessage(id, text, name string) Message {
	return Message{ID: id, Channel: "#space", Account: "mother", Nick: "other", Text: text,
		Attachments: []AttachmentRef{{ArtifactRef: ArtifactRef{ID: "00000000-0000-4000-8000-000000000001", Name: name, Bytes: 2048},
			At: strings.Index(text, "[Image #1]")}}}
}

func TestOnlyTheGeneratedChipIsTheButton(t *testing.T) {
	m, _ := previewModel(t)
	const id = "77eb4b77-cfbd-4b90-aec6-538014c4108d"
	// A typed look-alike of the chip stays text; only the attachment's own
	// chip, at its marker, toggles the preview.
	text := "typed [file dog.png · 2048 B] then [Image #1] here"
	m.Update(IncomingMsg{Message: attachmentMessage(id, text, "dog.png")})
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	if got := chipActions(m)[id]; got != "[file dog.png · 2048 B]" {
		t.Fatalf("attachment chip actions %q", got)
	}
	if out := m.View().Content; strings.Count(out, "[file dog.png · 2048 B]") != 2 {
		t.Fatalf("look-alike or chip text changed:\n%s", out)
	}
}

func TestChipShowsItsFileNameLiterally(t *testing.T) {
	m, _ := previewModel(t)
	const id = "88eb4b77-cfbd-4b90-aec6-538014c4108d"
	name := "a`b`*c*.png"
	m.Update(IncomingMsg{Message: attachmentMessage(id, "see [Image #1] here", name)})
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	label := "[file " + name + " · 2048 B]"
	if got := chipActions(m)[id]; got != label {
		t.Fatalf("chip actions %q, want %q", got, label)
	}
	lines := m.body(&m.messages[m.indexOf(messageKey(m.messages[len(m.messages)-1]))], 100).lines
	if got := lines[0].CopyText(0, len(lines[0].Text)); got != "see "+label+" here" {
		t.Fatalf("copy %q", got)
	}
	// Wrapped over several rows, every piece of the chip is the button.
	if got := chipActionsAt(m, 16)[id]; strings.ReplaceAll(got, " ", "") != strings.ReplaceAll(label, " ", "") {
		t.Fatalf("wrapped chip actions %q", got)
	}
	// Source view shows the original text, with no chip.
	m.source = true
	if got := chipActions(m); len(got) != 0 {
		t.Fatalf("source view has chip actions %v", got)
	}
}

func TestChipLabelsPassThePlaceholderFilter(t *testing.T) {
	m, _ := previewModel(t)
	const id = "99eb4b77-cfbd-4b90-aec6-538014c4108d"
	m.Update(IncomingMsg{Message: attachmentMessage(id, "see [Image #1] here", "a\U0010EEEE.png")})
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	if strings.ContainsRune(m.View().Content, kitty.Placeholder) {
		t.Fatal("a file name's placeholder reached the display")
	}
	if got := chipActions(m)[id]; got != "[file a .png · 2048 B]" {
		t.Fatalf("chip actions %q", got)
	}
}

func TestATableChipShowsItsFileNameLiterally(t *testing.T) {
	name := "a`b`*c*|d.png"
	label := "[file " + name + " · 2048 B]"
	for _, cell := range []string{"[Image #1]", "`[Image #1]`", "x **[Image #1]** y"} {
		t.Run(cell, func(t *testing.T) {
			m, _ := previewModel(t)
			const id = "aaeb4b77-cfbd-4b90-aec6-538014c4108d"
			m.Update(IncomingMsg{Message: attachmentMessage(id, "| file | n |\n| --- | --- |\n| "+cell+" | 1 |\n| short | 2 |", name)})
			m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
			lines := m.body(&m.messages[len(m.messages)-1], 100).lines
			var chipCell, otherCell string
			for _, dl := range lines {
				if dl.Table == nil || len(dl.Cells) < 2 {
					continue
				}
				first := dl.Text[dl.Cells[0][0]:dl.Cells[0][1]]
				switch {
				case strings.Contains(first, label):
					chipCell = dl.Text[:dl.Cells[1][0]]
				case first == "short":
					otherCell = dl.Text[:dl.Cells[1][0]]
				}
			}
			// The column is laid out for the label: the second column starts
			// at the same display column on every row.
			if chipCell == "" || m.widthMethod.StringWidth(chipCell) != m.widthMethod.StringWidth(otherCell) {
				t.Fatalf("table chip not literal or misaligned: %q / %q", chipCell, otherCell)
			}
			if !strings.Contains(ansi.Strip(m.View().Content), label) {
				t.Fatalf("table chip not shown literally:\n%s", m.View().Content)
			}
			if got := chipActions(m)[id]; got != "" {
				t.Fatalf("a table chip carries an action: %q", got)
			}
		})
	}
}

// labelCell is the screen cell where label starts in key's rows.
func labelCell(t *testing.T, m *Model, key, label string) (int, int) {
	t.Helper()
	m.View()
	for y := m.layout.bodyTop; y < m.layout.bodyTop+m.layout.bodyHeight; y++ {
		row, ok := m.rowAt(y)
		if !ok || row.key != key {
			continue
		}
		var text strings.Builder
		for _, r := range row.runs {
			text.WriteString(r.Text)
		}
		if at := strings.Index(text.String(), label); at >= 0 {
			return m.widthMethod.StringWidth(text.String()[:at]), y
		}
	}
	t.Fatalf("no %q in %s", label, key)
	return 0, 0
}

// The chip of an image the user sent is a button once the room echoes the
// message, as the footer action is, clicked with the SGR mouse reports a
// terminal writes (hover, press, a held motion in the same cell, release),
// decoded by a running program.
func TestOwnImageChipClicksThroughTerminalInput(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12})
	own := attachmentMessage("", "[Image #1] wasn't it merged?", "paste-1.png")
	own.ClientID, own.Account, own.Nick, own.Delivery = "c1", "", m.opts.Nick, Pending
	m.append(own)
	m.View() // the chip is drawn before the echo: no server ID, no button yet
	echo := own
	echo.ID, echo.Account = "99eb4b77-cfbd-4b90-aec6-538014c4108d", "mhuman"
	m.Update(IncomingMsg{Message: echo})
	x, y := labelCell(t, m, "client:c1", "[file paste-1.png")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m.opts.PreviewImage = func(r PreviewRequest) tea.Cmd { h.requests = append(h.requests, r); return tea.Quit }
	at := fmt.Sprintf("%d;%d", x+2, y+1) // one-based, inside the chip
	input := "\x1b[<35;" + at + "M" + "\x1b[<0;" + at + "M" + "\x1b[<32;" + at + "M" + "\x1b[<0;" + at + "m"
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(io.Discard), tea.WithInput(strings.NewReader(input)),
		tea.WithWindowSize(100, 30), tea.WithColorProfile(colorprofile.TrueColor), tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}), tea.WithoutSignals())
	if _, err := p.Run(); err != nil {
		t.Fatalf("the click never asked for the preview: %v", err)
	}
	if len(h.requests) != 1 || h.requests[0].MessageID != echo.ID {
		t.Fatalf("requests %+v", h.requests)
	}
}

const twoImagesID = "aaeb4b77-cfbd-4b90-aec6-538014c4108d"

// twoImages is a message with two published images, each at its marker on
// its own line, with text between and after them.
func twoImages() Message {
	text := "first [Image #1]\nbetween them\nsecond [Image #2]\nlast line"
	return Message{ID: twoImagesID, Channel: "#space", Account: "mother", Nick: "other", Text: text,
		Attachments: []AttachmentRef{
			{ArtifactRef: ArtifactRef{ID: "00000000-0000-4000-8000-000000000001", Name: "one.png", Bytes: 2048}, At: strings.Index(text, "[Image #1]")},
			{ArtifactRef: ArtifactRef{ID: "00000000-0000-4000-8000-000000000002", Name: "two.png", Bytes: 4096}, At: strings.Index(text, "[Image #2]")},
		}}
}

// shownRows are the transcript rows of key as drawn, one string per row:
// "image" for a preview row, otherwise the row's text.
func shownRows(m *Model, key string) []string {
	m.View()
	var out []string
	for _, row := range m.lastTranscript {
		if row.key != key || row.kind != rowBody && row.line < linePreview {
			continue
		}
		if row.line >= linePreview && row.line < lineSpacer {
			out = append(out, "image")
			continue
		}
		var text strings.Builder
		for _, r := range row.runs {
			text.WriteString(r.Text)
		}
		out = append(out, strings.TrimSpace(text.String()))
	}
	return out
}

// Each chip of a message with several images is its own button: it shows
// that image right under the line holding the chip, at the width of the
// message text, and a click on it again hides it.
func TestEachChipShowsItsImageUnderItsLine(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.Update(IncomingMsg{Message: twoImages()})
	key := "server:" + twoImagesID
	buttons := map[string]string{}
	for _, row := range m.transcript(100) {
		for _, r := range row.runs {
			if row.key == key && r.Act.Kind == actPreview {
				buttons[r.Act.Extra] += r.Text
			}
		}
	}
	if buttons["0"] != "[file one.png · 2048 B]" || buttons["1"] != "[file two.png · 4096 B]" {
		t.Fatalf("chip buttons %q", buttons)
	}
	runCmd(m.activate(chipAction(twoImagesID, 1)))
	if len(h.requests) != 1 || h.requests[0].Artifact.Name != "two.png" || h.requests[0].Cols != m.textWidth() {
		t.Fatalf("requests %+v (text width %d)", h.requests, m.textWidth())
	}
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "t2", ID: 7, Cols: 3, Rows: 2})
	want := []string{"first [file one.png · 2048 B]", "between them", "second [file two.png · 4096 B]", "image", "image", "last line"}
	if got := shownRows(m, key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows %q, want %q", got, want)
	}
	// The other chip replaces it, under its own line.
	runCmd(m.activate(chipAction(twoImagesID, 0)))
	if len(h.released) != 1 || h.released[0] != "t2" || len(h.requests) != 2 || h.requests[1].Artifact.Name != "one.png" {
		t.Fatalf("released %v requests %+v", h.released, h.requests)
	}
	m.Update(PreviewReadyMsg{Request: h.requests[1].Request, Generation: 1, Token: "t1", ID: 8, Cols: 3, Rows: 1})
	want = []string{"first [file one.png · 2048 B]", "image", "between them", "second [file two.png · 4096 B]", "last line"}
	if got := shownRows(m, key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows %q, want %q", got, want)
	}
	// A press on the image snaps a selection to the end of its chip's row.
	checked := 0
	for i, row := range m.lastTranscript {
		if row.key == key && row.line >= linePreview && row.line < lineSpacer {
			p, ok := m.pointAt(bodyIndent+1, i-m.scroll+m.layout.bodyTop)
			if lines := m.body(&m.messages[m.indexOf(key)], m.lastTextWidth).lines; !ok || p.line != 0 || p.lo != len(lines[0].Text) {
				t.Fatalf("point on the image %+v", p)
			}
			checked++
		}
	}
	if checked != 1 {
		t.Fatalf("%d image rows checked", checked)
	}
	runCmd(m.activate(chipAction(twoImagesID, 0)))
	if len(h.released) != 2 || h.released[1] != "t1" || m.preview != nil {
		t.Fatalf("second click: released %v preview %+v", h.released, m.preview)
	}
}

// The footer action steps through a message's images and then hides them,
// naming which one it shows next.
func TestShowImageStepsThroughSeveralImages(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.Update(IncomingMsg{Message: twoImages()})
	m.selected = m.indexOf("server:" + twoImagesID)
	label := func() string {
		for _, a := range previewActions(m) {
			if strings.HasPrefix(a, "image:") {
				return strings.TrimPrefix(a, "image:")
			}
		}
		return ""
	}
	for step, want := range []string{"Show image 1 of 2", "Show image 2 of 2", "Hide image"} {
		if got := label(); got != want {
			t.Fatalf("step %d: %q, want %q", step, got, want)
		}
		runCmd(m.runAction("image"))
		if step < 2 {
			r := h.requests[len(h.requests)-1]
			m.Update(PreviewReadyMsg{Request: r.Request, Generation: 1, Token: fmt.Sprintf("t%d", step), ID: 9, Cols: 2, Rows: 1})
		}
	}
	if len(h.requests) != 2 || h.requests[0].Artifact.Name != "one.png" || h.requests[1].Artifact.Name != "two.png" ||
		strings.Join(h.released, ",") != "t0,t1" || m.preview != nil || label() != "Show image 1 of 2" {
		t.Fatalf("requests %+v released %v", h.requests, h.released)
	}
	// Once no other image can be shown, the action hides the shown one.
	runCmd(m.runAction("image"))
	m.Update(PreviewReadyMsg{Request: h.requests[2].Request, Generation: 1, Token: "t2", ID: 9, Cols: 2, Rows: 1})
	m.membership = "left"
	if got := label(); got != "Hide image" {
		t.Fatalf("without sending access: %q", got)
	}
}

// A preview asks for the message width and the transcript's steady height.
// A shown preview the width bound is asked for again at a new width, and
// the old image freed; one bound only by its own size keeps its cells until
// the area no longer holds it. A request still pending is not repeated; its
// answer is checked when it arrives.
func TestPreviewFollowsTheTextWidth(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.View()
	m.runAction("image")
	if h.requests[0].Cols != m.textWidth() || h.requests[0].Rows != m.steadyBodyHeight()-1 {
		t.Fatalf("asked for %d×%d cells, text width %d, steady height %d", h.requests[0].Cols, h.requests[0].Rows, m.textWidth(), m.steadyBodyHeight())
	}
	// Filling the width: the width bound it.
	w := h.requests[0].Cols
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "wide", ID: 3, Cols: w, Rows: 4})
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	if len(h.requests) != 2 || h.requests[1].Cols != m.textWidth() || strings.Join(h.released, ",") != "wide" {
		t.Fatalf("after a resize: requests %+v released %v", h.requests, h.released)
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if len(h.requests) != 2 {
		t.Fatal("a pending request was repeated")
	}
	m.Update(PreviewReadyMsg{Request: h.requests[1].Request, Generation: 1, Token: "mid", ID: 4, Cols: h.requests[1].Cols, Rows: 3})
	if len(h.requests) != 3 || h.requests[2].Cols != m.textWidth() || strings.Join(h.released, ",") != "wide,mid" {
		t.Fatalf("answer at an old width: requests %+v released %v", h.requests, h.released)
	}
	// The host's widest preview bounds the request.
	m.Update(PreviewReadyMsg{Request: h.requests[2].Request, Generation: 1, Token: "narrow", ID: 5, Cols: h.requests[2].Cols, Rows: 3})
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 20, MaxRows: 256})
	if len(h.requests) != 4 || h.requests[3].Cols != 20 {
		t.Fatalf("capped request %+v", h.requests)
	}
}

// A small image keeps its own cells whatever the pane does, until the pane
// becomes too narrow or too short to hold it.
func TestPreviewOfASmallImageIsNotAskedAgain(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.View()
	m.runAction("image")
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "small", ID: 3, Cols: 8, Rows: 2})
	for _, size := range []tea.WindowSizeMsg{{Width: 70, Height: 30}, {Width: 120, Height: 40}, {Width: 60, Height: 20}} {
		m.Update(size)
		m.View()
	}
	if len(h.requests) != 1 || len(h.released) != 0 {
		t.Fatalf("a small image was asked again: requests %+v released %v", h.requests, h.released)
	}
	// Narrower than the image: it is asked again at the new width.
	m.Update(tea.WindowSizeMsg{Width: bodyIndent + 1 + 6, Height: 20})
	m.View()
	if len(h.requests) != 2 || h.requests[1].Cols != 6 || strings.Join(h.released, ",") != "small" {
		t.Fatalf("too narrow: requests %+v released %v", h.requests, h.released)
	}
}

// A tall image fills the transcript's steady height less the chip row. A
// taller or shorter pane asks again on the resize itself, before any other
// event or render; a multi-line draft or a notice, which do not change that
// height, never do.
func TestPreviewFollowsTheTranscriptHeight(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.View()
	m.runAction("image")
	rows := h.requests[0].Rows
	if rows < 1 || rows != m.lastBodyHeight-1 {
		t.Fatalf("asked for %d rows; body %d", rows, m.lastBodyHeight)
	}
	// Taller than wide: the height bound it.
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "tall", ID: 3, Cols: 10, Rows: rows})
	if err := m.SetDraft("one\ntwo\nthree\nfour"); err != nil {
		t.Fatal(err)
	}
	m.say(levelWarn, "a notice")
	m.View()
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if len(h.requests) != 1 {
		t.Fatalf("a draft or a notice asked again: %+v", h.requests)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if len(h.requests) != 2 || h.requests[1].Rows != rows+10 || strings.Join(h.released, ",") != "tall" {
		t.Fatalf("a taller pane: requests %+v released %v", h.requests, h.released)
	}
	m.Update(PreviewReadyMsg{Request: h.requests[1].Request, Generation: 1, Token: "taller", ID: 4, Cols: 10, Rows: rows + 10})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 25})
	if len(h.requests) != 3 || h.requests[2].Rows != rows-5 || strings.Join(h.released, ",") != "tall,taller" {
		t.Fatalf("a shorter pane: requests %+v released %v", h.requests, h.released)
	}
}

// The steady height a preview asks for is the transcript height View lays
// out with a one-line draft, no reply and no notice, at every size and with
// or without the channel strip and the participants panel.
func TestSteadyBodyHeightMatchesTheLayout(t *testing.T) {
	m, _ := previewModel(t)
	for _, size := range [][2]int{{20, 7}, {60, 15}, {60, 16}, {100, 30}, {200, 60}} {
		for _, roster := range []bool{false, true} {
			for _, channels := range [][]string{{"#space"}, {"#space", "#ux"}} {
				for _, name := range channels {
					m.view(name)
				}
				m.order, m.rosterOpen = channels, roster
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.View()
				if got := m.steadyBodyHeight(); got != m.lastBodyHeight {
					t.Errorf("%dx%d roster %v channels %d: steady %d, laid out %d", size[0], size[1], roster, len(channels), got, m.lastBodyHeight)
				}
			}
		}
	}
}

// Two chips in one wrapped paragraph: each image follows the displayed row
// holding its own chip, not the end of the paragraph, at every width.
func TestPreviewFollowsItsChipsRowInAWrappedParagraph(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	text := "first [Image #1] " + strings.Repeat("middle text ", 12) + "second [Image #2] " + strings.Repeat("tail text ", 8) + "end"
	msg := twoImages()
	msg.Text = text
	msg.Attachments[0].At, msg.Attachments[1].At = strings.Index(text, "[Image #1]"), strings.Index(text, "[Image #2]")
	m.Update(IncomingMsg{Message: msg})
	key := "server:" + twoImagesID
	// At 24 columns each chip itself wraps: the image follows its last row.
	for _, width := range []int{24, 50, 70} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 80})
		for k, chip := range []string{"2048 B]", "4096 B]"} {
			m.preview = nil
			runCmd(m.activate(chipAction(twoImagesID, k)))
			r := h.requests[len(h.requests)-1]
			m.Update(PreviewReadyMsg{Request: r.Request, Generation: 1, Token: fmt.Sprintf("t%d%d", width, k), ID: 7, Cols: 2, Rows: 1})
			rows := shownRows(m, key)
			at := slices.Index(rows, "image")
			if at < 1 || !strings.HasSuffix(strings.Join(rows[:at], " "), chip) && !strings.Contains(rows[at-1], chip+" ") || at == len(rows)-1 {
				t.Fatalf("width %d, %s: rows %q", width, chip, rows)
			}
			// A press on it snaps to the end of the chip's row.
			for i, row := range m.lastTranscript {
				if row.key == key && row.line >= linePreview && row.line < lineSpacer {
					above := m.lastTranscript[i-1]
					if p, ok := m.pointAt(bodyIndent+1, i-m.scroll+m.layout.bodyTop); !ok || p.line != above.line || p.lo != above.end {
						t.Fatalf("width %d, %s: point %+v, chip row ends at %d", width, chip, p, above.end)
					}
				}
			}
		}
	}
}

// The pointer over a shown image marks nothing: its placeholder rows are
// never underlined, neither when the image itself is hovered nor when its
// chip is, and a click on the image still hides it.
func TestHoveringAPreviewDoesNotUnderlineIt(t *testing.T) {
	m, h := previewModel(t)
	m.Update(PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 256, MaxRows: 256})
	m.View()
	m.runAction("image")
	m.Update(PreviewReadyMsg{Request: h.requests[0].Request, Generation: 1, Token: "tok", ID: 3, Cols: 4, Rows: 2})
	m.View()
	var image, chip [2]int
	found := false
	for _, c := range targetCells(m, actPreview, previewMsgID) {
		if row := m.lastTranscript[c[1]-m.layout.bodyTop+m.scroll]; isPreviewRow(row) {
			if !found {
				image, found = c, true
			}
		} else {
			chip = c
		}
	}
	if !found || chip == ([2]int{}) {
		t.Fatalf("no image or chip cells: %v", targetCells(m, actPreview, previewMsgID))
	}
	// targetCells gives each row's first action cell; for the image that is
	// its indent's end, so step onto the image itself.
	image[0]++
	underlined := func() (imageRows, chipRuns int) {
		for _, row := range m.lastTranscript {
			for _, r := range m.hoverRow(row) {
				if r.Style.Underline && isPreviewRow(row) {
					imageRows++
				}
				if r.Style.Underline && !isPreviewRow(row) && r.Act.Kind == actPreview {
					chipRuns++
				}
			}
		}
		return
	}
	move(m, image[0], image[1])
	m.View()
	if got := m.activeHover(); got != (hoverKey{}) {
		t.Fatalf("hovering the image is a target: %+v", got)
	}
	if rows, _ := underlined(); rows != 0 {
		t.Fatalf("hovering the image underlines %d image runs", rows)
	}
	move(m, chip[0], chip[1])
	m.View()
	if rows, chips := underlined(); rows != 0 || chips == 0 {
		t.Fatalf("hovering the chip: %d image runs underlined, %d chip runs", rows, chips)
	}
	clickCell(m, image)
	if m.preview != nil || strings.Join(h.released, ",") != "tok" {
		t.Fatalf("a click on the image did not hide it: %+v released %v", m.preview, h.released)
	}
}
