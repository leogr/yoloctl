package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

// barCell is the screen cell of a bar part of p.
func barCell(t *testing.T, m *model, tab *tabs.Tab, p *panes.Pane, part panes.BarPart) (int, int) {
	t.Helper()
	m.laidOut(tab)
	bar := p.Bar()
	for x := bar.X; x < bar.X+bar.W; x++ {
		if q, got := tab.Layout.BarAt(x, bar.Y); q == p && got == part {
			return x, m.tabTop() + bar.Y
		}
	}
	t.Fatalf("no bar part %v on %q", part, p.Command)
	return 0, 0
}

func motionAt(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{Button: tea.MouseLeft, X: x, Y: y}
}

func twoPanes(t *testing.T, m *model) (*tabs.Tab, *panes.Pane, *panes.Pane) {
	t.Helper()
	tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"), paneSession(t, 20, 80, "sleep", "30"))
	m.focusPalette = false
	ps := tab.Layout.Panes()
	return tab, ps[0], ps[1]
}

// R1: bar buttons act on the release over the same button; a release
// elsewhere cancels; the title focuses on press and delivers nothing.
func TestBarButtonsActOnReleaseOverTheSameButton(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, right := twoPanes(t, m)
	tab.Layout.SetFocus(right)

	x, y := barCell(t, m, tab, left, panes.BarTitle)
	_, _ = m.Update(click(x, y))
	if tab.Focused() != left || m.chrome == nil || m.mouseOwners[tea.MouseLeft].surface != nil {
		t.Fatal("a title press focuses its pane and is a consumed chrome gesture")
	}
	_, _ = m.Update(releaseAt(x, y))

	x, y = barCell(t, m, tab, right, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x-6, y)) // dragged off the ✕
	if len(tab.Layout.Panes()) != 2 {
		t.Fatal("a release off the ✕ cancels")
	}
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if m.confirm == nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("a running command's ✕ asks first")
	}
	_, _ = m.Update(key('y', 0, "y"))
	if len(tab.Layout.Panes()) != 1 || tab.Focused() != left {
		t.Fatalf("✕ detaches its pane: %d panes", len(tab.Layout.Panes()))
	}
	select {
	case <-right.Session.Done():
	default:
		t.Fatal("the detached pane's client ended")
	}
	if _, ok := m.mouseOwners[tea.MouseLeft]; ok || m.chrome != nil {
		t.Fatal("the release ends the gesture")
	}
}

func TestBarZoomButtonTogglesZoom(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, right := twoPanes(t, m)
	x, y := barCell(t, m, tab, left, panes.BarZoom)
	_, _ = m.Update(click(x, y))
	if tab.Layout.Zoomed() != nil {
		t.Fatal("zoom acts on release, not press")
	}
	_, _ = m.Update(releaseAt(x, y))
	if tab.Layout.Zoomed() != left || tab.Focused() != left || left.Session.LocalGeometry().Cols != m.width {
		t.Fatal("⤢ zooms its pane")
	}
	x, y = barCell(t, m, tab, left, panes.BarZoom)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if tab.Layout.Zoomed() != nil || right.Rect().W == 0 {
		t.Fatal("⤢ again restores the layout")
	}
}

