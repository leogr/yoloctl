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

func TestStandaloneTmuxStyleDetachKeys(t *testing.T) {
	for name, key := range map[string]tea.Key{
		"ctrl+w":        {Code: 'w', Mod: tea.ModCtrl},
		"ctrl+d":        {Code: 'd', Mod: tea.ModCtrl},
		"d":             {Code: 'd', Text: "d"},
		"d num lock":    {Code: 'd', Text: "d", Mod: tea.ModNumLock},
		"legacy ctrl+w": {Code: 0x17},
		"legacy ctrl+d": {Code: 0x04},
	} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t, 40, 8)
			m.standalone = true
			m.focusPalette = false
			_, capture := mouseCapture(t, m, "")
			_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
			_, cmd := m.Update(tea.KeyPressMsg(key))
			if cmd == nil {
				t.Fatal("did not detach")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("detach was not a quit")
			}
			if got := capture(); got != "" {
				t.Fatalf("prefix or key reached the child: %q", got)
			}
		})
	}
	for name, key := range map[string]tea.Key{
		"D":     {Code: 'd', ShiftedCode: 'D', Text: "D", Mod: tea.ModShift},
		"alt+d": {Code: 'd', Mod: tea.ModAlt},
		"e":     {Code: 'e', Text: "e"},
	} {
		t.Run("forwards "+name, func(t *testing.T) {
			m := newTestModel(t, 40, 8)
			m.standalone = true
			m.focusPalette = false
			_, capture := mouseCapture(t, m, "")
			_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
			_, cmd := m.Update(tea.KeyPressMsg(key))
			if cmd != nil {
				if _, ok := cmd().(tea.QuitMsg); ok {
					t.Fatal("detached")
				}
			}
			if got := capture(); !strings.HasPrefix(got, "\x19") || len(got) < 2 {
				t.Fatalf("prefix and key not forwarded: %q", got)
			}
		})
	}
}

func TestStandaloneDoubledCustomPrefixStillForwards(t *testing.T) {
	for _, key := range []string{"ctrl+d", "d"} {
		t.Run(key, func(t *testing.T) {
			m := newTestModel(t, 40, 8)
			m.cfg.PaletteKey = key
			m.standalone = true
			m.focusPalette = false
			_, capture := mouseCapture(t, m, "")
			press := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
			if key == "d" {
				press = tea.KeyPressMsg{Code: 'd', Text: "d"}
			}
			_, _ = m.Update(press)
			_, cmd := m.Update(press)
			if cmd != nil {
				if _, ok := cmd().(tea.QuitMsg); ok {
					t.Fatal("doubled prefix detached")
				}
			}
			want := "\x04"
			if key == "d" {
				want = "d"
			}
			if got := capture(); got != want {
				t.Fatalf("doubled prefix sent %q, want %q once", got, want)
			}
		})
	}
}
