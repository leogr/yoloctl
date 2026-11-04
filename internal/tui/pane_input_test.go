package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// paneCapture starts a raw PTY peer that records its exact input (as
// mouseCapture does) and adds it to the current tab as a split of the
// focused pane, right of it (vertical) or below it.
func paneCapture(t *testing.T, m *model, modes string, vertical bool) (*panes.Pane, func() string) {
	t.Helper()
	path := t.TempDir() + "/input"
	s := paneSession(t, m.viewRows(), m.width, "python3", "-c", captureMouseScript, path)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.GridText(), "READY") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(s.GridText(), "READY") {
		t.Fatal("mouse PTY did not become ready")
	}
	_, _ = s.Write([]byte(modes))
	tab := m.tabs.Current()
	p := &panes.Pane{Session: s, Command: "pane-capture"}
	if err := m.laidOut(tab).Split(vertical, p); err != nil {
		t.Fatal(err)
	}
	m.layoutTab(tab)
	return p, func() string {
		t.Helper()
		if err := s.SendInput([]byte("\x00YOLOCTL_MOUSE_END\x00")); err != nil {
			t.Fatal(err)
		}
		if err := s.Wait(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
}

const dragSGR = "\x1b[?1002h\x1b[?1006h"

// twoCapturePanes is an 80×10 model with a left and a right capture pane
// (both reporting drags in SGR), the left one focused.
func twoCapturePanes(t *testing.T) (*model, *panes.Pane, *panes.Pane, func() string, func() string) {
	t.Helper()
	m := newTestModel(t, 80, 10)
	leftSession, left := mouseCapture(t, m, dragSGR)
	right, rightInput := paneCapture(t, m, dragSGR, true)
	tab := m.tabs.Current()
	lp := tab.Layout.Panes()[0]
	if lp.Session != leftSession {
		t.Fatal("unexpected pane order")
	}
	tab.Layout.SetFocus(lp)
	m.focusPalette = false
	return m, lp, right, left, rightInput
}

// D2 and offsets: a press in the unfocused right pane focuses it and reaches
// its child at pane-local cells; the drag and release keep that owner even
// over the other pane, where the position is outside the owner (clamped by
// the terminal core to its edge).
func TestPaneMouseRoutesByOffsetAndFocusesOnPress(t *testing.T) {
	m, _, right, left, rightInput := twoCapturePanes(t)
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(click(r.X+3, top+2))
	if m.tabs.Focused() != right {
		t.Fatal("a press in an unfocused pane focuses it")
	}
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 2, Y: top + 2})
	_, _ = m.Update(releaseAt(2, top+2))
	if got, want := rightInput(), "\x1b[<0;4;3M\x1b[<32;1;3M\x1b[<0;1;3m"; got != want {
		t.Fatalf("right pane got %q, want %q", got, want)
	}
	if got := left(); got != "" {
		t.Fatalf("the gesture leaked to the left pane: %q", got)
	}
}

// The press owner keeps its pane across a focus change and a host resize
// (the release uses the pane's new origin), and a closed pane's owner is
// dropped: its release is never retargeted to the pane that now sits under
// the pointer.
func TestPaneMouseOwnerSurvivesFocusResizeAndDropsOnClose(t *testing.T) {
	m, lp, right, _, rightInput := twoCapturePanes(t)
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(click(r.X+1, top+1))
	m.tabs.Current().Layout.SetFocus(lp)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	r = right.Rect()
	_, _ = m.Update(releaseAt(r.X+5, top+1))
	// A new gesture in the left pane, which closes before the release; the
	// right pane then covers the release position.
	_, _ = m.Update(click(1, top+1))
	m.closeFocusedPane()
	_, _ = m.Update(key('y', 0, "y")) // the running command's close asks first
	if len(m.tabs.Tabs) != 1 || m.tabs.Focused() != right || right.Rect().X != 0 {
		t.Fatal("the sibling takes the tab")
	}
	_, _ = m.Update(releaseAt(1, top+1))
	if got, want := rightInput(), "\x1b[<0;2;2M\x1b[<0;6;2m"; got != want {
		t.Fatalf("right pane got %q, want %q (no retargeted release)", got, want)
	}
}