// R6: a tab activates on release; its ✕ detaches it without activating it,
// only on a release over the same tab's ✕; a divided tab's ✕ detaches
// every pane.
func TestStripCloseAndReleaseActivation(t *testing.T) {
	m := newTestModel(t, 120, 26)
	divided, a, b := twoPanes(t, m)
	addExitedTab(t, m, "t2")
	addExitedTab(t, m, "t3")
	m.tabs.Select(2)
	row := m.tabStripRow()

	x := tabClickX(m, 1)
	_, _ = m.Update(click(x, row))
	if m.tabs.Active != 2 {
		t.Fatal("a press on a label changes nothing")
	}
	_, _ = m.Update(releaseAt(x, row))
	if m.tabs.Active != 1 {
		t.Fatal("the release activates the tab")
	}

	closeX := func(i int) int {
		segs, _, _, col := m.tabStripLayout()
		for _, s := range segs {
			if s.index == i {
				return col + s.width - 2
			}
			col += s.width
		}
		t.Fatalf("tab %d not shown", i)
		return 0
	}
	x = closeX(0)
	_, _ = m.Update(click(x, row))
	_, _ = m.Update(releaseAt(x+3, row))
	if len(m.tabs.Tabs) != 3 {
		t.Fatal("a release off the ✕ cancels")
	}
	_, _ = m.Update(click(x, row))
	_, _ = m.Update(releaseAt(x, row))
	if m.confirm == nil || len(m.confirm.listed) != 2 {
		t.Fatal("one popup lists both running commands of the tab")
	}
	_, _ = m.Update(key('y', 0, "y"))
	if len(m.tabs.Tabs) != 2 || m.tabs.Index(divided) >= 0 || m.tabs.Active != 0 {
		t.Fatalf("the inactive divided tab is detached without activation: %d tabs, active %d", len(m.tabs.Tabs), m.tabs.Active)
	}
	for _, p := range []*panes.Pane{a, b} {
		select {
		case <-p.Session.Done():
		default:
			t.Fatal("every pane of the detached tab ended its client")
		}
	}

	// Same object: the pressed tab's ✕ moves when another tab closes before
	// the release; the release over the old cells acts on nothing.
	x = closeX(1)
	_, _ = m.Update(click(x, row))
	m.tabs.ClosePane(m.tabs.Tabs[0], m.tabs.Tabs[0].Focused())
	_, _ = m.Update(releaseAt(x, row))
	if len(m.tabs.Tabs) != 1 {
		t.Fatal("a release over cells the captured ✕ left does nothing")
	}
}

// Another button pressed while a chrome gesture is held cancels it; the
// second press is consumed.
func TestSecondButtonCancelsAChromeGesture(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, _, right := twoPanes(t, m)
	x, y := barCell(t, m, tab, right, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: x, Y: y})
	if m.chrome != nil || m.mouseOwners[tea.MouseRight].surface != nil {
		t.Fatal("the second press cancels and is consumed")
	}
	_, _ = m.Update(releaseAt(x, y))
	if len(tab.Layout.Panes()) != 2 {
		t.Fatal("a cancelled ✕ does nothing on release")
	}
}

// R7 resize: a border press previews the moved border, leaving committed
// rectangles and PTY sizes alone until the release commits them once; the
// grab offset is kept; Esc, host focus loss and a tab switch cancel.
func TestResizeDragPreviewsThenCommitsOnce(t *testing.T) {
	m := newTestModel(t, 100, 26)
	_, left, right := twoPanes(t, m)
	border := left.Rect().X + left.Rect().W
	top := m.tabTop()
	cols := left.Session.LocalGeometry().Cols

	// A press on the ┴ junction of the bottom bar row, then a drag.
	_, _ = m.Update(click(border, top+left.Bar().Y))
	_, _ = m.Update(motionAt(border+10, top+3))
	if left.Rect().X+left.Rect().W != border || left.Session.LocalGeometry().Cols != cols {
		t.Fatal("the preview leaves committed geometry and PTY sizes")
	}
	_, _ = m.Update(releaseAt(border+10, top+3))
	if got := left.Rect().W; got != cols+10 || left.Session.LocalGeometry().Cols != got || right.Session.LocalGeometry().Cols != right.Rect().W {
		t.Fatalf("release commits: left %d, PTY %d", got, left.Session.LocalGeometry().Cols)
	}

	border = left.Rect().X + left.Rect().W
	for name, cancel := range map[string]tea.Msg{
		"Esc":        key(tea.KeyEscape, 0, ""),
		"focus loss": tea.BlurMsg{},
	} {
		_, _ = m.Update(click(border, top+5))
		_, _ = m.Update(motionAt(border-8, top+5))
		_, _ = m.Update(cancel)
		_, _ = m.Update(releaseAt(border-8, top+5))
		if left.Rect().X+left.Rect().W != border {
			t.Fatalf("%s cancels the drag", name)
		}
	}

	// The grab offset: a press on the border followed by a one-cell move
	// moves the border one cell, never jumping to the pointer.
	_, _ = m.Update(click(border, top+5))
	_, _ = m.Update(motionAt(border+1, top+5))
	_, _ = m.Update(releaseAt(border+1, top+5))
	if left.Rect().X+left.Rect().W != border+1 {
		t.Fatalf("one cell: %d", left.Rect().X+left.Rect().W)
	}

	// A keyboard tab switch cancels.
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	m.focusPalette = false
	border = left.Rect().X + left.Rect().W
	_, _ = m.Update(click(border, top+5))
	_, _ = m.Update(motionAt(border+6, top+5))
	m.focusPalette = true
	_, _ = m.Update(key(tea.KeyRight, tea.ModCtrl, ""))
	if m.chrome != nil {
		t.Fatal("a tab switch cancels the drag")
	}
	_, _ = m.Update(releaseAt(border+6, top+5))
	if left.Rect().X+left.Rect().W != border {
		t.Fatal("a cancelled drag commits nothing")
	}
}

