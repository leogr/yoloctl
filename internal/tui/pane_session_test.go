package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

// Condition 2, D1 against the real managed protocol: one holder shown in
// two panes of one tab. Each view sends its own pane size to the shared
// holder and the latest resize wins; the other view then renders a short
// or clipped frame inside its own rectangle, its border intact, until it
// resizes again and takes the size back.
func TestOneSessionInTwoPanesLatestResizeWins(t *testing.T) {
	m := newTestModel(t, 100, 26)
	sock, agent := startManagedHolder(t)
	v1 := managedView(t, sock, m.viewRows(), m.width)
	tab := testTab(v1, "yoloctl box attach coder", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	agent.emit(t, "agent ready\r\n")
	waitFor(t, func() bool { return strings.Contains(v1.GridText(), "agent ready") })
	shell := paneSession(t, 24, 50, "sleep", "30")
	if err := tab.Layout.Split(true, &panes.Pane{Session: shell, Command: "sh"}); err != nil {
		t.Fatal(err)
	}
	v2 := managedView(t, sock, 11, 49)
	if err := tab.Layout.Split(false, &panes.Pane{Session: v2, Command: "yoloctl box attach coder"}); err != nil {
		t.Fatal(err)
	}
	m.layoutTab(tab) // v1, the shell, then v2: v2 resizes the holder last
	p1, p3 := tab.Layout.Panes()[0], tab.Layout.Panes()[2]
	r1, r3 := p1.Rect(), p3.Rect()
	waitFor(t, func() bool { return agent.lastSize() == [2]int{r3.W, r3.H} })
	var out strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&out, "line %02d %s\r\n", i, strings.Repeat("=", 60))
	}
	agent.emit(t, out.String())
	waitFor(t, func() bool {
		return strings.Contains(v1.GridText(), "line 29") && strings.Contains(v2.GridText(), "line 29")
	})
	view := func() []string {
		rows := strings.Split(m.View().Content, "\n")[m.chromeRows():]
		for i := range rows {
			rows[i] = ansi.Strip(rows[i])
		}
		return rows
	}
	rows := view()
	if f := v1.Frame(); f.Rows > r1.H || f.Rows != r3.H {
		t.Fatalf("v1 should render the holder's %d rows (v2's pane), got %d", r3.H, f.Rows)
	}
	for y := 0; y < r1.H; y++ {
		if c := []rune(rows[y])[r1.W]; c != '│' && c != '├' {
			t.Fatalf("row %d lost the border while v1 is short: %q", y, rows[y])
		}
	}
	if strings.TrimSpace(string([]rune(rows[r1.H-1])[:r1.W])) != "" {
		t.Fatalf("v1's rows past the holder's height are blank: %q", rows[r1.H-1])
	}
	// v1 resizes again (as layoutTab would after any change to its pane):
	// the holder takes v1's size, and v2 is now clipped at its border.
	if err := v1.ResizeGeometry(m.surfaceGeometry(r1.H, r1.W)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return agent.lastSize() == [2]int{r1.W, r1.H} })
	waitFor(t, func() bool { return v2.Frame().Cols == r1.W })
	rows = view()
	for y, row := range rows[:m.viewRows()] {
		if w := ansi.StringWidth(row); w != m.width {
			t.Fatalf("row %d is %d wide after v2 grew past its pane: %q", y, w, row)
		}
	}
	if []rune(rows[r3.Y])[r3.X-1] != '│' && []rune(rows[r3.Y])[r3.X-1] != '├' {
		t.Fatalf("v2's left border is intact: %q", rows[r3.Y])
	}
}

// Ported from the prototype's real-PTY check: each program under a PTY in
// a pane sees its own pane's size (stty size after SIGWINCH).
func TestPanePTYsHaveTheirPaneSize(t *testing.T) {
	m := newTestModel(t, 100, 26)
	script := `trap 'echo size: $(stty size)' WINCH; echo size: $(stty size); while :; do sleep 0.05; done`
	left := paneSession(t, m.viewRows(), m.width, "sh", "-c", script)
	right := paneSession(t, m.viewRows(), m.width, "sh", "-c", script)
	tab := splitTab(t, m, left, right)
	for i, s := range []*panes.Pane{tab.Layout.Panes()[0], tab.Layout.Panes()[1]} {
		r := s.Rect()
		want := fmt.Sprintf("size: %d %d", r.H, r.W)
		waitFor(t, func() bool { return strings.Contains(s.Session.GridText(), want) })
		if i == 0 && r.W == m.width {
			t.Fatal("the left pane was not narrowed by the split")
		}
	}
}
