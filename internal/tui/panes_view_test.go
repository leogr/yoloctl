package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// paneSession starts argv under a PTY for a pane test. Boxes without the
// opt-in fs.pty.allocate grant cannot open one; those tests skip there and
// run in the PTY gate.
func paneSession(t testing.TB, rows, cols int, argv ...string) *term.Session {
	t.Helper()
	s, err := term.Start(term.Options{Argv: argv, Rows: rows, Cols: cols})
	if err != nil {
		if strings.Contains(err.Error(), "/dev/ptmx") {
			t.Skipf("needs a PTY (%v): run with fs.pty.allocate", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// splitTab adds a tab of the given sessions: the first is the tab, each
// next one splits the focused pane (vertical first, then alternating).
func splitTab(t *testing.T, m *model, sessions ...*term.Session) *tabs.Tab {
	t.Helper()
	tab := testTab(sessions[0], "first", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	for i, s := range sessions[1:] {
		if err := tab.Layout.Split(i%2 == 0, &panes.Pane{Session: s, Command: "next"}); err != nil {
			t.Fatal(err)
		}
		m.layoutTab(tab)
	}
	m.focusPalette = false
	return tab
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Condition 1: an undivided tab never takes the per-cell composer.
func TestUndividedTabKeepsTheSingleFramePath(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.tabs.Add(testTab(paneSession(t, m.viewRows(), 80, "printf", "solo"), "solo", false))
	m.layoutTab(m.tabs.Current())
	m.focusPalette = false
	old := composePanes
	t.Cleanup(func() { composePanes = old })
	composePanes = func(*panes.Layout, map[*panes.Pane][]string) []string {
		t.Fatal("an undivided tab took the per-cell composer")
		return nil
	}
	waitFor(t, func() bool { return strings.Contains(m.View().Content, "solo") })
	tab := m.tabs.Current()
	tab.Layout.ToggleZoom() // a lone pane cannot zoom; still one frame
	_ = m.View()
}

// A divided tab composes every visible pane; each session has its pane's
// size; the host cursor is the focused pane's, offset by its rectangle,
// and no other pane's visible cursor is used.
func TestDividedTabComposesPanesWithOneCursor(t *testing.T) {
	m := newTestModel(t, 100, 26)
	left := paneSession(t, 24, 100, "sh", "-c", `printf 'left pane'; sleep 30`)
	right := paneSession(t, 24, 100, "sh", "-c", `printf 'right pane\r\nprompt> '; sleep 30`)
	tab := splitTab(t, m, left, right)
	lp, rp := tab.Layout.Panes()[0], tab.Layout.Panes()[1]
	if tab.Focused() != rp {
		t.Fatal("the new pane is focused")
	}
	for _, p := range []*panes.Pane{lp, rp} {
		r, g := p.Rect(), p.Session.LocalGeometry()
		if g.Rows != r.H || g.Cols != r.W {
			t.Fatalf("pane %q session %d×%d, rect %d×%d", p.Command, g.Rows, g.Cols, r.H, r.W)
		}
	}
	waitFor(t, func() bool {
		v := ansi.Strip(m.View().Content)
		return strings.Contains(v, "left pane") && strings.Contains(v, "prompt> ")
	})
	rows := strings.Split(m.View().Content, "\n")
	row := ansi.Strip(rows[m.chromeRows()])
	if []rune(row)[lp.Rect().W] != '│' || ansi.StringWidth(row) != m.width {
		t.Fatalf("the border sits between the panes: %q", row)
	}
	c := m.hostCursor()
	want := right.Frame().Cursor
	if c == nil || c.X != rp.Rect().X+want.X || c.Y != m.chromeRows()+rp.Rect().Y+want.Y {
		t.Fatalf("host cursor %+v, want the right pane's %+v offset by %+v", c, want, rp.Rect())
	}
	// Focus the left pane: its cursor (after "left pane") is the only one.
	tab.Layout.SetFocus(lp)
	c, want = m.hostCursor(), left.Frame().Cursor
	if c == nil || c.X != want.X || c.Y != m.chromeRows()+want.Y {
		t.Fatalf("host cursor %+v, want the left pane's %+v", c, want)
	}
	if m.View().WindowTitle != windowTitle(lp.Title()) {
		t.Fatalf("window title from the focused pane: %q", m.View().WindowTitle)
	}
}

func benchmarkView(b *testing.B, divided bool) {
	m := newTestModel(b, 160, 50)
	m.tabs.Add(testTab(paneSession(b, m.viewRows(), m.width, "seq", "1", "200"), "bench", false))
	if divided {
		p := &panes.Pane{Session: paneSession(b, m.viewRows(), m.width/2, "seq", "1", "200"), Command: "bench"}
		if err := m.tabs.Current().Layout.Split(true, p); err != nil {
			b.Fatal(err)
		}
	}
	m.layoutTab(m.tabs.Current())
	m.focusPalette = false
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkViewUndivided is today's single-frame path (condition 1);
// BenchmarkViewDivided is the per-cell composer for two panes.
func BenchmarkViewUndivided(b *testing.B) { benchmarkView(b, false) }
func BenchmarkViewDivided(b *testing.B)   { benchmarkView(b, true) }
