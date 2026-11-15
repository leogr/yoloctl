package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// stepPane starts a child that prints three lines (cursor on the third),
// then reports the first step click it receives on its private channel.
func stepPane(t *testing.T) *term.Session {
	t.Helper()
	s, err := term.Start(term.Options{StepClicks: true, Rows: 10, Cols: 40, Argv: []string{"sh", "-c",
		`printf 'a\nb\nc'; while ! read l <&"$YOLOCTL_TUI_STEP_FD" 2>/dev/null; do sleep 0.05; done; printf '\ngot %s\n' "$l"; sleep 5`}})
	if err != nil {
		if strings.Contains(err.Error(), "/dev/ptmx") {
			t.Skipf("needs a PTY (%v): run with fs.pty.allocate", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func paneText(s *term.Session) string { return strings.Join(s.Frame().Lines, "\n") }

// A plain click on a row at or above the cursor of a pane's live screen
// reaches its command as the row's offset from the cursor, never as input.
func TestPlainClickReachesTheStepsBlock(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s := stepPane(t)
	tab := testTab(s, "yoloctl box create api", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	waitFor(t, func() bool { return s.Frame().Cursor.Y == 2 && strings.Contains(paneText(s), "c") })
	r := tab.Focused().Rect()
	y := m.tabTop() + r.Y // the pane's first row: two above the cursor
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	waitFor(t, func() bool { return strings.Contains(paneText(s), "got -2") })
	if strings.Contains(paneText(s), "\x1b[<") {
		t.Fatal("no mouse report reached the input")
	}
}

// A drag, a modified click or a row below the cursor sends nothing; the
// same plain click then does.
func TestOnlyPlainClicksReachTheStepsBlock(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s := stepPane(t)
	tab := testTab(s, "yoloctl box create api", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	waitFor(t, func() bool { return s.Frame().Cursor.Y == 2 })
	r := tab.Focused().Rect()
	top := m.tabTop() + r.Y
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 3, Y: top}) // dragged
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top, Mod: tea.ModAlt})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top, Mod: tea.ModAlt})
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top + 5}) // below the cursor
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top + 5})
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(paneText(s), "got") {
		t.Fatalf("only a plain click on a block row is sent:\n%s", paneText(s))
	}
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: top})
	waitFor(t, func() bool { return strings.Contains(paneText(s), "got -2") })
}
