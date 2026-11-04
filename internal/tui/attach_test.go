package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestStandaloneGeometryMouseAndDetach(t *testing.T) {
	m := newTestModel(t, 40, 8)
	m.standalone = true
	m.focusPalette = false
	s, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	if m.viewRows() != 8 || m.chromeRows() != 0 {
		t.Fatal("standalone reserved chrome")
	}
	_, _ = m.Update(click(3, 0))
	_, _ = m.Update(releaseAt(3, 0))
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("detach did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("detach was not a quit")
	}
	if got := capture(); got != "\x1b[<0;4;1M\x1b[<0;4;1m" {
		t.Fatalf("mouse/prefix reached wrong row or child: %q", got)
	}
	_, _ = s.Write([]byte("\x1bcTOP"))
	m.status = "copy blocked"
	view := m.View()
	text := ansi.Strip(view.Content)
	if !strings.HasPrefix(text, "TOP") || !strings.Contains(text, "copy blocked") {
		t.Fatalf("standalone frame=%q", text)
	}
}

func TestStandalonePrefixAndOrdinaryKeys(t *testing.T) {
	m := newTestModel(t, 40, 8)
	m.standalone = true
	m.focusPalette = false
	_, capture := mouseCapture(t, m, "\x1b[>3u")
	for _, key := range []tea.Key{
		{Code: tea.KeyEnter, Mod: tea.ModShift},
		{Code: 'y', Mod: tea.ModCtrl}, {Code: 'y', Mod: tea.ModCtrl},
		{Code: 'y', Mod: tea.ModCtrl}, {Code: 'x', Text: "x"},
	} {
		_, _ = m.Update(tea.KeyPressMsg(key))
	}
	want := "\x1b[13;2u\x1b[121;5u\x1b[121;5:3u\x1b[121;5u\x1b[121;5:3ux"
	if got := capture(); got != want {
		t.Fatalf("keys=%q want %q", got, want)
	}
}