// Addendum C: a bar between two stacked panes is their split's handle along
// its width. A press on its marker, title or line starts the drag at once
// (no movement threshold) and focuses the pane; a click without motion
// focuses only; ⤢ and ✕ stay buttons; a bottom-edge bar never resizes.
func TestSeparatorBarIsASplitHandle(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"))
	if err := tab.Layout.Split(false, &panes.Pane{Session: paneSession(t, 20, 80, "sleep", "30"), Command: "below"}); err != nil {
		t.Fatal(err)
	}
	m.layoutTab(tab)
	m.focusPalette = false
	upper, lower := tab.Layout.Panes()[0], tab.Layout.Panes()[1]
	bar := upper.Bar()
	y := m.tabTop() + bar.Y
	titleX, _ := barCell(t, m, tab, upper, panes.BarTitle)
	zoomX, _ := barCell(t, m, tab, upper, panes.BarZoom)
	for _, c := range []struct {
		name string
		x    int
	}{{"line at the left edge", bar.X}, {"title", titleX}, {"line before the buttons", zoomX - 2}} {
		tab.Layout.SetFocus(lower)
		gen := tab.Layout.Generation()
		_, _ = m.Update(click(c.x, y))
		if m.chrome == nil || m.chrome.kind != chromeResize || tab.Focused() != upper {
			t.Fatalf("%s: the press starts the split drag at once and focuses the pane", c.name)
		}
		_, _ = m.Update(releaseAt(c.x, y))
		if tab.Layout.Generation() != gen || upper.Rect().H != bar.Y {
			t.Fatalf("%s: a click without motion only focuses", c.name)
		}
	}
	// The first motion moves the border; no row of travel is needed first.
	_, _ = m.Update(click(bar.X+3, y))
	_, _ = m.Update(motionAt(bar.X+3, y-1))
	_, _ = m.Update(releaseAt(bar.X+3, y-1))
	if upper.Rect().H != bar.Y-1 || upper.Session.LocalGeometry().Rows != upper.Rect().H {
		t.Fatalf("a one-row drag moves the border one row: %+v", upper.Rect())
	}
	_, _ = m.Update(click(bar.X, y-1))
	_, _ = m.Update(motionAt(bar.X, y-100))
	_, _ = m.Update(releaseAt(bar.X, y-100))
	if upper.Rect().H != panes.MinRows || lower.Rect().Y != upper.Rect().H+1 {
		t.Fatalf("dragged up to the minimum: %+v %+v", upper.Rect(), lower.Rect())
	}
	if upper.Session.LocalGeometry().Rows != panes.MinRows || lower.Session.LocalGeometry().Rows != lower.Rect().H {
		t.Fatal("the commit resizes both sessions")
	}
	// ⤢ and ✕ stay buttons.
	for _, part := range []panes.BarPart{panes.BarZoom, panes.BarClose} {
		x, y := barCell(t, m, tab, upper, part)
		_, _ = m.Update(click(x, y))
		if m.chrome == nil || m.chrome.kind == chromeResize {
			t.Fatal("a press on ⤢ or ✕ is a button, not the handle")
		}
		m.cancelChrome()
		_, _ = m.Update(releaseAt(x, y))
	}
	// A bottom-edge bar separates nothing: a press there focuses only.
	before := upper.Rect()
	x, by := barCell(t, m, tab, lower, panes.BarTitle)
	tab.Layout.SetFocus(upper)
	_, _ = m.Update(click(x, by))
	_, _ = m.Update(motionAt(x, by-3))
	_, _ = m.Update(releaseAt(x, by-3))
	if tab.Focused() != lower || upper.Rect() != before {
		t.Fatal("a bottom-edge bar press focuses and does not resize")
	}
}

// A drag begun in a child's content is not chrome: it stays the child's
// (here a local selection) when it crosses a bar.
func TestChildDragCrossingABarStaysTheChilds(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, _ := twoPanes(t, m)
	top := m.tabTop()
	_, _ = m.Update(click(3, top+2))
	owner := m.mouseOwners[tea.MouseLeft]
	if m.chrome != nil || owner.pane != left {
		t.Fatal("a content press belongs to its pane")
	}
	_, _ = m.Update(motionAt(3, top+left.Bar().Y))
	_, _ = m.Update(releaseAt(3, top+left.Bar().Y))
	if m.chrome != nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("crossing a bar never turns into a chrome gesture")
	}
}