// Pixel reports are offset by the pane origin in whole measured cells.
func TestPanePixelReportsAreOffsetByThePaneOrigin(t *testing.T) {
	m := newTestModel(t, 80, 10)
	_, _ = mouseCapture(t, m, dragSGR)
	right, rightInput := paneCapture(t, m, "\x1b[?1002h\x1b[?1016h", true)
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	m.hostMouseEncoding.pixels = true
	m.focusPalette = false
	r, top := right.Rect(), m.tabTop()
	wx, wy := (r.X+3)*10+4, (top+2)*20+5 // window pixels, zero-based
	// UV subtracts one from the wire coordinates; the handler adds it back.
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: wx - 1, Y: wy - 1})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: wx - 1, Y: wy - 1})
	if got, want := rightInput(), "\x1b[<0;34;45M\x1b[<0;34;45m"; got != want {
		t.Fatalf("pixel gesture %q, want %q", got, want)
	}
}

// One host mouse mode: tracking follows the focused pane only.
func TestHostMouseTrackingFollowsTheFocusedPane(t *testing.T) {
	m := newTestModel(t, 80, 10)
	_, _ = mouseCapture(t, m, "\x1b[?1003h\x1b[?1006h") // any-motion
	right, _ := paneCapture(t, m, dragSGR, true)
	m.focusPalette = false
	tab := m.tabs.Current()
	tab.Layout.SetFocus(tab.Layout.Panes()[0])
	if got := m.wantedMouseTracking(); got != 1003 {
		t.Fatalf("left focused: tracking %d, want 1003", got)
	}
	tab.Layout.SetFocus(right)
	if got := m.wantedMouseTracking(); got != 1002 {
		t.Fatalf("right focused: tracking %d, want 1002", got)
	}
}

// A local selection started in a pane stays inside it, wherever the drag
// goes; the other pane gets no selection.
func TestPaneSelectionStaysInsideItsPane(t *testing.T) {
	m := newTestModel(t, 80, 10)
	left := paneSession(t, m.viewRows(), m.width, "sh", "-c", `printf 'LEFTSIDE text'; sleep 30`)
	right := paneSession(t, m.viewRows(), m.width, "sh", "-c", `printf 'RIGHTSIDE text'; sleep 30`)
	tab := splitTab(t, m, left, right)
	tab.Layout.SetFocus(tab.Layout.Panes()[0])
	waitFor(t, func() bool {
		return strings.Contains(left.GridText(), "LEFTSIDE") && strings.Contains(right.GridText(), "RIGHTSIDE")
	})
	top, rr := m.tabTop(), tab.Layout.Panes()[1].Rect()
	_, _ = m.Update(click(0, top))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: rr.X + 20, Y: top})
	_, _ = m.Update(releaseAt(rr.X+20, top))
	got, err := left.SelectedText()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "LEFTSIDE text") || strings.Contains(got, "RIGHT") {
		t.Fatalf("left selection %q", got)
	}
	if other, _ := right.SelectedText(); other != "" {
		t.Fatalf("the other pane got a selection: %q", other)
	}
}

func key(code rune, mod tea.KeyMod, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod, Text: text}
}

