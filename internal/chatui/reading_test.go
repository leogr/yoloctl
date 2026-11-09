package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// jumpHit is the composer divider's jump to the latest messages, if drawn.
func jumpHit(m *Model) (hit, string, bool) {
	lines := strings.Split(view(m), "\n")
	for _, h := range m.hits {
		if h.area == hitTop && h.id == "live" && h.y > 0 {
			return h, lines[h.y], true
		}
	}
	return hit{}, "", false
}

// Scrolled back, the composer divider shows a jump to the latest messages,
// with or without new ones. Arrivals never move the reader; only a click on
// the jump or Alt+End returns to the live tail.
func TestJumpToLatestShowsWhileScrolledBackAndActsOnlyWhenChosen(t *testing.T) {
	m := scrolledModel(t)
	if _, _, ok := jumpHit(m); ok {
		t.Fatal("the live tail offers no jump")
	}
	press(m, tea.KeyPgUp, 0)
	h, row, ok := jumpHit(m)
	if !ok || m.follow || m.unread != 0 || !strings.Contains(row, "↓ Jump to latest") {
		t.Fatalf("scrolled back without new messages: follow=%v jump=%v %q", m.follow, ok, row)
	}
	anchor := m.lastTranscript[m.scroll]
	for _, id := range []string{"n1", "n2"} {
		m.Update(IncomingMsg{Message: Message{ID: id, Nick: "x", Text: "new"}})
	}
	h, row, ok = jumpHit(m)
	if now := m.lastTranscript[m.scroll]; now.key != anchor.key || now.line != anchor.line || m.follow {
		t.Fatalf("an arrival moved the reader: %+v → %+v", anchor, now)
	}
	if !ok || !strings.Contains(row, "↓ 2 new · Jump to latest") {
		t.Fatalf("new messages while scrolled back: %q", row)
	}
	if got := ansi.StringWidthWc(row); got != m.width {
		t.Fatalf("divider is %d cells, want %d", got, m.width)
	}
	m.Update(tea.MouseClickMsg{X: h.x0, Y: h.y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: h.x0, Y: h.y, Button: tea.MouseLeft})
	if _, _, ok := jumpHit(m); ok || !m.follow || m.unread != 0 || m.scroll != max(m.lastRows-m.lastBodyHeight, 0) {
		t.Fatalf("clicking the jump: follow=%v unread=%d scroll=%d", m.follow, m.unread, m.scroll)
	}
	press(m, tea.KeyPgUp, 0)
	if _, _, ok := jumpHit(m); !ok {
		t.Fatal("scrolled back again: no jump")
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	if _, _, ok := jumpHit(m); ok || !m.follow {
		t.Fatal("Alt+End did not return to the live tail")
	}
}

// On a narrow screen the jump shortens before the byte count gives way, and
// the divider still fills exactly its width.
func TestJumpToLatestShortensOnNarrowScreens(t *testing.T) {
	m := scrolledModel(t)
	m.Update(tea.WindowSizeMsg{Width: 26, Height: 20})
	press(m, tea.KeyPgUp, 0)
	_, row, ok := jumpHit(m)
	if !ok || !strings.Contains(row, "↓ Latest") || strings.Contains(row, "Jump") {
		t.Fatalf("narrow jump: %q", row)
	}
	m.Update(IncomingMsg{Message: Message{ID: "n1", Nick: "x", Text: "new"}})
	_, row, ok = jumpHit(m)
	if !ok || !strings.Contains(row, "↓ 1 new") || strings.Contains(row, "Jump") || ansi.StringWidthWc(row) != 26 {
		t.Fatalf("narrow jump with a new message: %q", row)
	}
}

// A fenced block is a card as wide as its widest line plus one cell of
// padding on each side: one light tint, the language as a quiet label row
// (with the copy button flush right),
// a padding row below, no corner glyphs or "code" title, and no tint past
// its width. Code keeps its whitespace; a drag copies exactly the code,
// never the label or padding, even when it starts on the padding.
func TestCodeBlocksAreLightCardsThatCopyExactly(t *testing.T) {
	m, copied := copyingModel(80, 24)
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "x", Text: "before\n```bash\n\tindented  two\nshort\n\n```\nafter"}})
	out := view(m)
	for _, frame := range []string{"┌", "└", "code bash", "```"} {
		if strings.Contains(out, frame) {
			t.Fatalf("the card shows %q:\n%s", frame, out)
		}
	}
	body := m.body(&m.messages[0], m.lastTextWidth)
	card := displayWidth(ansi.WcWidth, "\tindented  two") + 2
	bg := codeCard(m.theme)
	blockRows := 0
	for _, r := range body.rows {
		dl := body.lines[r.Line]
		if !dl.Code && dl.Fence == fenceNone {
			for _, run := range r.Runs {
				if run.Style.BG != 0 {
					t.Fatalf("prose row %q is tinted", dl.Text)
				}
			}
			continue
		}
		blockRows++
		if w := runsWidth(ansi.WcWidth, r.Runs); w != card {
			t.Fatalf("block row %q is %d cells, want the card's %d", dl.Text, w, card)
		}
		for _, run := range r.Runs {
			if run.Style.BG != bg {
				t.Fatalf("block row %q has an untinted run %q", dl.Text, run.Text)
			}
		}
		if dl.Fence == fenceOpen && (strings.Join(strings.Fields(runsText(r.Runs)), " ") != "bash "+copyLabel || r.Runs[1].Style.FG != m.theme.Dim) {
			t.Fatalf("label row: %q", runsText(r.Runs))
		}
	}
	if blockRows != 5 {
		t.Fatalf("label, three code rows and padding: got %d rows", blockRows)
	}
	for _, row := range m.lastTranscript {
		if row.key == "server:c" && row.fill != 0 {
			t.Fatal("a code row fills the transcript width")
		}
	}
	x0, y0 := cellOf(t, m, "server:c", 2, 0)
	x1, y1 := cellOf(t, m, "server:c", 3, len("short"))
	for _, start := range []int{x0, x0 - 1} {
		*copied = nil
		drag(m, start, y0, x1, y1)
		if want := "    indented  two\nshort"; len(*copied) != 1 || (*copied)[0] != want {
			t.Fatalf("drag from column %d copied %q, want %q", start, *copied, want)
		}
	}
}

// A code line wider than the view wraps inside a card that spans the view,
// and the narrowest views drop the padding rather than overflow.
func TestCodeCardsWrapWithinTheView(t *testing.T) {
	theme := New(Options{}).theme
	lines := displayLines(ansi.WcWidth, "```\n"+strings.Repeat("x", 50)+"\n```", false, theme, "")
	for _, width := range []int{1, 2, 3, 20} {
		rows := bodyRows(ansi.WcWidth, lines, width, theme)
		code := 0
		for _, r := range rows {
			if w := runsWidth(ansi.WcWidth, r.Runs); w != width {
				t.Fatalf("width %d: row is %d cells", width, w)
			}
			if !r.Decor {
				code += r.End - r.Start
			}
		}
		if code != 50 {
			t.Fatalf("width %d: rows cover %d of 50 code bytes", width, code)
		}
	}
}

func runsText(runs []run) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}
