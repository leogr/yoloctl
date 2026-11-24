package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// followModel is a host TUI on missing-or-present "default" whose palette
// runs a script: any argv containing "fail" exits 1, others exit 0. records
// says which spaces have a saved record, as `space create` leaves them.
func followModel(t *testing.T, records map[string]bool) *model {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yoloctl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$*\" in *fail*) echo failed; exit 1;; esac\necho created\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldExecutable, oldExists := executable, spaceRecordExists
	executable = func() (string, error) { return script, nil }
	spaceRecordExists = func(name string) (bool, error) { return records[name], nil }
	t.Cleanup(func() { executable, spaceRecordExists = oldExecutable, oldExists })
	m := newTestModel(t, 100, 30)
	m.ctxSpace = "default"
	m.createdSpace = func(args []string) string {
		if len(args) > 2 && args[0] == "space" && args[1] == "create" {
			return args[2]
		}
		return ""
	}
	m.welcome = welcomeView{ctx: t.Context(), read: func(context.Context, string) WelcomeStatus { return WelcomeStatus{Step: WelcomeReady} }}
	_ = m.syncWelcome() // the idle screen of the start space
	return m
}

// runToEnd runs one palette command to its end and returns the check its end
// starts, nil when there is none.
func runToEnd(t *testing.T, m *model, args ...string) tea.Cmd {
	t.Helper()
	m.dispatch(args)
	waitFor(t, func() bool { return !m.console.Busy() })
	return m.syncCreated()
}

func answer(t *testing.T, check tea.Cmd) createdSpaceMsg {
	t.Helper()
	if check == nil {
		t.Fatal("a successful create was not checked")
	}
	msg, ok := check().(createdSpaceMsg)
	if !ok {
		t.Fatal("the check found nothing to follow")
	}
	return msg
}

func TestFollowCreateFromMissingSpace(t *testing.T) {
	m := followModel(t, map[string]bool{"work": true})
	msg := answer(t, runToEnd(t, m, "space", "create", "work"))
	m.pal.Insert("box list") // typed while the check runs
	m.openSpacePicker()      // a menu still loading its list
	generation := m.spaceGeneration
	_, _ = m.Update(msg)
	if m.ctxSpace != "work" || m.confirm != nil || m.spacePicker != nil || m.spaceGeneration != generation+1 {
		t.Fatalf("space %q confirm %v picker %v: want an immediate switch", m.ctxSpace, m.confirm, m.spacePicker)
	}
	if line, _ := m.pal.Line(); line != "box list" || !m.focusPalette {
		t.Fatalf("draft %q focus %v: the follow must keep the palette", line, m.focusPalette)
	}
	if !strings.Contains(m.noticeText(), "This TUI now uses space work") || !strings.Contains(m.console.GridText(), "created") {
		t.Fatalf("notice %q or the create's output is missing", m.noticeText())
	}
	if m.welcome.tip.command != "space use work" || !strings.HasPrefix(m.welcome.tip.text, "Tip: to save work as your default, type:\n  space use work\n") {
		t.Fatalf("save-default tip = %+v", m.welcome.tip)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "work ▾") {
		t.Fatal("the chip does not show the new space")
	}
	if !slices.Contains(m.childEnv(), "YOLOCTL_SPACE=work") {
		t.Fatal("palette commands still run in the old space")
	}
	// Used once: the next command's idle screen reads work again.
	if m.syncCreated() != nil {
		t.Fatal("the create was followed twice")
	}
	if check := runToEnd(t, m, "box", "list"); check == nil || check() != nil {
		t.Fatal("a later command revived the create")
	}
	tab := tabs.New(&panes.Pane{}) // leave the idle screen and come back
	m.tabs.Add(tab)
	_ = m.syncWelcome()
	m.tabs.CloseTab(tab)
	if read := m.syncWelcome(); read == nil || m.welcome.tip.text != "" || m.ctxSpace != "work" {
		t.Fatal("the save-default tip must last one idle visit")
	}
}

