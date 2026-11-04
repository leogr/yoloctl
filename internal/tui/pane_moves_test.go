package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// sleepTab adds a tab of one or more sleeping panes (split as splitTab
// does) and returns it with its panes.
func sleepTab(t *testing.T, m *model, n int) (*tabs.Tab, []*panes.Pane) {
	t.Helper()
	var ss []*term.Session
	for range n {
		ss = append(ss, paneSession(t, 20, 80, "sleep", "30"))
	}
	tab := splitTab(t, m, ss...)
	return tab, tab.Layout.Panes()
}

func running(t *testing.T, ps ...*panes.Pane) {
	t.Helper()
	for _, p := range ps {
		select {
		case <-p.Session.Done():
			t.Fatalf("%q's client ended: a move closes no session", p.Command)
		default:
		}
	}
}

// R4 with prefix j: the picker opens in join mode; a digit brings that tab's
// whole layout below the focused pane at once, without closing or starting
// a session; the tab leaves the strip and the joined tree's focused pane
// takes focus. Refusals keep the picker open and change nothing.
func TestPrefixJoinBringsATabBelow(t *testing.T) {
	m := newTestModel(t, 120, 40)
	a, ap := sleepTab(t, m, 1)
	b, bp := sleepTab(t, m, 2)
	_, cp := sleepTab(t, m, 1)
	e, _ := sleepTab(t, m, 1)
	m.tabs.Select(m.tabs.Index(e))
	prefixed(m, key('|', tea.ModShift, "|"))
	m.tabs.Select(0)
	m.focusPalette = false
	prefixed(m, key('j', 0, "j"))
	if m.picker == nil || !m.picker.join || !strings.Contains(overlayText(m), "bring a tab in below the focused pane") {
		t.Fatal("j opens the picker in join mode")
	}
	popup := overlayText(m)
	if !strings.Contains(popup, "❶ first · that is this tab") || !strings.Contains(popup, "❹ first · tab 4 has an empty pane") {
		t.Fatalf("refused rows are greyed with their reason:\n%s", popup)
	}
	for _, c := range []struct{ digit, notice string }{{"1", "that is this tab"}, {"9", "no tab 9"}, {"4", "tab 4 has an empty pane"}} {
		_, _ = m.Update(key(rune(c.digit[0]), 0, c.digit))
		if m.picker == nil || m.picker.notice != c.notice || len(m.tabs.Tabs) != 4 || len(a.Layout.Panes()) != 1 {
			t.Fatalf("digit %s is refused with %q and changes nothing", c.digit, c.notice)
		}
	}
	_, _ = m.Update(key('2', 0, "2"))
	if m.picker != nil || len(m.tabs.Tabs) != 3 || m.tabs.Index(b) >= 0 || m.tabs.Current() != a || m.focusPalette {
		t.Fatalf("2 brings tab ❷ in: %d tabs", len(m.tabs.Tabs))
	}
	ps := a.Layout.Panes()
	if len(ps) != 3 || ps[0] != ap[0] || ps[1] != bp[0] || ps[2] != bp[1] || a.Focused() != bp[1] {
		t.Fatal("tab ❷'s whole layout is grafted, panes kept, its focused pane focused")
	}
	if r0, r1 := ap[0].Rect(), bp[0].Rect(); r1.Y != r0.Y+r0.H+1 || r1.X != 0 {
		t.Fatalf("below the focused pane: %+v %+v", r0, r1)
	}
	if bp[0].Session.LocalGeometry().Cols != bp[0].Rect().W {
		t.Fatal("the joined panes are laid out and resized")
	}
	running(t, ap[0], bp[0], bp[1], cp[0])
	// Delayed events follow the pane to its new tab.
	m.tabs.Select(1)
	_, _ = m.Update(paneBellMsg{from: bp[0].Session})
	_, _ = m.Update(tabExitMsg{session: bp[1].Session})
	if !a.Badge || !bp[0].Mark || !bp[1].Exited {
		t.Fatal("a moved pane's bell and exit reach it in its new tab")
	}
	// Into the empty pane: tab ❷ (c) fills tab ❸'s empty pane.
	m.tabs.Select(m.tabs.Index(e))
	m.focusPalette = true
	prefixed(m, key('j', 0, "j"))
	if !strings.Contains(overlayText(m), "bring a tab into the empty pane") {
		t.Fatal("with an empty pane the join lands in it")
	}
	slot := e.Layout.Empty().Rect()
	_, _ = m.Update(key('2', 0, "2"))
	if e.Layout.Empty() != nil || len(e.Layout.Panes()) != 2 || e.Focused() != cp[0] || cp[0].Rect() != slot || m.focusPalette {
		t.Fatal("the tab fills the empty pane in place and takes focus")
	}
	running(t, cp[0])
}

