package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// copyButtons returns the screen cell of every copy button shown for the
// message, in display order.
func copyButtons(m *Model, key string) [][2]int {
	view(m)
	var cells [][2]int
	for i, row := range m.lastTranscript {
		if row.key != key {
			continue
		}
		x := 0
		for _, r := range row.runs {
			if r.act.kind == actCopyCode {
				cells = append(cells, [2]int{x, i - m.scroll + m.layout.bodyTop})
			}
			x += ansi.StringWidthWc(r.text)
		}
	}
	return cells
}

func click(m *Model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}

// Every fenced block, plain text included, gets its own copy button. It
// copies that block's source lines exactly (tabs, blank and trailing lines
// kept), never another block, the fences or the prose around them.
func TestCopyButtonCopiesExactlyItsOwnBlock(t *testing.T) {
	m, copied := copyingModel(80, 30)
	text := "intro\n```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\nbetween\n```\nplain text\n\n  indented\n```\n~~~\nunclosed `tick`\nlast"
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "x", Text: text}})
	buttons := copyButtons(m, "server:c")
	if len(buttons) != 3 {
		t.Fatalf("want one button per block, got %d", len(buttons))
	}
	want := []string{"func main() {\n\tprintln(\"hi\")\n}\n", "plain text\n\n  indented\n", "unclosed `tick`\nlast"}
	for i, cell := range buttons {
		*copied = nil
		click(m, cell[0], cell[1])
		if len(*copied) != 1 || (*copied)[0] != want[i] {
			t.Fatalf("button %d copied %q, want %q", i, *copied, want[i])
		}
		if m.notice != "Copied the code block" || m.noticeLevel != levelOK {
			t.Fatalf("button %d notice: %q", i, m.notice)
		}
	}
	// The button is on the label row, right-aligned within the card.
	body := m.body(&m.messages[0], m.lastTextWidth)
	for _, r := range body.rows {
		if dl := body.lines[r.line]; dl.fence == fenceOpen {
			last := r.runs[len(r.runs)-2]
			if last.text != copyLabel || last.act.kind != actCopyCode || last.copy {
				t.Fatalf("label row %q does not end with the button", runsText(r.runs))
			}
		}
	}
}

// The button follows seams.clipboard like every other explicit copy: off
// writes nothing and says why, and the hover hint says so too.
func TestCopyButtonHonorsClipboardOff(t *testing.T) {
	m, copied := copyingModel(80, 24)
	m.opts.Clipboard = "off"
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "x", Text: "```sh\nrm -rf build\n```"}})
	cell := copyButtons(m, "server:c")[0]
	m.Update(tea.MouseMotionMsg{X: cell[0], Y: cell[1]})
	if hint := runsText(m.hoverHint()); !strings.Contains(hint, "Copy code") || !strings.Contains(hint, "seams.clipboard") {
		t.Fatalf("hover hint: %q", hint)
	}
	if cmd := click(m, cell[0], cell[1]); cmd != nil || len(*copied) != 0 {
		t.Fatal("clipboard off still copied", *copied)
	}
	if !strings.Contains(m.notice, "Host clipboard is off") || m.noticeLevel != levelWarn {
		t.Fatalf("notice: %q", m.notice)
	}
}

// An empty block has nothing to copy, so it shows no button; a drag over a
// label row never copies the button's text.
func TestCopyButtonAbsentOnEmptyBlocksAndNeverDragged(t *testing.T) {
	m, copied := copyingModel(80, 24)
	m.Update(IncomingMsg{Message: Message{ID: "e", Nick: "x", Text: "```\n```"}})
	if n := len(copyButtons(m, "server:e")); n != 0 {
		t.Fatalf("empty block shows %d buttons", n)
	}
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "x", Text: "```py\nx = 1\n```"}})
	cell := copyButtons(m, "server:c")[0]
	x1, y1 := cellOf(t, m, "server:c", 1, len("x = 1"))
	drag(m, cell[0]+len(copyLabel)-1, cell[1], x1, y1)
	if len(*copied) != 1 || strings.Contains((*copied)[0], copyLabel) || (*copied)[0] != "x = 1" {
		t.Fatalf("drag from the button copied %q", *copied)
	}
}

// A card too narrow for both gives the button priority over the language
// label, which truncates first; narrower than the button, the row keeps the
// label alone. Each label row still fills its card exactly.
func TestCopyButtonOnNarrowCards(t *testing.T) {
	wm := ansi.WcWidth
	theme := spaceview.NewTheme(true, nil, nil)
	lines := displayLines(wm, "```typescript-long-label\nab\n```", false, theme, "")
	for _, width := range []int{40, 12, 7, 5, 3} {
		rows := bodyRows(wm, lines, width, theme)
		cards := codeCards(wm, lines, width)
		label := rows[0]
		if w := runsWidth(wm, label.runs); w != cards[0] {
			t.Fatalf("width %d: label row is %d cells, card %d", width, w, cards[0])
		}
		text := runsText(label.runs)
		hasButton := strings.HasSuffix(strings.TrimRight(text, " "), copyLabel)
		inner := cards[0] - 2*cardPad(width)
		if hasButton != (inner >= len(copyLabel)) {
			t.Fatalf("width %d (inner %d): button shown %v in %q", width, inner, hasButton, text)
		}
		if width == 40 && !strings.Contains(text, "typescript-long-label  "+copyLabel) {
			t.Fatalf("wide card: %q", text)
		}
		if width == 12 && !strings.Contains(text, "…") {
			t.Fatalf("narrow card did not truncate the label: %q", text)
		}
	}
}