// R2, the pane keys behind the prefix: arrows move focus, Ctrl+arrows a
// border, =, z and x act on the focused pane, each returning to it; the
// palette keeps every key without the prefix, a leading - included; an
// undivided tab takes the keys as no-ops.
func TestPrefixPaneKeys(t *testing.T) {
	m := newTestModel(t, 100, 26)
	sleep := func() *term.Session { return paneSession(t, 24, 100, "sleep", "30") }
	one := sleep()
	m.tabs.Add(testTab(one, "one", false))
	m.layoutTab(m.tabs.Current())
	m.focusPalette = true
	prefixed(m, key(tea.KeyLeft, 0, ""))
	if m.focusPalette || m.prefix != nil {
		t.Fatal("an undivided tab takes ← after the prefix as a no-op and returns to the pane")
	}
	tab := m.tabs.Current()
	two, three := sleep(), sleep()
	if err := tab.Layout.Split(true, &panes.Pane{Session: two, Command: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Layout.Split(false, &panes.Pane{Session: three, Command: "three"}); err != nil {
		t.Fatal(err)
	}
	m.layoutTab(tab)
	p1, p2, p3 := tab.Layout.Panes()[0], tab.Layout.Panes()[1], tab.Layout.Panes()[2]
	press := func(k tea.KeyPressMsg) {
		t.Helper()
		m.focusPalette = true
		prefixed(m, k)
	}
	press(key(tea.KeyUp, 0, ""))
	if tab.Focused() != p2 || m.focusPalette {
		t.Fatal("↑ focuses the pane above and closes the palette")
	}
	press(key(tea.KeyLeft, 0, ""))
	if tab.Focused() != p1 {
		t.Fatal("← focuses the left pane")
	}
	press(key(tea.KeyRight, 0, ""))
	press(key(tea.KeyDown, 0, ""))
	if tab.Focused() != p3 {
		t.Fatal("→ then ↓ reach the bottom-right pane")
	}
	before := p3.Rect()
	press(key(tea.KeyLeft, tea.ModCtrl, ""))
	if p3.Rect().X != before.X-1 || p3.Session.LocalGeometry().Cols != p3.Rect().W {
		t.Fatalf("Ctrl+← moves the border one cell and resizes the session: %+v → %+v", before, p3.Rect())
	}
	m.endResize()
	press(key('=', 0, "="))
	if p3.Rect() != before {
		t.Fatalf("= equalizes: %+v", p3.Rect())
	}
	press(key('z', 0, "z"))
	if tab.Layout.Zoomed() != p3 || p3.Session.LocalGeometry().Cols != m.width {
		t.Fatal("z zooms the focused pane to the tab")
	}
	press(key('z', 0, "z"))
	// Without the prefix the palette keeps every key: ↑ is history, ← moves
	// the line cursor, | and a leading - are text.
	m.focusPalette = true
	_, _ = m.Update(key(tea.KeyUp, 0, ""))
	for _, k := range []tea.KeyPressMsg{key('-', 0, "-"), key('b', 0, "b"), key('o', 0, "o"), key(tea.KeyLeft, 0, ""), key('|', tea.ModShift, "|"), key('z', 0, "z"), key('x', 0, "x")} {
		_, _ = m.Update(k)
	}
	if line, _ := m.pal.Line(); line != "-b|zxo" || tab.Focused() != p3 || tab.Layout.Empty() != nil || tab.Layout.Zoomed() != nil {
		t.Fatalf("typed line %q, focus %q", line, tab.Focused().Command)
	}
	m.pal.Submit()
	// x detaches the focused pane; its sibling takes the space.
	press(key('x', 0, "x"))
	if m.confirm == nil || m.focusPalette || m.confirm.returnPalette || m.activeOverlay() == nil {
		t.Fatal("x on a running command shows the popup in the pane overlay; focus stays the pane's")
	}
	_, _ = m.Update(key('y', 0, "y"))
	// The bar takes the last row.
	if len(tab.Layout.Panes()) != 2 || tab.Focused() == p3 || p2.Rect().H != m.viewRows()-1 || m.focusPalette {
		t.Fatalf("x closes the focused pane: %d panes", len(tab.Layout.Panes()))
	}
	// A split that cannot fit is refused in the menu footer.
	small := newTestModel(t, 40, 10)
	small.tabs.Add(tabs.New(&panes.Pane{Session: sleep(), Command: "small"}))
	small.layoutTab(small.tabs.Current())
	small.focusPalette = true
	prefixed(small, key('|', tea.ModShift, "|"))
	if len(small.tabs.Current().Layout.Panes()) != 1 || !strings.Contains(shownNotice(small), "not enough room") || small.status != "" {
		t.Fatalf("a split below the minimum is refused: %q", shownNotice(small))
	}
}

// Focus reports go to the focused pane only: moving focus sends focus-out
// to the pane left and focus-in to the pane entered.
func TestFocusReportsFollowTheFocusedPane(t *testing.T) {
	m := newTestModel(t, 80, 10)
	_, left := mouseCapture(t, m, "\x1b[?1004h")
	_, right := paneCapture(t, m, "\x1b[?1004h", true)
	m.focusPalette = false
	m.syncFocus() // the new right pane is focused
	m.focusPalette = true
	prefixed(m, key(tea.KeyLeft, 0, "")) // ← behind the prefix
	if gotL, gotR := left(), right(); gotL != "\x1b[O\x1b[I" || gotR != "\x1b[I\x1b[O" {
		t.Fatalf("left %q, right %q", gotL, gotR)
	}
}

// The wheel scrolls the pane under the pointer, even an unfocused one; the
// focused pane keeps its live view.
func TestWheelScrollsThePaneUnderThePointer(t *testing.T) {
	m := newTestModel(t, 80, 10)
	left := paneSession(t, m.viewRows(), m.width, "sh", "-c", `seq 1 100; sleep 30`)
	right := paneSession(t, m.viewRows(), m.width, "sh", "-c", `seq 1 100; sleep 30`)
	tab := splitTab(t, m, left, right)
	waitFor(t, func() bool {
		return strings.Contains(left.GridText(), "100") && strings.Contains(right.GridText(), "100")
	})
	lp := tab.Layout.Panes()[0]
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: lp.Rect().X + 2, Y: m.tabTop() + 1})
	if left.Frame().Offset != 1 || right.Frame().Offset != 0 || tab.Focused() == lp {
		t.Fatalf("offsets left %d right %d; focus must not move", left.Frame().Offset, right.Frame().Offset)
	}
}