// The preflight refuses a tree that would not fit, changing nothing; the
// switch picker never grafts.
func TestJoinTooSmallChangesNothing(t *testing.T) {
	m := newTestModel(t, 60, 16)
	x, xp := sleepTab(t, m, 2)
	y, yp := sleepTab(t, m, 2)
	m.tabs.Select(0)
	before := []panes.Rect{xp[0].Rect(), xp[1].Rect(), yp[0].Rect(), yp[1].Rect()}
	prefixed(m, key('j', 0, "j"))
	_, _ = m.Update(key('2', 0, "2"))
	if m.picker == nil || m.picker.notice != "too small for tab ❷'s panes" {
		t.Fatalf("too small: %+v", m.picker)
	}
	after := []panes.Rect{xp[0].Rect(), xp[1].Rect(), yp[0].Rect(), yp[1].Rect()}
	for i := range before {
		if before[i] != after[i] || len(m.tabs.Tabs) != 2 || len(x.Layout.Panes()) != 2 || len(y.Layout.Panes()) != 2 {
			t.Fatal("a refused join changes nothing")
		}
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	prefixed(m, key('w', 0, "w"))
	_, _ = m.Update(key('2', 0, "2"))
	if len(m.tabs.Tabs) != 2 || m.tabs.Active != 1 || len(x.Layout.Panes()) != 2 {
		t.Fatal("the switch picker switches, never grafts")
	}
}

// K3: join mode captures its destination; a tab switch or a focus move
// cancels it rather than join elsewhere.
func TestJoinPickerCancelsWhenTheDestinationChanges(t *testing.T) {
	m := newTestModel(t, 120, 40)
	a, ap := sleepTab(t, m, 2)
	sleepTab(t, m, 1)
	m.tabs.Select(0)
	a.Layout.SetFocus(ap[0])
	prefixed(m, key('j', 0, "j"))
	a.Layout.SetFocus(ap[1]) // an attach request or a click moved focus
	_, _ = m.Update(wakeMsg{})
	if m.picker != nil || !strings.Contains(shownNotice(m), "destination changed") || m.activeOverlay() == nil {
		t.Fatal("a focus move cancels the join picker; its notice shows in the inert popup")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, "")) // dismisses the notice
	prefixed(m, key('j', 0, "j"))
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	if m.picker != nil || len(m.tabs.Tabs) != 2 {
		t.Fatal("a tab switch cancels the join picker")
	}
}