// Same object, current hitbox: when a title grows and the strip re-windows
// between press and release, a release over the captured tab's NEW ✕ acts
// and a release over its old cells does nothing.
func TestStripReleaseResolvesTheCurrentHitbox(t *testing.T) {
	m := newTestModel(t, 120, 26)
	addExitedTab(t, m, "a")
	addExitedTab(t, m, "b")
	for _, tab := range m.tabs.Tabs {
		tab.Focused().Exited = true // closes at once
	}
	row := m.tabStripRow()
	closeX := func(i int) int {
		segs, _, _, col := m.tabStripLayout()
		for _, s := range segs {
			if s.index == i {
				return col + s.width - 2
			}
			col += s.width
		}
		return -1
	}
	grow := func() { m.tabs.Tabs[0].Focused().Command += " with a longer title" }
	old := closeX(1)
	_, _ = m.Update(click(old, row))
	grow()
	_, _ = m.Update(releaseAt(old, row))
	if len(m.tabs.Tabs) != 2 {
		t.Fatal("the old cells do nothing once the ✕ moved")
	}
	old = closeX(1)
	_, _ = m.Update(click(old, row))
	grow()
	moved := closeX(1)
	if moved == old {
		t.Fatal("precondition: the ✕ moved")
	}
	_, _ = m.Update(releaseAt(moved, row))
	if len(m.tabs.Tabs) != 1 || !strings.HasPrefix(m.tabs.Tabs[0].Title(), "a ") {
		t.Fatal("a release over the captured tab's new ✕ closes it")
	}
}

// A host resize between the press and the release of a bar button: the
// release over the button's new cells acts, over its old cells nothing.
func TestBarButtonAcrossAHostResize(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, _, right := twoPanes(t, m)
	right.Exited = true // closes at once
	x, y := barCell(t, m, tab, right, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 26})
	_, _ = m.Update(releaseAt(x, y))
	if len(tab.Layout.Panes()) != 2 {
		t.Fatal("the button's old cells do nothing after a host resize")
	}
	x, y = barCell(t, m, tab, right, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 26})
	x2, y2 := barCell(t, m, tab, right, panes.BarClose)
	_, _ = m.Update(releaseAt(x2, y2))
	if len(tab.Layout.Panes()) != 1 {
		t.Fatal("a release over the button's new cells acts")
	}
}

// A host resize during a resize drag re-clamps the preview at the new size;
// cancelling returns to the committed ratio at the new size.
func TestResizeDragAcrossAHostResize(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, right := twoPanes(t, m)
	top := m.tabTop()
	border := left.Rect().X + left.Rect().W
	_, _ = m.Update(click(border, top+3))
	_, _ = m.Update(motionAt(border+28, top+3)) // near the right minimum at 100
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 26})
	row := []rune(ansi.Strip(panes.Compose(tab.Layout, nil)[0]))
	if bx := slices.Index(row, '│'); bx < 0 || bx > 60-panes.MinCols-1 {
		t.Fatalf("the preview is clamped again at the new size: border at %d in %q", bx, string(row))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	_, _ = m.Update(releaseAt(border+28, top+3))
	if left.Rect().W != 30 || right.Rect().X != 31 || left.Session.LocalGeometry().Cols != 30 {
		t.Fatalf("cancel returns to the committed half at 60 columns: %+v %+v", left.Rect(), right.Rect())
	}
}

// A release after the captured split disappeared mid-drag cancels.
func TestResizeReleaseAfterTheSplitIsGone(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, right := twoPanes(t, m)
	top := m.tabTop()
	border := left.Rect().X + left.Rect().W
	_, _ = m.Update(click(border, top+3))
	_, _ = m.Update(motionAt(border+5, top+3))
	right.Exited = true
	m.detachPane(tab, right)
	_, _ = m.Update(motionAt(border+7, top+3))
	_, _ = m.Update(releaseAt(border+7, top+3))
	if m.chrome != nil || tab.Layout.Bars() || left.Rect().W != 100 {
		t.Fatalf("the gesture is cancelled and the remaining pane fills the tab: %+v", left.Rect())
	}
}