func TestFollowCreateAsksFromExistingSpace(t *testing.T) {
	for _, accept := range []bool{false, true} {
		m := followModel(t, map[string]bool{"default": true, "lab": true})
		_, _ = m.Update(answer(t, runToEnd(t, m, "space", "create", "lab")))
		if c := m.confirm; c == nil || c.space != "lab" || !c.created || c.close || m.ctxSpace != "default" {
			t.Fatalf("confirm %+v space %q: want the switch dialog with Keep selected", c, m.ctxSpace)
		}
		// Nothing to close: it asks only to switch.
		if text := ansi.Strip(m.View().Content); !strings.Contains(text, "Switch this TUI from default to lab?") || !strings.Contains(text, "[ Switch ]") ||
			strings.Contains(text, "close all tabs") || strings.Contains(text, "Close 0") {
			t.Fatalf("dialog in %q", text)
		}
		if !accept {
			_, _ = m.Update(key(tea.KeyEnter, 0, ""))
			if m.confirm != nil || m.ctxSpace != "default" || m.welcome.tip.command == "space use lab" {
				t.Fatal("Keep must stay on the current space")
			}
			continue
		}
		_, _ = m.Update(key('y', 0, "y"))
		if m.ctxSpace != "lab" || m.welcome.tip.command != "space use lab" {
			t.Fatalf("space %q tip %+v after yes", m.ctxSpace, m.welcome.tip)
		}
	}
}

func TestFollowCreateWithTabsUsesTheCloseDialog(t *testing.T) {
	for _, current := range []bool{false, true} {
		m := followModel(t, map[string]bool{"default": current, "lab": true})
		tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"), paneSession(t, 20, 80, "sleep", "30"))
		m.focusPalette = true
		_, _ = m.Update(answer(t, runToEnd(t, m, "space", "create", "lab")))
		if m.confirm == nil || m.confirm.space != "lab" || len(m.confirm.all) != 2 || m.ctxSpace != "default" {
			t.Fatalf("current=%v: tabs must ask first", current)
		}
		if text := ansi.Strip(m.View().Content); !strings.Contains(text, "Switch from default to lab and close all") ||
			!strings.Contains(text, "Close 1 tab / 2 panes.") || !strings.Contains(text, "[ Close tabs & switch ]") {
			t.Fatalf("current=%v: dialog in %q", current, text)
		}
		_, _ = m.Update(key('y', 0, "y"))
		if m.tabs.Index(tab) >= 0 || m.ctxSpace != "lab" || m.welcome.tip.command != "space use lab" {
			t.Fatalf("current=%v: yes did not close the tabs and switch", current)
		}
	}
}

// The dialog takes every key: it never opens over a tab being typed in.
func TestFollowCreateFocusInATabOnlyNotifies(t *testing.T) {
	m := followModel(t, map[string]bool{"default": true, "lab": true})
	tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"))
	msg := answer(t, runToEnd(t, m, "space", "create", "lab"))
	m.focusPalette = false
	_, _ = m.Update(msg)
	if m.confirm != nil || m.ctxSpace != "default" || m.tabs.Index(tab) < 0 {
		t.Fatal("a dialog opened over the focused tab")
	}
	if n := m.noticeText(); !strings.Contains(n, "Space lab was created. To switch this TUI to it, click default ▾ or press") || !strings.HasSuffix(n, " then s") {
		t.Fatalf("notice = %q", n)
	}
}

