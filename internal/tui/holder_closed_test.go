package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// A box stop ends its holder without an exit status. The focused managed view
// then ends like any finished session: its pane keeps the screen with its end
// banner, bar and re-attach button, and the palette does not open over it
// with the holder-closed error.
func TestHolderClosedEndsTheViewWithoutOpeningThePalette(t *testing.T) {
	sock, _, stop := startStoppableHolder(t)
	m := newTestModel(t, 100, 30)
	s := managedView(t, sock, m.viewRows(), 100)
	p := &panes.Pane{Session: s, Command: "yoloctl box attach coder", Args: []string{"box", "attach", "coder", "--space", "fixture"}}
	tab := tabs.New(p)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	waitFor(t, s.HolderView)

	stop()
	<-s.Done()
	_, _ = m.Update(tabExitMsg{session: s})

	if !p.Exited {
		t.Fatal("the pane did not end")
	}
	if m.focusPalette || strings.Contains(m.console.GridText(), "holder closed") {
		t.Fatalf("the palette opened over the ended view: focus=%v\n%s", m.focusPalette, m.console.GridText())
	}
	if len(m.reattachSlots()) != 1 {
		t.Fatal("the ended view's re-attach button is not on screen")
	}
	if m.status != "box attach coder: session: connection to holder closed without an exit status" {
		t.Fatalf("the status line lost the exact diagnostic: %q", m.status)
	}
}

// Any other view failure (here the relay's first frame is a session error,
// which the frontend reports as a protocol failure) is still a command result
// in the palette, focused when it came from the focused pane.
func TestOtherViewFailureStillOpensThePalette(t *testing.T) {
	m := newTestModel(t, 100, 30)
	s, err := term.Start(term.Options{
		Argv:        []string{os.Args[0], "-test.run=^TestManagedRelayChild$"},
		Env:         append(os.Environ(), "YOLOCTL_TEST_RELAY_SOCK=unused", "YOLOCTL_TEST_RELAY_REFUSE=box coder is being recreated"),
		TrustedView: true, Rows: m.viewRows(), Cols: 100, CellWidth: 8, CellHeight: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	p := &panes.Pane{Session: s, Command: "yoloctl box attach coder", Args: []string{"box", "attach", "coder", "--space", "fixture"}}
	tab := tabs.New(p)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	<-s.Done() // the refusal kills the relay client
	_, _ = m.Update(tabExitMsg{session: s})
	text := m.console.GridText()
	if !m.focusPalette || !strings.Contains(text, "✗ box attach coder: ") || strings.Contains(text, "holder closed") || m.status != "" {
		t.Fatalf("a view refusal must stay a palette result: focus=%v status=%q\n%s", m.focusPalette, m.status, m.console.GridText())
	}
}