// prefix !: the focused pane of a divided tab moves to a new tab at the end
// of the strip, which becomes active; an undivided tab refuses; a tab left
// with only its empty pane goes.
func TestPrefixBreakPane(t *testing.T) {
	m := newTestModel(t, 120, 40)
	one, op := sleepTab(t, m, 1)
	a, ap := sleepTab(t, m, 2)
	m.tabs.Select(0)
	prefixed(m, key('1', tea.ModShift, "!"))
	if shownNotice(m) != "already its own tab" || len(m.tabs.Tabs) != 2 {
		t.Fatalf("an undivided tab: %q", shownNotice(m))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, "")) // the refusal kept the menu open
	m.tabs.Select(1)
	a.Layout.SetFocus(ap[0])
	prefixed(m, key('1', tea.ModShift, "!"))
	if len(m.tabs.Tabs) != 3 || m.tabs.Active != 2 || m.tabs.Current().Focused() != ap[0] || len(a.Layout.Panes()) != 1 {
		t.Fatalf("! breaks the pane out to the last tab: %d tabs", len(m.tabs.Tabs))
	}
	if ap[0].Session.LocalGeometry().Cols != m.width || ap[1].Session.LocalGeometry().Cols != m.width {
		t.Fatal("both tabs are laid out again")
	}
	running(t, op[0], ap[0], ap[1])
	m.tabs.Select(0)
	prefixed(m, key('|', tea.ModShift, "|"))
	one.Layout.SetFocus(op[0])
	prefixed(m, key('1', tea.ModShift, "!"))
	if m.tabs.Index(one) >= 0 || m.tabs.Current().Focused() != op[0] {
		t.Fatal("a tab left with only its empty pane goes")
	}
	prefixed(m, key('|', tea.ModShift, "|"))
	prefixed(m, key('1', tea.ModShift, "!"))
	if shownNotice(m) != "an empty pane cannot be its own tab" || m.prefix == nil {
		t.Fatalf("the empty pane does not break out: %q", shownNotice(m))
	}
	running(t, op[0])
}

// A held key's release and a mouse gesture begun before a move reach their
// pane after it, in its new tab, at its new origin.
func TestHeldKeyAndGestureAcrossAMove(t *testing.T) {
	m := newTestModel(t, 100, 26)
	_, input := mouseCapture(t, m, "\x1b[>3u"+dragSGR) // kitty event types: releases are reported
	src := m.tabs.Current()
	cp := src.Focused()
	dst, _ := sleepTab(t, m, 1)
	m.tabs.Select(0)
	m.focusPalette = false
	_, _ = m.Update(key('k', 0, "k"))
	top := m.tabTop()
	_, _ = m.Update(click(3, top+2))
	m.tabs.Select(1)
	m.focusPalette = false
	prefixed(m, key('j', 0, "j"))
	_, _ = m.Update(key('1', 0, "1"))
	if m.tabs.Index(src) >= 0 || dst.Layout.Panes()[1] != cp {
		t.Fatal("setup: the capture tab joined below")
	}
	r := cp.Rect()
	_, _ = m.Update(tea.KeyReleaseMsg{Code: 'k', Text: "k"})
	_, _ = m.Update(releaseAt(r.X+4, m.tabTop()+r.Y+1))
	got := input()
	if !strings.Contains(got, "\x1b[107;1:3u") || !strings.Contains(got, "\x1b[<0;5;2m") {
		t.Fatalf("the release and the mouse release reach the moved pane at its new origin: %q", got)
	}
}