// R3, the empty pane's transitions: prefix | opens it right of the focused
// pane, focused, with the palette open for it (the Chooser); a second split
// is refused; it stays through a tab switch and back and through an
// ordinary command; a request from another tab's pane opens a new tab;
// reuse wins and says where; a request from a pane of its own tab fills it;
// Esc in the Chooser removes it, Esc in a Draft only hides the palette; a
// failed start keeps it; a live palette command fills it; x removes it.
func TestEmptyPaneTransitions(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return "/bin/true", nil } // never the test binary
	m := newTestModel(t, 100, 26)
	view := paneSession(t, 24, 100, "sleep", "30")
	other := paneSession(t, 24, 100, "sleep", "30")
	m.tabs.Add(testTab(view, "yoloctl space view", false))
	m.layoutTab(m.tabs.Current())
	m.tabs.Add(testTab(other, "yoloctl space view --space other", false))
	m.layoutTab(m.tabs.Current())
	m.tabs.Select(0)
	tab := m.tabs.Current()
	ps := func() []*panes.Pane { return tab.Layout.Panes() }
	open := func(text string) *panes.Pane {
		t.Helper()
		m.focusPalette = false
		prefixed(m, key(rune(text[0]), tea.ModShift, text))
		e := tab.Layout.Empty()
		if e == nil || tab.Focused() != e || !m.focusPalette || !m.inChooser() {
			t.Fatalf("%s opens a focused empty pane with the palette for it (status %q)", text, m.status)
		}
		return e
	}
	typeLine := func(line string) {
		t.Helper()
		for _, r := range line {
			_, _ = m.Update(key(r, 0, string(r)))
		}
		_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	}
	e := open("|")
	if r0, r1 := ps()[0].Rect(), e.Rect(); r1.X != r0.X+r0.W+1 || r1.Y != r0.Y {
		t.Fatalf("| opens it to the right: %+v %+v", r0, r1)
	}
	prefixed(m, key('-', 0, "-"))
	if len(ps()) != 2 || shownNotice(m) != "fill or close the empty pane first" {
		t.Fatalf("one empty pane per tab: %d panes, %q", len(ps()), shownNotice(m))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, "")) // the refusal kept the menu open
	_, _ = m.Update(key(tea.KeyRight, tea.ModCtrl, ""))
	if m.tabs.Active != 1 {
		t.Fatal("setup: Ctrl+→ in the palette selects the next tab")
	}
	_, _ = m.Update(key(tea.KeyLeft, tea.ModCtrl, ""))
	if tab.Layout.Empty() != e || tab.Focused() != e || !m.focusPalette || m.tabs.Active != 0 {
		t.Fatal("the empty pane stays through a tab switch and back, focused, with its palette")
	}
	executable = func() (string, error) { return "/bin/echo", nil }
	_ = m.dispatch([]string{"space", "ls"})
	waitFor(t, func() bool { return !m.console.Busy() })
	if tab.Layout.Empty() != e || len(m.tabs.Tabs) != 2 {
		t.Fatal("an ordinary command runs in the overlay and keeps the empty pane")
	}
	executable = func() (string, error) { return "/bin/true", nil }
	request := termio.TabRequest{Op: "attach", Space: "dev", Box: "api"}
	_, _ = m.Update(tabRequestMsg{req: request, from: other})
	if len(m.tabs.Tabs) != 3 || tab.Layout.Empty() != e {
		t.Fatal("a request from another tab's pane opens a new tab and leaves the empty pane")
	}
	_, _ = m.Update(tabRequestMsg{req: request, from: view})
	if m.tabs.Active != 2 || m.status != "already open in tab 3" || tab.Layout.Empty() != e {
		t.Fatalf("reuse wins over the empty pane and says where: active %d, %q", m.tabs.Active, m.status)
	}
	m.status = ""
	m.tabs.CloseCurrent()
	m.tabs.Select(0)
	slot := e.Rect()
	_, _ = m.Update(tabRequestMsg{req: request, from: view})
	filled := ps()[1]
	if tab.Layout.Empty() != nil || filled.Command != "yoloctl box attach api --space dev" || filled.Rect() != slot ||
		tab.Focused() != filled || m.focusPalette || m.tabs.Active != 0 || len(m.tabs.Tabs) != 2 {
		t.Fatalf("a request from a pane of its tab fills the empty pane in place: %q %+v", filled.Command, filled.Rect())
	}
	tab.Layout.SetFocus(ps()[0])
	open("-")
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if tab.Layout.Empty() != nil || len(ps()) != 2 || m.focusPalette || tab.Focused() != ps()[0] {
		t.Fatal("Esc in the Chooser removes the empty pane; its sibling takes the space")
	}
	e = open("-")
	_, _ = m.Update(key('b', 0, "b"))
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if tab.Layout.Empty() != e || m.focusPalette || m.hostCursor() != nil {
		t.Fatal("Esc in a Draft hides the palette and keeps the empty pane, which shows no cursor")
	}
	_, _ = m.Update(key('o', 0, "o"))
	if line, _ := m.pal.Line(); line != "bo" || !m.focusPalette {
		t.Fatalf("a key on the empty pane opens its palette again with the draft: %q", line)
	}
	_, _ = m.Update(key(tea.KeyBackspace, 0, ""))
	_, _ = m.Update(key(tea.KeyBackspace, 0, ""))
	executable = func() (string, error) { return "", errors.New("gone") }
	typeLine("box attach web")
	if tab.Layout.Empty() != e || !strings.Contains(m.status, "cannot locate") {
		t.Fatalf("a failed start keeps the empty pane: %q", m.status)
	}
	executable = func() (string, error) { return "/bin/true", nil }
	m.focusPalette = true
	typeLine("box attach web")
	if tab.Layout.Empty() != nil || tab.Focused().Command != "yoloctl box attach web" || m.focusPalette {
		t.Fatalf("a live palette command fills the empty pane: %q", tab.Focused().Command)
	}
	open("|")
	prefixed(m, key('x', 0, "x"))
	if tab.Layout.Empty() != nil || m.confirm != nil || len(ps()) != 3 {
		t.Fatal("x removes an empty pane at once")
	}
}

