package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReopenViewArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"chat", "--space", "dev", "--human=me"}, true},
		{[]string{"box", "logs", "api", "--follow", "--space=dev"}, true},
		{[]string{"space", "logs", "dev", "-f"}, true},
		{[]string{"sp", "logs", "dev", "--follow=false", "-f"}, true},
		{[]string{"space", "logs", "dev", "-f", "--follow=false"}, false},
		{[]string{"space", "logs", "dev", "--follow=bad", "-f"}, false},
		{[]string{"space", "logs", "dev", "--", "-f"}, false},
		{[]string{"space", "logs", "dev"}, false},
		{[]string{"chat", "--help"}, false},
		{[]string{"chat", "status"}, false},
		{[]string{"box", "logs", "api", "-f", "--help"}, false},
		{[]string{"space", "view", "dev"}, true},
		{[]string{"sp", "view", "dev"}, true},
		{[]string{"space", "shell", "dev"}, true},
		{[]string{"space", "ssh", "dev"}, true},
		{[]string{"box", "exec", "api", "--", "echo", "-f"}, false},
		{[]string{"box", "edit", "api"}, false},
		{[]string{"account", "secret", "set", "token"}, false},
		{[]string{"box", "start", "api"}, false},
		{[]string{"unknown", "--follow"}, false},
		{nil, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			launch := pinReopenArgs(tc.args, func() (string, error) { return "fixture", nil })
			action, ok := viewEndAction(launch)
			if ok != tc.ok {
				t.Fatalf("eligible=%v want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if action.label != "[ Reopen ]" || action.hint != "r to reopen" || !slices.Equal(action.args, launch) {
				t.Fatalf("action=%+v", action)
			}
			action.args[0] = "changed"
			if launch[0] == "changed" {
				t.Fatal("action aliases recorded argv")
			}
		})
	}
}

func TestEndedViewsReopenInPlace(t *testing.T) {
	oldExec := executable
	t.Cleanup(func() { executable = oldExec })
	executable = func() (string, error) { return truePath(t), nil }
	for _, args := range [][]string{{"chat", "--space", "dev"}, {"box", "logs", "api", "-f"}, {"space", "logs", "dev", "--follow"}, {"space", "view", "dev"}, {"space", "ssh", "--root"}, {"box", "exec", "api", "--", "bash", "-il"}} {
		for _, failed := range []bool{false, true} {
			t.Run(strings.Join(args, " ")+map[bool]string{false: "/success", true: "/failed"}[failed], func(t *testing.T) {
				m := newTestModel(t, 100, 26)
				ended := endedAttach(t, args...)
				if failed {
					ended.Session.Close()
					s := paneSession(t, 10, 40, "false")
					if err := s.Wait(5 * time.Second); err == nil {
						t.Fatal("need a failed child")
					}
					ended.Session = s
				}
				tab, live := splitWithEnded(t, m, ended)
				tab.Layout.ToggleZoom()
				m.layoutTab(tab)
				rect := ended.Rect()
				slots := m.reattachSlots()
				if len(slots) != 1 || slots[0].label != "[ Reopen ]" {
					t.Fatalf("slots=%+v", slots)
				}
				base := make([]string, m.viewRows())
				m.applyReattachButtons(base)
				if !strings.Contains(ansi.Strip(base[slots[0].y]), "[ Reopen ]") {
					t.Fatal("button absent")
				}
				if failed {
					// The same mouse routing consumes the gesture and replaces once.
					m.Update(tea.MouseClickMsg{X: slots[0].x, Y: slots[0].y + m.tabTop(), Button: tea.MouseLeft})
					m.Update(tea.MouseReleaseMsg{X: slots[0].x, Y: slots[0].y + m.tabTop(), Button: tea.MouseLeft})
				} else {
					m.Update(key('r', 0, "r"))
				}
				p := tab.Focused()
				if p == ended || !slices.Equal(p.Args, ended.Args) || p.Rect() != rect || tab.Layout.Zoomed() != p || len(tab.Layout.Panes()) != 2 || tab.Layout.Panes()[0] != live || m.focusPalette {
					t.Fatal("replacement lost argv/layout/focus")
				}
				m.Update(tabExitMsg{session: ended.Session})
				if p.Exited {
					t.Fatal("late exit changed replacement")
				}
				p.Session.Close()
			})
		}
	}
}

func TestReopenExcludesLiveAndRetainsFailedStart(t *testing.T) {
	oldExec := executable
	t.Cleanup(func() { executable = oldExec })
	m := newTestModel(t, 100, 26)
	ended := endedAttach(t, "chat", "--space=dev")
	tab, _ := splitWithEnded(t, m, ended)
	ended.Exited = false
	if m.reattachable(tab, ended) || len(m.reattachSlots()) != 0 {
		t.Fatal("live child offers reopen")
	}
	ended.Exited = true
	executable = func() (string, error) { return "", errors.New("cannot launch") }
	m.Update(key('r', 0, "r"))
	if tab.Focused() != ended || !ended.Exited || m.status == "" {
		t.Fatal("local failure lost old pane or error")
	}
	m.console.ShowError("palette fixture")
	m.focusPalette = true
	if len(m.reattachSlots()) != 0 {
		t.Fatal("palette-clipped row remains clickable")
	}
	m.focusPalette = false
	tab.Layout.ToggleZoom()
	m.width = len("[ Reopen ]") - 1
	m.layoutTab(tab)
	if len(m.reattachSlots()) != 0 {
		t.Fatal("narrow pane offers partial button")
	}
	m.standalone = true
	if m.reattachable(tab, ended) || m.reattachHint(ended.Args) != "" {
		t.Fatal("standalone offers reopen")
	}
}