// R3/MUST-4: the empty pane is audited on every session path: it draws its
// hint and an `empty` bar, takes no cursor, input, paste, scroll, selection
// or focus report, survives a host resize, and closes without a session.
func TestEmptyPaneOnEverySessionPath(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, right := twoPanes(t, m)
	tab.Layout.SetFocus(left)
	prefixed(m, key('-', 0, "-"))
	e := tab.Layout.Empty()
	if e == nil || tab.Focused() != e || !m.focusPalette {
		t.Fatal("setup: a focused empty pane below left")
	}
	v := m.View()
	plain := ansi.Strip(v.Content)
	for _, want := range []string{"what goes here?", "type a yoloctl command ↵", "^Q j N brings tab N here", "Esc removes it", " ▸ empty"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the hint says %q:\n%s", want, plain)
		}
	}
	if v.Cursor == nil || v.Cursor.Y != 0 || v.WindowTitle != "yoloctl — first" {
		t.Fatalf("the palette has the cursor and the tab keeps its title: %+v %q", v.Cursor, v.WindowTitle)
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if e.Rect().W != left.Rect().W || m.wantedMouseTracking() != 1002 {
		t.Fatal("a host resize lays the empty pane out; it wants no mouse mode")
	}
	_, _ = m.Update(key('y', tea.ModCtrl, "")) // palette key: hides the palette
	if m.focusPalette || m.hostCursor() != nil {
		t.Fatal("the palette key hides the palette; the empty pane shows no cursor")
	}
	m.syncFocus()
	_, _ = m.Update(tea.PasteMsg{Content: "x"})
	if m.status != "nothing takes input here" {
		t.Fatalf("paste is refused: %q", m.status)
	}
	er := e.Rect()
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: er.X + 2, Y: m.tabTop() + er.Y + 1})
	_, _ = m.Update(key('c', tea.ModCtrl|tea.ModShift, ""))
	_, _ = m.Update(click(er.X+2, m.tabTop()+er.Y+1))
	_, _ = m.Update(motionAt(er.X+6, m.tabTop()+er.Y+2))
	_, _ = m.Update(releaseAt(er.X+6, m.tabTop()+er.Y+2))
	if !m.focusPalette || m.selectionPointer != nil {
		t.Fatal("a click on the empty pane opens its palette; nothing selects")
	}
	n := 0
	m.eachSession(func(*panes.Pane) { n++ })
	if n != 2 {
		t.Fatalf("shutdown closes the two sessions only, not the empty pane: %d", n)
	}
	m.focusPalette = false
	x, y := barCell(t, m, tab, e, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if tab.Layout.Empty() != nil || m.confirm != nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("its ✕ removes it at once")
	}
	// A tab left with only its empty pane goes with its last pane.
	tab.Layout.SetFocus(left)
	prefixed(m, key('|', tea.ModShift, "|"))
	m.detachPane(tab, left)
	m.detachPane(tab, right)
	if m.tabs.Index(tab) >= 0 {
		t.Fatal("the empty pane does not keep a tab alone")
	}
}

// R7 dock, the required sequence: tab A active, press inactive tab B's
// label, drag over A's right half, release: B leaves the strip and its tree
// sits right of A's pane, with no intermediate activation of B.
func TestDockDragSequence(t *testing.T) {
	m := newTestModel(t, 120, 30)
	a, ap := sleepTab(t, m, 1)
	_, bp := sleepTab(t, m, 2)
	m.tabs.Select(0)
	m.focusPalette = false
	row, x := m.tabStripRow(), tabClickX(m, 1)
	_, _ = m.Update(click(x, row))
	if m.tabs.Active != 0 || m.chrome == nil {
		t.Fatal("a press on a label changes nothing")
	}
	r, top := ap[0].Rect(), m.tabTop()
	_, _ = m.Update(motionAt(r.X+r.W-4, top+r.H/2))
	if m.chrome.kind != chromeDock || m.tabs.Active != 0 || m.status != dockStatus {
		t.Fatalf("motion starts a dock drag with A still visible: %q", m.status)
	}
	v := m.View().Content
	if !strings.Contains(v, "48;5;94") || !strings.Contains(v, "\x1b[7m") {
		t.Fatal("the passing half is tinted and the dragged label is in reverse video")
	}
	_, _ = m.Update(releaseAt(r.X+r.W-4, top+r.H/2))
	ps := a.Layout.Panes()
	if len(m.tabs.Tabs) != 1 || len(ps) != 3 || ps[1] != bp[0] || ps[2] != bp[1] || m.status != "" || m.chrome != nil {
		t.Fatalf("B docked right of A: %d tabs, %d panes, %q", len(m.tabs.Tabs), len(ps), m.status)
	}
	if ap[0].Rect().X != 0 || bp[0].Rect().X <= ap[0].Rect().X+ap[0].Rect().W || a.Focused() != bp[1] {
		t.Fatal("the docked tree is on the right and its focused pane is focused")
	}
	if strings.Contains(m.View().Content, "48;5;94") {
		t.Fatal("the target is gone after the drop")
	}
	running(t, ap[0], bp[0], bp[1])
}