// Nor over a palette whose next key already belongs to something else: a
// draft being typed, notice history or the pane menu. The next y goes there.
func TestFollowCreateBusyPaletteOnlyNotifies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *model)
		after func(t *testing.T, m *model)
	}{
		{"draft", func(m *model) { m.pal.Insert("box l") }, func(t *testing.T, m *model) {
			if line, _ := m.pal.Line(); line != "box ly" {
				t.Fatalf("draft = %q: y did not reach the palette", line)
			}
		}},
		{"notice history", func(m *model) {
			m.postNotice(prettylog.KindInfo, "earlier notice")
			_, _ = m.Update(key('n', tea.ModCtrl, ""))
			if !m.noticeFocused {
				t.Fatal("Ctrl+N did not focus notice history")
			}
		}, nil},
		{"suggestions on an empty line", func(m *model) {
			_, _ = m.Update(suggestionsMsg{Line: "", Items: candidates("agent", "box", "space"), Nav: true, Generation: m.spaceGeneration})
			if !m.menuShown() || !strings.Contains(ansi.Strip(m.View().Content), "agent") {
				t.Fatal("Down on the empty palette did not show the suggestions")
			}
		}, func(t *testing.T, m *model) {
			if line, _ := m.pal.Line(); line != "y" {
				t.Fatalf("draft = %q: y did not reach the palette", line)
			}
		}},
		{"pane resize window", func(m *model) {
			// After a resize key, Ctrl or Alt with an arrow keeps moving the
			// border; the dialog would take Ctrl+→ as Keep → Close.
			m.resizeTab, m.resizeUntil = m.tabs.Current(), clock().Add(resizeWindow)
			m.focusPalette = true
			if !m.resizing() {
				t.Fatal("the resize window is not open")
			}
		}, nil},
		{"pane menu", func(m *model) {
			_, _ = m.Update(key('q', tea.ModCtrl, ""))
			if m.prefix == nil {
				t.Fatal("Ctrl+Q did not arm the prefix")
			}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := followModel(t, map[string]bool{"default": true, "lab": true})
			tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"))
			m.focusPalette = true
			msg := answer(t, runToEnd(t, m, "space", "create", "lab"))
			tc.setup(m)
			_, _ = m.Update(msg)
			if m.confirm != nil {
				t.Fatal("the dialog opened over a palette in use")
			}
			if tc.name == "suggestions on an empty line" && !m.menuShown() {
				t.Fatal("the answer hid the suggestions")
			}
			if !slices.ContainsFunc(m.notices, func(n toastNotice) bool { return strings.HasPrefix(n.text, "Space lab was created.") }) {
				t.Fatal("the switch notice is missing")
			}
			_, _ = m.Update(key('y', 0, "y"))
			if m.confirm != nil || m.tabs.Index(tab) < 0 || m.ctxSpace != "default" {
				t.Fatal("the next y closed tabs or switched")
			}
			if tc.after != nil {
				tc.after(t, m)
			}
		})
	}
}

func TestFollowCreateIdlePaletteOpensTheDialog(t *testing.T) {
	m := followModel(t, map[string]bool{"default": true, "lab": true})
	tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"))
	m.focusPalette = true
	_, _ = m.Update(answer(t, runToEnd(t, m, "space", "create", "lab")))
	if m.confirm == nil || !m.confirm.created {
		t.Fatal("an idle palette must ask")
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.confirm != nil || m.tabs.Index(tab) < 0 || m.ctxSpace != "default" {
		t.Fatal("Enter must keep")
	}
}

func TestFollowCreateFailureAndStaleAnswers(t *testing.T) {
	m := followModel(t, map[string]bool{"work": true, "lab": true})
	// A failed create (refused, already exists, or failed after saving the
	// record) never moves the TUI.
	if runToEnd(t, m, "space", "create", "work", "fail") != nil || m.created != nil {
		t.Fatal("a failed create was checked")
	}
	// Anything but a create with a name has nothing to follow.
	if check := runToEnd(t, m, "space", "ls"); check == nil || check() != nil {
		t.Fatal("a non-create command named a space")
	}
	// A newer command makes an answer stale.
	msg := answer(t, runToEnd(t, m, "space", "create", "work"))
	if runToEnd(t, m, "space", "ls") == nil {
		t.Fatal("space ls was not recorded")
	}
	_, _ = m.Update(msg)
	if m.ctxSpace != "default" {
		t.Fatal("an answer for an older command switched")
	}
	// A manual switch while the check runs drops it.
	msg = answer(t, runToEnd(t, m, "space", "create", "lab"))
	m.spaceGeneration++
	_, _ = m.Update(msg)
	if m.ctxSpace != "default" || m.confirm != nil {
		t.Fatal("an answer from before a switch acted")
	}
	// A create whose start failed is not followed on the earlier exit 0.
	executable = func() (string, error) { return filepath.Join(t.TempDir(), "missing"), nil }
	if runToEnd(t, m, "space", "create", "work") != nil {
		t.Fatal("a create that never started was checked")
	}
}

func TestFollowCreateBoundTUI(t *testing.T) {
	m := followModel(t, map[string]bool{"work": true})
	m.spaceBound = true
	if runToEnd(t, m, "space", "create", "work") != nil || m.created != nil {
		t.Fatal("a bound TUI recorded a create")
	}
}

func TestConsoleLastExit(t *testing.T) {
	m := newTestModel(t, 80, 24)
	for _, code := range []string{"3", "0"} {
		if started, err := m.console.Run(nil, "", "sh", "-c", "exit "+code); !started || err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return !m.console.Busy() })
		if got := m.console.LastExit(); got != int(code[0]-'0') {
			t.Fatalf("LastExit = %d, want %s", got, code)
		}
	}
}
