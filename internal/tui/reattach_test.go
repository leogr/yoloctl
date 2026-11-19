package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

func TestReattachArgs(t *testing.T) {
	chat := []string{"box", "attach", "api", "--space", "dev", "--chat-room-id", "r1", "--chat-member-id", "m1", "--chat-box-id", "b1", "--chat-allow-start"}
	for _, tc := range []struct {
		name string
		args []string
		want []string
		box  string
		ok   bool
	}{
		{"box attach", []string{"box", "attach", "claude"}, []string{"box", "attach", "claude"}, "claude", true},
		{"root attach", []string{"attach", "claude", "--space", "dev"}, []string{"attach", "claude", "--space", "dev"}, "claude", true},
		{"flag first", []string{"box", "attach", "--space", "dev", "api"}, []string{"box", "attach", "--space", "dev", "api"}, "api", true},
		{"space= form", []string{"box", "attach", "--space=dev", "api", "--passthrough"}, []string{"box", "attach", "--space=dev", "api", "--passthrough"}, "api", true},
		{"new session dropped", []string{"box", "attach", "api", "--new-session"}, []string{"box", "attach", "api"}, "api", true},
		{"new session=true dropped", []string{"box", "attach", "--new-session=true", "api"}, []string{"box", "attach", "api"}, "api", true},
		{"new session=false dropped", []string{"box", "attach", "api", "--new-session=false"}, []string{"box", "attach", "api"}, "api", true},
		{"chat guards kept", chat, chat, "api", true},
		{"invalid name not shown", []string{"box", "attach", "Bad/Name"}, []string{"box", "attach", "Bad/Name"}, "", true},
		{"help", []string{"box", "attach", "api", "--help"}, nil, "", false},
		{"box-use", []string{"box", "attach", "api", "--box-use"}, nil, "", false},
		{"logs follow", []string{"box", "logs", "-f", "api"}, nil, "", false},
		{"lifecycle auto-attach", []string{"box", "start", "api"}, nil, "", false},
		{"exec", []string{"box", "exec", "api", "--", "bash"}, nil, "", false},
		{"empty", nil, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, box, ok := reattachArgs(tc.args)
			if ok != tc.ok || box != tc.box || !slices.Equal(got, tc.want) {
				t.Fatalf("reattachArgs(%q) = %q, %q, %v; want %q, %q, %v", tc.args, got, box, ok, tc.want, tc.box, tc.ok)
			}
		})
	}
	m := &model{standalone: true}
	if h := m.reattachHint([]string{"box", "attach", "api", "--space", "fixture"}); h != "" {
		t.Fatalf("a standalone attach offers no re-attach: %q", h)
	}
	m.standalone = false
	if h := m.reattachHint([]string{"box", "attach", "api", "--space", "fixture"}); h != "r to re-attach to api" {
		t.Fatalf("hint %q", h)
	}
	if h := m.reattachHint([]string{"box", "logs", "-f", "api", "--space", "fixture"}); h != "r to reopen" {
		t.Fatalf("a followed log offers reopen: %q", h)
	}
}

// endedAttach is an exited pane started with args, as startPane records it.
func endedAttach(t *testing.T, args ...string) *panes.Pane {
	t.Helper()
	args = pinReopenArgs(args, func() (string, error) { return "fixture", nil })
	s := paneSession(t, 10, 40, "true")
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	return &panes.Pane{Session: s, Command: "yoloctl " + strings.Join(args, " "), Args: args, Exited: true}
}

// splitWithEnded is a tab of a live pane and, right of it, an ended view,
// focused and zoomed.
func splitWithEnded(t *testing.T, m *model, ended *panes.Pane) (*tabs.Tab, *panes.Pane) {
	t.Helper()
	live := &panes.Pane{Session: paneSession(t, 20, 100, "sleep", "30"), Command: "yoloctl space view"}
	tab := tabs.New(live)
	m.tabs.Add(tab)
	m.tabs.Select(len(m.tabs.Tabs) - 1)
	m.layoutTab(tab)
	if err := tab.Layout.Split(true, ended); err != nil {
		t.Fatal(err)
	}
	tab.Layout.SetFocus(ended)
	m.layoutTab(tab)
	m.focusPalette = false
	return tab, live
}