// One bar button in pixel mode: the press and release resolve to cells
// through the measured cell size.
func TestBarButtonInPixelMode(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, _, right := twoPanes(t, m)
	right.Exited = true
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	m.hostMouseEncoding.pixels = true
	x, y := barCell(t, m, tab, right, panes.BarClose)
	px, py := x*10+4, y*20+9 // inside the cell; UV subtracts one on the wire
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: px - 1, Y: py - 1})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: px - 1, Y: py - 1})
	if len(tab.Layout.Panes()) != 1 {
		t.Fatal("a pixel press and release on ✕ detach the pane")
	}
}

// A layout change of the gesture's tab (here equalize) cancels a held drag;
// its release commits nothing.
func TestLayoutChangeCancelsADrag(t *testing.T) {
	m := newTestModel(t, 100, 26)
	tab, left, _ := twoPanes(t, m)
	top := m.tabTop()
	border := left.Rect().X + left.Rect().W
	_, _ = m.Update(click(border, top+3))
	_, _ = m.Update(motionAt(border+10, top+3))
	tab.Layout.Equalize()
	_, _ = m.Update(motionAt(border+12, top+3))
	_, _ = m.Update(releaseAt(border+12, top+3))
	if m.chrome != nil || left.Rect().X+left.Rect().W != border {
		t.Fatal("equalize mid-drag cancels the drag")
	}
}

// Another button's gesture that began before a chrome gesture keeps its
// motion and release at its owner.
func TestChromeGestureLeavesAnotherButtonsDrag(t *testing.T) {
	m, left, right, _, rightInput := twoCapturePanes(t)
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: r.X + 1, Y: top + 1})
	border := left.Rect().X + left.Rect().W
	_, _ = m.Update(click(border, top+2))
	if m.chrome == nil {
		t.Fatal("precondition: a chrome drag is held")
	}
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseRight, X: r.X + 3, Y: top + 1})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseRight, X: r.X + 3, Y: top + 1})
	_, _ = m.Update(releaseAt(border, top+2))
	if got, want := rightInput(), "\x1b[<2;2;2M\x1b[<34;4;2M\x1b[<2;4;2m"; got != want {
		t.Fatalf("the right button's drag got %q, want %q", got, want)
	}
}

// A legacy release names no button: it ends the chrome's left gesture once
// and every other captured gesture at its own owner, whether the chrome
// gesture completed or was cancelled.
func TestLegacyReleaseFinishesEveryOwner(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		m, left, right, _, rightInput := twoCapturePanes(t)
		r, top := right.Rect(), m.tabTop()
		_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: r.X + 3, Y: top + 2})
		border := left.Rect().X + left.Rect().W
		_, _ = m.Update(click(border, top+3))
		if cancel {
			_, _ = m.Update(key(tea.KeyEscape, 0, ""))
		}
		_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseNone, X: r.X + 3, Y: top + 2})
		if len(m.mouseOwners) != 0 || m.chrome != nil {
			t.Fatalf("cancel=%v: every owner ended: %v", cancel, m.mouseOwners)
		}
		if got, want := rightInput(), "\x1b[<2;4;3M\x1b[<2;4;3m"; got != want {
			t.Fatalf("cancel=%v: the right button's gesture got %q, want %q", cancel, got, want)
		}
	}
}

// A layout change of a strip gesture's tab cancels it: its ✕ and its label
// do nothing on release.
func TestStripGestureCancelsOnLayoutChange(t *testing.T) {
	m := newTestModel(t, 120, 26)
	tab, a, b := twoPanes(t, m)
	a.Exited, b.Exited = true, true // closes at once if it acted
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	m.focusPalette = true
	row := m.tabStripRow()
	segs, _, _, col := m.tabStripLayout()
	closeX := col + segs[0].width - 2
	_, _ = m.Update(click(closeX, row))
	prefixed(m, key('=', 0, "=")) // equalize: a pane key
	_, _ = m.Update(releaseAt(closeX, row))
	if m.tabs.Index(tab) < 0 {
		t.Fatal("a layout change cancels the strip ✕")
	}
	// An inactive tab's label: its layout changes before the release.
	m.tabs.Select(1)
	x := tabClickX(m, 0)
	_, _ = m.Update(click(x, m.tabStripRow()))
	m.detachPane(tab, b)
	_, _ = m.Update(releaseAt(x, m.tabStripRow()))
	if m.tabs.Active != 1 {
		t.Fatal("a layout change of the pressed tab cancels its activation")
	}
}
