package followview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestBodyStartsAtRowZeroAndKeepsScrollCopyAndFooter(t *testing.T) {
	m, path := fileModel(t, "one\ntwo\nthree\nfour\nfive", Text)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 40, Height: 4})
	applyWork(t, m, cmd)
	applyWork(t, m, key(m, tea.KeyHome, 0))
	view := m.View()
	rows := strings.Split(ansi.Strip(view.Content), "\n")
	if m.bodyHeight() != 3 || m.maxTop() != 2 || len(rows) != 4 {
		t.Fatalf("body=%d maxTop=%d rows=%q", m.bodyHeight(), m.maxTop(), rows)
	}
	for y, want := range []string{"one", "two", "three"} {
		if strings.TrimSpace(rows[y]) != want {
			t.Fatalf("body row%d=%q want%q", y, rows[y], want)
		}
	}
	if !strings.HasPrefix(rows[3], "bytes ") || view.WindowTitle != path {
		t.Fatalf("footer/title changed: rows=%q title=%q", rows, view.WindowTitle)
	}
	if next := key(m, tea.KeyPgDown, 0); next != nil {
		applyWork(t, m, next)
	}
	if m.top != 2 || m.following {
		t.Fatalf("page scroll top=%d follow=%v", m.top, m.following)
	}
	var copied []string
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	for _, at := range []struct {
		y, x int
		want string
	}{{0, 5, "three"}, {2, 4, "five"}} {
		m.Update(tea.MouseClickMsg(tea.Mouse{X: 0, Y: at.y, Button: tea.MouseLeft}))
		m.Update(tea.MouseMotionMsg(tea.Mouse{X: at.x, Y: at.y, Button: tea.MouseLeft}))
		m.Update(tea.MouseReleaseMsg(tea.Mouse{X: at.x, Y: at.y, Button: tea.MouseLeft}))
		if len(copied) == 0 || copied[len(copied)-1] != at.want {
			t.Fatalf("visible row%d copied%q", at.y, copied)
		}
	}
	key(m, tea.KeyEsc, 0)
	m.Update(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 3, Button: tea.MouseLeft}))
	if m.selection != nil || m.drag {
		t.Fatal("footer started a document selection")
	}
	_, cmd = m.Update(tea.WindowSizeMsg{Width: 40, Height: 1})
	applyWork(t, m, cmd)
	one := m.View()
	if m.bodyHeight() != 0 || strings.Contains(one.Content, "\n") || !strings.HasPrefix(ansi.Strip(one.Content), "bytes ") || one.WindowTitle != path {
		t.Fatalf("one-row viewport: body=%d view=%q title=%q", m.bodyHeight(), one.Content, one.WindowTitle)
	}
}