// R7 dock: the side follows the pointer, a refused candidate is hatched
// with its reason, chrome is no target, the empty pane is a whole target,
// the active tab cannot dock into itself, and a drop elsewhere does nothing.
func TestDockDragTargets(t *testing.T) {
	m := newTestModel(t, 60, 16)
	a, ap := sleepTab(t, m, 1)
	b, _ := sleepTab(t, m, 2)
	m.tabs.Select(0)
	m.focusPalette = false
	row, top := m.tabStripRow(), m.tabTop()
	r := ap[0].Rect()
	_, _ = m.Update(click(tabClickX(m, 1), row))
	_, _ = m.Update(motionAt(r.X+r.W-2, top+r.H/2))
	if m.status != "too small for tab ❷'s panes" || !strings.Contains(ansi.Strip(m.View().Content), "╱╱╱") {
		t.Fatalf("right of a 60-column pane, two 20-column panes do not fit: %q", m.status)
	}
	_, _ = m.Update(motionAt(r.X+r.W/2, top+r.H-1))
	if m.status != dockStatus || !strings.Contains(m.View().Content, "48;5;94") {
		t.Fatalf("below it they fit: %q", m.status)
	}
	_, _ = m.Update(motionAt(5, row))
	if strings.Contains(m.View().Content, "48;5;94") || m.status != dockStatus {
		t.Fatal("the strip is no target")
	}
	_, _ = m.Update(releaseAt(5, row))
	if len(m.tabs.Tabs) != 2 || m.tabs.Active != 0 {
		t.Fatal("a drop on the strip does nothing and switches nothing")
	}
	_, _ = m.Update(click(tabClickX(m, 1), row))
	_, _ = m.Update(motionAt(r.X+r.W-2, top+r.H/2))
	_, _ = m.Update(releaseAt(r.X+r.W-2, top+r.H/2))
	if len(m.tabs.Tabs) != 2 || m.status != "too small for tab ❷'s panes" {
		t.Fatal("a refused drop changes nothing and says why")
	}
	m.status = ""
	_, _ = m.Update(click(tabClickX(m, 0), row))
	_, _ = m.Update(motionAt(r.X+5, top+3))
	if m.status != "drop needs another tab" {
		t.Fatalf("the active tab has no target: %q", m.status)
	}
	_, _ = m.Update(releaseAt(r.X+5, top+3))
	if len(m.tabs.Tabs) != 2 || m.status != "" {
		t.Fatal("dropping the active tab does nothing")
	}
	// The empty pane is a whole target: dropping on it fills it.
	prefixed(m, key('-', 0, "-"))
	m.focusPalette = false
	e := a.Layout.Empty()
	er := e.Rect()
	_, _ = m.Update(click(tabClickX(m, 1), m.tabStripRow()))
	_, _ = m.Update(motionAt(er.X+1, m.tabTop()+er.Y))
	_, _ = m.Update(releaseAt(er.X+1, m.tabTop()+er.Y))
	if a.Layout.Empty() != nil || len(a.Layout.Panes()) != 3 || m.tabs.Index(b) >= 0 {
		t.Fatalf("a drop on the empty pane fills it: %q", m.status)
	}
}