// r on an ended box attach runs its own attach again in the same slot:
// split, zoom and focus kept, --new-session dropped; the replaced
// session's late exit does not touch the new view.
func TestEndedAttachReattachesInPlace(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return truePath(t), nil } // never the test binary
	m := newTestModel(t, 100, 26)
	ended := endedAttach(t, "box", "attach", "api", "--space", "dev", "--new-session")
	tab, live := splitWithEnded(t, m, ended)
	tab.Layout.ToggleZoom()
	m.layoutTab(tab)
	slot := ended.Rect()

	_, _ = m.Update(key('r', 0, "r"))
	p := tab.Focused()
	if p == ended || p.Exited || !slices.Equal(p.Args, []string{"box", "attach", "api", "--space", "dev"}) ||
		p.Command != "yoloctl box attach api --space dev" {
		t.Fatalf("r re-attaches with the same attach minus --new-session: %q exited=%v", p.Args, p.Exited)
	}
	if ps := tab.Layout.Panes(); len(ps) != 2 || ps[0] != live || ps[1] != p || p.Rect() != slot ||
		tab.Layout.Zoomed() != p || m.focusPalette || len(m.tabs.Tabs) != 1 {
		t.Fatalf("the new view takes the ended view's slot: %+v vs %+v, zoom %v", p.Rect(), slot, tab.Layout.Zoomed() == p)
	}
	select {
	case <-ended.Session.Done():
	default:
		t.Fatal("the ended view's session was not closed")
	}
	p.Session.Close() // its `true` exits; only this test's late message may arrive
	_, _ = m.Update(tabExitMsg{session: ended.Session})
	if p.Exited || p.Mark || tab.Focused() != p {
		t.Fatal("the replaced session's late exit changed the new view")
	}
}

// Only an ended attach view re-attaches; Enter still closes, and a failed
// local start keeps the ended view.
func TestReattachOnlyEndedAttachViews(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return truePath(t), nil }
	m := newTestModel(t, 100, 26)
	logs := endedAttach(t, "box", "logs", "api")
	tab, _ := splitWithEnded(t, m, logs)
	_, _ = m.Update(key('r', 0, "r"))
	if tab.Focused() != logs || len(m.endedButtonSlots()) != 1 || m.endedButtonSlots()[0].label != endedCloseButton {
		t.Fatal("r on a finite ended view does nothing, and only Close is offered")
	}

	ended := endedAttach(t, "box", "attach", "api")
	tab2, _ := splitWithEnded(t, m, ended)
	executable = func() (string, error) { return "", errors.New("gone") }
	_, _ = m.Update(key('r', 0, "r"))
	if tab2.Focused() != ended || !ended.Exited || !strings.Contains(m.noticeText(), "cannot locate") {
		t.Fatalf("a failed start keeps the ended view: %q", m.noticeText())
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if len(tab2.Layout.Panes()) != 1 {
		t.Fatal("Enter still closes an ended attach view")
	}
}

// The button sits on the ended attach view's last row; a press on it
// re-attaches, a press beside it does not.
func TestReattachButton(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return truePath(t), nil }
	m := newTestModel(t, 100, 26)
	ended := endedAttach(t, "box", "attach", "api")
	tab, live := splitWithEnded(t, m, ended)
	tab.Layout.SetFocus(live) // the button works on an unfocused view too
	m.layoutTab(tab)

	slots := m.endedButtonSlots()
	r := ended.Rect()
	if len(slots) != 2 || slots[0].pane != ended || slots[0].label != endedCloseButton || slots[0].x != r.X || slots[0].y != r.Y+r.H-1 ||
		slots[1].pane != ended || slots[1].label != reattachButton || slots[1].x != r.X+len(endedCloseButton)+1 || slots[1].y != slots[0].y {
		t.Fatalf("Close before Re-attach on the last row: %+v rect %+v", slots, r)
	}
	base := make([]string, m.viewRows())
	base[slots[0].y] = strings.Repeat("x", m.width)
	m.applyEndedButtons(base)
	if row := ansi.Strip(base[slots[0].y]); !strings.HasPrefix(row[r.X:], "[ Close ] [ Re-attach ]") {
		t.Fatalf("buttons not drawn in order: %q", row)
	}
	y := m.tabTop() + slots[0].y
	gap := slots[1].x - 1
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: gap, Y: y})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: gap, Y: y})
	if len(tab.Layout.Panes()) != 2 || tab.Layout.Panes()[1] != ended {
		t.Fatal("the gap between buttons started or closed a view")
	}
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: slots[1].x, Y: y})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: slots[1].x, Y: y})
	p := tab.Layout.Panes()[1]
	if p == ended || p.Exited || tab.Focused() != p || !slices.Equal(p.Args, []string{"box", "attach", "api", "--space", "fixture"}) {
		t.Fatal("a press on the button re-attaches that view, focused")
	}
	if len(m.endedButtonSlots()) != 0 {
		t.Fatal("the live view has no button")
	}
}

