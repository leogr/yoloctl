package tui

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// Observe real child/private-stream completion before delivering the exit event;
// no fixture writes Pane.Exited. Diagnostics may cover the last row until the
// user dismisses them, as required by the existing palette behavior.
func TestReattachAfterRealAttachCompletion(t *testing.T) {
	for _, end := range []string{"exit-zero", "exit-nonzero", "holder-stopped", "admission-refused"} {
		for _, action := range []string{"key", "mouse"} {
			t.Run(end+"/"+action, func(t *testing.T) {
				m := newTestModel(t, 100, 30)
				args := []string{"box", "attach", "coder", "--space", "fixture",
					"--chat-room-id", "room", "--chat-member-id", "member",
					"--chat-box-id", "box-id", "--chat-allow-start", "--new-session"}
				opts := term.Options{TrustedView: true, Rows: m.viewRows(), Cols: m.width,
					CellWidth: 8, CellHeight: 16, EndHint: m.reattachHint(args)}
				var stop func()
				switch end {
				case "exit-zero":
					opts.Argv = []string{"sh", "-c", "exit 0"}
				case "exit-nonzero":
					opts.Argv = []string{"sh", "-c", "exit 17"}
				case "holder-stopped":
					sock, _, halt := startStoppableHolder(t)
					stop = halt
					opts.Argv = []string{os.Args[0], "-test.run=^TestManagedRelayChild$"}
					opts.Env = append(os.Environ(), "YOLOCTL_TEST_RELAY_SOCK="+sock)
				case "admission-refused":
					opts.Argv = []string{os.Args[0], "-test.run=^TestRejectedViewChild$"}
					opts.Env = append(os.Environ(), "YOLOCTL_TEST_REJECTED_VIEW=1")
				}
				s, err := term.Start(opts)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.Close)
				p := &panes.Pane{Session: s, Command: "yoloctl " + strings.Join(args, " "), Args: slices.Clone(args)}
				tab := tabs.New(p)
				m.tabs.Add(tab)
				m.layoutTab(tab)
				m.focusPalette = false
				if stop != nil {
					waitFor(t, s.HolderView)
					stop()
				}
				select {
				case <-s.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("attachment child/private stream did not finish")
				}
				_, _ = m.Update(tabExitMsg{session: s})
				if !p.Exited {
					t.Fatal("actual completion did not end the pane")
				}
				t.Logf("before diagnostic dismissal: palette=%v slots=%d status=%q", m.focusPalette, len(m.reattachSlots()), m.status)
				if end == "admission-refused" {
					if !m.focusPalette || !strings.Contains(m.console.GridText(), "Error:") {
						t.Fatal("admission error lost its diagnostic")
					}
					_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				}
				slots := m.reattachSlots()
				if len(slots) != 1 || slots[0].pane != p {
					t.Fatalf("ended attach action missing: palette=%v slots=%v", m.focusPalette, slots)
				}
				starts := 0
				oldExecutable := executable
				t.Cleanup(func() { executable = oldExecutable })
				executable = func() (string, error) { starts++; return truePath(t), nil }
				slot := p.Rect()
				if action == "key" {
					_, _ = m.Update(key('r', 0, "r"))
				} else {
					x, y := slots[0].x+2, m.tabTop()+slots[0].y
					_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
				}
				next := tab.Focused()
				t.Cleanup(next.Session.Close)
				if starts != 1 || next == p || next.Rect() != slot || m.focusPalette ||
					!slices.Equal(next.Args, args[:len(args)-1]) {
					t.Fatalf("action did not perform exactly one guarded same-slot attach: starts=%d args=%q", starts, next.Args)
				}
			})
		}
	}
}
