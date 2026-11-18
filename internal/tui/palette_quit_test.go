package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPaletteCtrlDQuitsWithDraft(t *testing.T) {
	for _, draft := range []string{"", "box inspect unfinished"} {
		t.Run(draft, func(t *testing.T) {
			m := newTestModel(t, 80, 24)
			m.pal.Insert(draft)
			_, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatal("Ctrl+D did not quit the idle palette")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("Ctrl+D did not return a quit command")
			}
			if got, _ := m.pal.Line(); got != draft || m.console.Busy() {
				t.Fatalf("Ctrl+D executed or changed the draft: %q", got)
			}
		})
	}
}