func TestCloseEndedViewButton(t *testing.T) {
	for _, name := range []string{"last tab without resume", "unfocused split pane"} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t, 100, 26)
			var tab *tabs.Tab
			var live *panes.Pane
			var ended *panes.Pane
			if name == "last tab without resume" {
				ended = endedAttach(t, "box", "logs", "api")
				tab = tabs.New(ended)
				m.tabs.Add(tab)
				m.focusPalette = false
			} else {
				ended = endedAttach(t, "box", "attach", "api")
				tab, live = splitWithEnded(t, m, ended)
				live.Command = "yoloctl box edit api"
				tab.Layout.SetFocus(live)
			}
			m.layoutTab(tab)
			if live != nil {
				if _, err := live.Session.Write([]byte(strings.Repeat("older output\r\n", 80))); err != nil {
					t.Fatal(err)
				}
				live.Session.Scroll(10)
				if offset := live.Session.Frame().Offset; offset != 10 {
					t.Fatalf("live sibling offset = %d, want 10", offset)
				}
			}
			slots := m.endedButtonSlots()
			if len(slots) == 0 || slots[0].pane != ended || slots[0].label != endedCloseButton || live == nil && len(slots) != 1 {
				t.Fatalf("missing or misplaced Close: %+v", slots)
			}
			x, y := slots[0].x, slots[0].y+m.tabTop()
			_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			if owner, ok := m.mouseOwners[tea.MouseLeft]; !ok || owner.surface != nil || owner.pane != nil {
				t.Fatalf("Close press must stay chrome-owned through release: %+v", owner)
			}
			_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 0, Y: y})
			if _, ok := m.mouseOwners[tea.MouseLeft]; ok || m.confirm != nil {
				t.Fatal("release must settle the gesture, and an ended view needs no confirmation")
			}
			if live == nil {
				if m.tabs.Current() != nil || !m.focusPalette {
					t.Fatal("Close must dismiss the last tab and return focus to the palette")
				}
				return
			}
			if m.tabs.Current() != tab || tab.Focused() != live || !slices.Equal(tab.Layout.Panes(), []*panes.Pane{live}) || m.focusPalette {
				t.Fatal("Close must dismiss only the clicked pane and keep the live sibling focused")
			}
			if live.Session.Frame().Offset == 0 {
				t.Fatal("closing an unfocused pane jumped the live sibling to the bottom")
			}
		})
	}
}

// r re-attaches as the host's decoder delivers it: a kitty CSI u report
// (Ghostty) with Num Lock or Caps Lock on still counts as a plain r; a real
// modifier (Ctrl+r) does not.
func TestReattachKeyIgnoresLockModifiers(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return truePath(t), nil }
	for _, c := range []struct {
		name, seq string
		want      bool
	}{
		{"plain", "\x1b[114u", true},
		{"num lock", "\x1b[114;129u", true},
		{"caps lock", "\x1b[114;65u", true},
		{"num lock with text", "\x1b[114;129;114u", true},
		{"ctrl", "\x1b[114;5u", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var decoder uv.EventDecoder
			n, event := decoder.Decode([]byte(c.seq))
			press, ok := event.(uv.KeyPressEvent)
			if !ok || n != len(c.seq) {
				t.Fatalf("decode %q: %d %#v", c.seq, n, event)
			}
			m := newTestModel(t, 100, 26)
			ended := endedAttach(t, "box", "attach", "api")
			tab, _ := splitWithEnded(t, m, ended)
			_, _ = m.Update(tea.KeyPressMsg(press))
			if got := tab.Focused() != ended; got != c.want {
				t.Fatalf("%q (mod %v): re-attached=%v", c.seq, press.Mod, got)
			}
		})
	}
}
