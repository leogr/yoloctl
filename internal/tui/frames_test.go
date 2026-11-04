package tui

import (
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func frameTestTab(t *testing.T, m *model) *term.Session {
	t.Helper()
	s, err := term.Start(term.Options{Argv: []string{"sleep", "30"}, Rows: m.viewRows(), Cols: m.width})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "frame-test", false))
	m.focusPalette = false
	return s
}

func TestViewCapturesCursorWithGrid(t *testing.T) {
	m := newTestModel(t, 30, 5)
	s := frameTestTab(t, m)
	const a = "\x1b[2J\x1b[HA\x1b[2;1HA\x1b[1;2H\x1b[5 q"
	const b = "\x1b[2J\x1b[HBBB\x1b[2;1HBBB\x1b[2;4H\x1b[4 q"
	_, _ = s.Write([]byte(a))
	var writers sync.WaitGroup
	writers.Go(func() {
		for range 150 {
			_, _ = s.Write([]byte(b))
			_, _ = s.Write([]byte(a))
		}
	})
	defer writers.Wait()
	for range 150 {
		v := m.View()
		rows := strings.Split(ansi.Strip(v.Content), "\n")
		first, second := strings.TrimSpace(rows[barRows]), strings.TrimSpace(rows[barRows+1])
		if v.Cursor == nil {
			t.Fatal("lost visible cursor")
		}
		c := v.Cursor
		valid := first == "A" && second == "A" && c.X == 1 && c.Y == barRows && c.Shape == tea.CursorBar && c.Blink
		valid = valid || (first == "BBB" && second == "BBB" && c.X == 3 && c.Y == barRows+1 && c.Shape == tea.CursorUnderline && !c.Blink)
		if !valid {
			t.Fatalf("mixed TUI frame %q/%q, cursor %+v", first, second, c)
		}
	}
}

func TestViewHistoryStaysAnchoredDuringOutput(t *testing.T) {
	m := newTestModel(t, 30, 5)
	s := frameTestTab(t, m)
	_, _ = s.Write([]byte("A\r\nB\r\nC\r\nD\r\nE\r\nF"))
	_, _ = m.Update(wheel(tea.MouseWheelUp))
	for _, output := range []string{"", "\r\nG", "\r\nH"} {
		_, _ = s.Write([]byte(output))
		v := m.View()
		rows := strings.Split(ansi.Strip(v.Content), "\n")
		if strings.TrimSpace(rows[barRows]) != "C" || strings.TrimSpace(rows[barRows+1]) != "D" || v.Cursor != nil {
			t.Fatalf("scrolled viewport moved: %q cursor %+v", rows[barRows:], v.Cursor)
		}
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.View().Cursor == nil || m.scrollOff != 0 {
		t.Fatal("typing did not return to live view")
	}
}