// R7 dock cancels: Esc, host focus loss, a keyboard tab switch, another
// button, and the dragged tab going; the preflight is repeated at release.
func TestDockDragCancels(t *testing.T) {
	m := newTestModel(t, 120, 30)
	a, ap := sleepTab(t, m, 1)
	b, _ := sleepTab(t, m, 2)
	m.tabs.Select(0)
	m.focusPalette = false
	r := ap[0].Rect()
	drag := func() (int, int) {
		t.Helper()
		_, _ = m.Update(click(tabClickX(m, m.tabs.Index(b)), m.tabStripRow()))
		x, y := r.X+r.W-4, m.tabTop()+r.H/2
		_, _ = m.Update(motionAt(x, y))
		if m.chrome == nil || m.chrome.kind != chromeDock {
			t.Fatal("setup: a dock drag")
		}
		return x, y
	}
	for _, c := range []struct {
		name   string
		cancel tea.Msg
	}{
		{"Esc", key(tea.KeyEscape, 0, "")},
		{"focus loss", tea.BlurMsg{}},
		{"another button", tea.MouseClickMsg{Button: tea.MouseRight, X: 3, Y: 5}},
	} {
		x, y := drag()
		_, _ = m.Update(c.cancel)
		_, _ = m.Update(releaseAt(x, y))
		if m.chrome != nil || len(m.tabs.Tabs) != 2 || m.status != "" || strings.Contains(m.View().Content, "48;5;94") {
			t.Fatalf("%s cancels the dock drag", c.name)
		}
		_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseRight, X: 3, Y: 5})
		_, _ = m.Update(tea.FocusMsg{})
	}
	x, y := drag()
	m.focusPalette = true
	_, _ = m.Update(key(tea.KeyRight, tea.ModCtrl, "")) // a keyboard tab switch
	_, _ = m.Update(releaseAt(x, y))
	if m.chrome != nil || len(m.tabs.Tabs) != 2 || strings.Contains(a.Layout.Panes()[0].Command, "next") {
		t.Fatal("a keyboard tab switch cancels the dock drag")
	}
	m.tabs.Select(0)
	m.focusPalette = false
	// The destination changes under the drag: the drop is checked again.
	x, y = drag()
	if err := a.Layout.Split(true, &panes.Pane{Session: paneSession(t, 20, 80, "sleep", "30"), Command: "late"}); err != nil {
		t.Fatal(err)
	}
	m.layoutTab(a)
	_, _ = m.Update(releaseAt(x, y))
	if len(m.tabs.Tabs) != 2 || len(a.Layout.Panes()) != 2 || !strings.HasPrefix(m.status, "too small") {
		t.Fatalf("the preflight repeated at release refuses: %q", m.status)
	}
	m.status = ""
	// A host resize mid-drag: the drop still lands by the current geometry.
	a.Layout.Remove(a.Layout.Panes()[1])
	m.layoutTab(a)
	drag()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	r = ap[0].Rect()
	_, _ = m.Update(releaseAt(r.X+r.W-4, m.tabTop()+r.H/2))
	if len(m.tabs.Tabs) != 1 || len(a.Layout.Panes()) != 3 {
		t.Fatal("a host resize mid-drag keeps the drag")
	}
}

// The dragged tab removed mid-drag cancels it.
func TestDockDragSourceGone(t *testing.T) {
	m := newTestModel(t, 120, 30)
	_, ap := sleepTab(t, m, 1)
	b, _ := sleepTab(t, m, 1)
	m.tabs.Select(0)
	m.focusPalette = false
	r := ap[0].Rect()
	_, _ = m.Update(click(tabClickX(m, 1), m.tabStripRow()))
	_, _ = m.Update(motionAt(r.X+r.W-4, m.tabTop()+r.H/2))
	m.detachTab(b)
	_, _ = m.Update(motionAt(r.X+r.W-5, m.tabTop()+r.H/2))
	_, _ = m.Update(releaseAt(r.X+r.W-5, m.tabTop()+r.H/2))
	if m.chrome != nil || len(m.tabs.Tabs) != 1 || len(m.tabs.Current().Layout.Panes()) != 1 || m.status != "" {
		t.Fatal("the dragged tab going cancels the drag")
	}
}

// A dock drag delivers nothing to any child: press, motion and release
// stay with the chrome even over a mouse-tracking pane.
func TestDockDragDeliversNothingToChildren(t *testing.T) {
	m, _, right, left, rightInput := twoCapturePanes(t)
	sleepTab(t, m, 1)
	m.tabs.Select(0)
	m.focusPalette = false
	rr := right.Rect()
	_, _ = m.Update(click(tabClickX(m, 1), m.tabStripRow()))
	_, _ = m.Update(motionAt(rr.X+3, m.tabTop()+2))
	_, _ = m.Update(motionAt(4, m.tabTop()+3))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseNone, X: 5, Y: m.tabTop() + 3})
	_, _ = m.Update(releaseAt(30, m.tabStripRow()))
	if l, r := left(), rightInput(); l != "" || r != "" {
		t.Fatalf("children received %q and %q", l, r)
	}
}