// Attention: a bell or an exit in a pane other than the focused one marks
// that pane (shown on its border, cleared by focusing it); the current tab
// is not badged, as before panes; another tab is.
func TestBellAndExitMarkAnUnfocusedPane(t *testing.T) {
	m := newTestModel(t, 100, 26)
	left := paneSession(t, 24, 100, "sleep", "30")
	right := paneSession(t, 24, 100, "sleep", "30")
	tab := splitTab(t, m, left, right)
	lp, rp := tab.Layout.Panes()[0], tab.Layout.Panes()[1]
	_, _ = m.Update(paneBellMsg{from: left})
	if !lp.Mark || rp.Mark || tab.Badge {
		t.Fatalf("bell: left mark %v, right mark %v, tab badge %v", lp.Mark, rp.Mark, tab.Badge)
	}
	if !strings.Contains(m.View().Content, "●") {
		t.Fatal("the mark is drawn on the border")
	}
	_, _ = m.Update(paneBellMsg{from: right}) // the focused pane: nothing
	if rp.Mark {
		t.Fatal("the focused pane is never marked")
	}
	m.focusPalette = true
	prefixed(m, key(tea.KeyLeft, 0, ""))
	if tab.Focused() != lp || lp.Mark || strings.Contains(m.View().Content, "●") {
		t.Fatal("focusing the pane clears its mark")
	}
	_, _ = m.Update(tabExitMsg{session: right})
	if !rp.Exited || !rp.Mark || tab.Badge {
		t.Fatal("an unfocused pane's exit marks it without badging the current tab")
	}
	other := testTab(paneSession(t, 24, 100, "sleep", "30"), "other", false)
	m.tabs.Add(other)
	_, _ = m.Update(paneBellMsg{from: left})
	if !tab.Badge || !lp.Mark {
		t.Fatal("a bell in a background tab badges it and marks the pane")
	}
}

// A pixel-mouse child in an unfocused pane, while the host reports cells:
// the press that focuses it (D2) arrives as the cell's centre in measured
// pixels, relative to the pane.
func TestPixelChildInAnUnfocusedPaneGetsTheCellCentre(t *testing.T) {
	m := newTestModel(t, 80, 10)
	_, _ = mouseCapture(t, m, dragSGR)
	right, rightInput := paneCapture(t, m, "\x1b[?1002h\x1b[?1016h", true)
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	tab := m.tabs.Current()
	tab.Layout.SetFocus(tab.Layout.Panes()[0])
	m.focusPalette = false
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(click(r.X+3, top+2))
	_, _ = m.Update(releaseAt(r.X+3, top+2))
	// Cell (3,2) of the pane: centre (3*10+5, 2*20+10) = (35, 50).
	if got, want := rightInput(), "\x1b[<0;35;50M\x1b[<0;35;50m"; got != want {
		t.Fatalf("pixel child got %q, want %q", got, want)
	}
}
