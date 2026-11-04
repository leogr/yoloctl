package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A held nickname asks once: Enter takes the other session over through the
// host, Esc closes this view, and a stray click outside the panel neither
// dismisses it nor chooses for the user.
func TestNickHeldAsksToTakeOverOrClose(t *testing.T) {
	takeOvers := 0
	m := New(Options{Space: "trial", Nick: "leogr", TakeOver: func() tea.Cmd { takeOvers++; return nil }})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(NickHeldMsg{})
	if m.overlay == nil || m.overlay.kind != overlayTakeOver {
		t.Fatal("a held nickname must ask before connecting")
	}
	if out := view(m); !strings.Contains(out, "@leogr is in use") || !strings.Contains(out, "Take over") || !strings.Contains(out, "Cancel") {
		t.Fatalf("prompt:\n%s", out)
	}
	m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if m.overlay == nil || takeOvers != 0 {
		t.Fatal("a click outside the panel must not answer the prompt")
	}
	press(m, tea.KeyEnter, 0)
	if takeOvers != 1 || m.overlay != nil || !m.reconnecting || !strings.Contains(m.notice, "Taking over @leogr") {
		t.Fatalf("Enter must take over through the host: %d %q", takeOvers, m.notice)
	}

	m.Update(NickHeldMsg{})
	cmd := press(m, tea.KeyEscape, 0)
	if cmd == nil {
		t.Fatal("Esc must close the view")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit || takeOvers != 1 || m.overlay != nil {
		t.Fatal("Cancel must close this view without taking over")
	}

	// A view without the host action only reports the refusal.
	plain := New(Options{Space: "trial", Nick: "leogr"})
	plain.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	plain.Update(NickHeldMsg{})
	if plain.overlay != nil || !strings.Contains(plain.notice, "@leogr is in use by another chat session") {
		t.Fatalf("without TakeOver the refusal is reported: %q", plain.notice)
	}
}
