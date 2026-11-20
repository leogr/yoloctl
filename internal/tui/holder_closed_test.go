package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestHolderClosedEndBanner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		standalone bool
	}{
		{name: "tab"},
		{name: "standalone", standalone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, _, stop := startStoppableHolder(t)
			m := newTestModel(t, 100, 30)
			m.standalone = tc.standalone
			t.Setenv("YOLOCTL_TEST_RELAY_SOCK", sock)
			t.Setenv("YOLOCTL_TEST_RELAY_BINARY", os.Args[0])
			relay := filepath.Join(t.TempDir(), "relay")
			if err := os.WriteFile(relay, []byte("#!/bin/sh\nexec \"$YOLOCTL_TEST_RELAY_BINARY\" -test.run=^TestManagedRelayChild$\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			oldExecutable := executable
			executable = func() (string, error) { return relay, nil }
			t.Cleanup(func() { executable = oldExecutable })
			p := m.startPane([]string{"box", "attach", "coder", "--space", "fixture"}, m.viewRows(), m.width)
			if p == nil {
				t.Fatal(m.noticeText())
			}
			s := p.Session
			t.Cleanup(s.Close)
			tab := tabs.New(p)
			m.tabs.Add(tab)
			m.layoutTab(tab)
			m.focusPalette = false
			waitFor(t, s.HolderView)

			stop()
			_, _ = m.Update(wakeMsg{}) // The UI can drain its error before the exit event.
			if err := s.Wait(5 * time.Second); !errors.Is(err, session.ErrSessionClosed) {
				t.Fatalf("managed view result = %v, want ErrSessionClosed", err)
			}
			_, _ = m.Update(tabExitMsg{session: s})

			if !p.Exited || m.focusPalette || strings.Contains(m.console.GridText(), "holder closed") {
				t.Fatalf("expected an ended pane without a palette result, exited=%v focus=%v\n%s", p.Exited, m.focusPalette, m.console.GridText())
			}
			grid := s.GridText()
			if !strings.Contains(grid, term.ExitCloseHint) {
				t.Fatalf("the end banner lost its close hint:\n%s", grid)
			}
			if tc.standalone {
				if !strings.Contains(grid, "✗ exit ") || !strings.HasSuffix(m.noticeText(), ": "+session.ErrSessionClosed.Error()) {
					t.Fatalf("standalone lost its exit line or diagnostic: %q\n%s", m.noticeText(), grid)
				}
				return
			}
			if !strings.Contains(grid, "✗ session ended without an exit status (the box stopped or restarted)") || strings.Contains(grid, "✗ exit ") {
				t.Fatalf("the end banner lost the inline failure:\n%s", grid)
			}
			if len(m.notices) != 0 || tab.NoticeAttention || tab.Badge {
				t.Fatalf("an exitless stream posted a notice or marked its tab: %q", m.noticeText())
			}
			if slots := m.endedButtonSlots(); len(slots) != 2 || slots[0].label != endedCloseButton || slots[1].label != reattachButton {
				t.Fatal("the ended view's Close and Re-attach buttons are not on screen")
			}
		})
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
	if !m.focusPalette || !strings.Contains(text, "✗ box attach coder: ") || strings.Contains(text, "holder closed") || m.noticeText() != "" {
		t.Fatalf("a view refusal must stay a palette result: focus=%v status=%q\n%s", m.focusPalette, m.noticeText(), m.console.GridText())
	}
}
