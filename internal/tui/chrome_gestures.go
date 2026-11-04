package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

// chromeKind is what a left press on yoloctl's own chrome started.
type chromeKind int

const (
	chromeInert         chromeKind = iota // a bar's line: consumed, does nothing
	chromeTabLabel                        // a tab label: activates on release, or docks once dragged
	chromeTabClose                        // a tab's ✕: detaches the tab on release
	chromeBarTitle                        // a bar's title or marker: focuses on press
	chromeBarZoom                         // ⤢: toggles zoom on release
	chromeBarClose                        // a bar's ✕: detaches the pane on release
	chromeResize                          // a border, junction or separator bar
	chromeBarSplitRight                   // │: an empty pane opens right, on release
	chromeBarSplitBelow                   // ─: an empty pane opens below, on release
	chromeDock                            // a tab label dragged: drops the tab on a pane
)

// dockStatus is the status line while a dock drag has no refused target.
const dockStatus = "drop on a pane: left · right · top · bottom · Esc cancels"

// chromeGesture is a left-button gesture owned by the chrome: a consumed
// owner (no child receives its press, motion or release) that captures its
// objects at the press, never a cell or an index. At the release the object
// is looked up again and the release must be over its current hitbox.
type chromeGesture struct {
	kind     chromeKind
	tab      *tabs.Tab   // the strip tab, or the tab holding the pane/split
	current  *tabs.Tab   // the tab shown at the press
	pane     *panes.Pane // a bar's pane
	split    panes.Split // a resize's split
	vertical bool        // a resize's axis
	grab     int         // pointer minus border along the axis, kept while dragging
	gen      int         // the layout generation at the press, for bar and border gestures
	x, y     int         // the press cell, for a label's first motion
	status   string      // the status line a dock drag set, cleared with it
}

// chromeAt is the chrome gesture a left press at a screen cell starts, if
// the cell is chrome: a strip label or ✕, a bar part, a vertical border or
// junction. The input line and pane content are not.
func (m *model) chromeAt(mo tea.Mouse) (chromeGesture, bool) {
	cur := m.tabs.Current()
	g := chromeGesture{current: cur, x: mo.X, y: mo.Y}
	if mo.Y == m.tabStripRow() {
		if i := m.tabCloseAt(mo.X); i >= 0 {
			g.kind, g.tab = chromeTabClose, m.tabs.Tabs[i]
		} else if i := m.tabAt(mo.X); i >= 0 {
			g.kind, g.tab = chromeTabLabel, m.tabs.Tabs[i]
		} else {
			return g, false
		}
		g.gen = g.tab.Layout.Generation()
		return g, true
	}
	top := m.tabTop()
	if cur == nil || mo.Y < top {
		return g, false
	}
	l := m.laidOut(cur)
	x, y := mo.X, mo.Y-top
	g.tab, g.gen = cur, l.Generation()
	if p, part := l.BarAt(x, y); p != nil {
		g.pane = p
		if s, ok := l.SplitBelow(p); ok && (part == panes.BarNone || part == panes.BarTitle) {
			// A bar between two stacked panes is their split's handle along
			// its whole width, buttons aside: the drag starts at the press.
			g.kind, g.split, g.vertical = chromeResize, s, false
			g.grab = y - l.BorderPos(s)
			return g, true
		}
		g.kind = map[panes.BarPart]chromeKind{
			panes.BarNone: chromeInert, panes.BarTitle: chromeBarTitle,
			panes.BarZoom: chromeBarZoom, panes.BarClose: chromeBarClose,
			panes.BarSplitRight: chromeBarSplitRight, panes.BarSplitBelow: chromeBarSplitBelow,
		}[part]
		return g, true
	}
	if s, ok := l.BorderAt(x, y); ok {
		g.kind, g.split, g.vertical = chromeResize, s, true
		g.grab = x - l.BorderPos(s)
		return g, true
	}
	return g, false
}

// startChrome takes a left press as a chrome gesture. A press on a bar's
// title, or anywhere on a separator bar, focuses its pane at once (D2:
// nothing is delivered).
func (m *model) startChrome(g chromeGesture) {
	if g.kind == chromeBarTitle || g.kind == chromeResize && g.pane != nil {
		if m.focusPalette {
			m.focusView()
		}
		g.tab.Layout.SetFocus(g.pane)
	}
	m.chrome = &g
}

// chromeMotion follows a held chrome gesture: a resize previews its border;
// a tab label moved by a cell becomes a dock drag, which shows its target.
func (m *model) chromeMotion(mo tea.Mouse) {
	g := m.chrome
	if g.kind == chromeTabLabel && (mo.X != g.x || mo.Y != g.y) {
		g.kind = chromeDock
	}
	if g.kind == chromeDock {
		m.dockMotion(mo)
		return
	}
	l := m.laidOut(g.tab)
	if g.kind != chromeResize {
		return
	}
	pos := mo.Y - m.tabTop()
	if g.vertical {
		pos = mo.X
	}
	l.Preview(g.split, pos-g.grab)
}

// chromeRelease ends the gesture: a button acts only when the release is
// over the same object's current button or label; a resize commits once.
func (m *model) chromeRelease(mo tea.Mouse) {
	g := *m.chrome
	m.chrome = nil
	if g.kind == chromeDock {
		m.dockRelease(g, mo)
		return
	}
	if g.kind == chromeResize {
		if g.tab.Layout.CommitPreview() {
			m.layoutTab(g.tab) // one PTY resize per pane, on release
		}
		return
	}
	at, ok := m.chromeAt(mo)
	if !ok || at.kind != g.kind || at.tab != g.tab || at.pane != g.pane {
		return // released elsewhere: cancelled
	}
	switch g.kind {
	case chromeTabLabel:
		m.activateTab(m.tabs.Index(g.tab))
	case chromeTabClose:
		m.requestDetachTab(g.tab)
	case chromeBarZoom:
		g.tab.Layout.SetFocus(g.pane)
		g.tab.Layout.ToggleZoom()
		m.layoutTab(g.tab)
	case chromeBarClose:
		m.requestDetachPane(g.tab, g.pane)
	case chromeBarSplitRight, chromeBarSplitBelow:
		g.tab.Layout.SetFocus(g.pane)
		m.openPlaceholder(g.tab, g.kind == chromeBarSplitRight)
	}
}

// dockTarget is the drop target under a screen cell in the visible tab:
// a pane and side, and why the drop would be refused ("" when it would be
// taken); ok is false where there is no target (strip, palette, a border,
// a bar).
func (m *model) dockTarget(g *chromeGesture, mo tea.Mouse) (p *panes.Pane, side panes.Dir, why string, ok bool) {
	cur := m.tabs.Current()
	top := m.tabTop()
	if cur == nil || cur == g.tab || mo.Y < top {
		return nil, 0, "", false
	}
	p, side, ok = m.laidOut(cur).DropAt(mo.X, mo.Y-top)
	if !ok {
		return nil, 0, "", false
	}
	return p, side, m.canJoin(g.tab, cur, p, side), true
}

// dockMotion shows the dock drag's target: tinted when the drop would be
// taken, hatched with the reason on the status line when it would not. A
// drag of the active tab has no target.
func (m *model) dockMotion(mo tea.Mouse) {
	g := m.chrome
	text := dockStatus
	cur := m.tabs.Current()
	if g.current != nil {
		g.current.Layout.ClearDrop()
	}
	if g.tab == cur {
		text = "drop needs another tab"
	} else if p, side, why, ok := m.dockTarget(g, mo); ok {
		cur.Layout.SetDrop(panes.Drop{Pane: p, Side: side, OK: why == ""})
		if why != "" {
			text = why
		}
	}
	if g.status == "" || m.status == g.status {
		m.status = text
	}
	g.status = text
}

// dockRelease drops the dragged tab: over a target that still passes the
// preflight, its whole layout joins the visible tab on that side (or fills
// its empty pane), and the tab leaves the strip; anywhere else nothing
// changes.
func (m *model) dockRelease(g chromeGesture, mo tea.Mouse) {
	m.endDock(&g)
	p, side, why, ok := m.dockTarget(&g, mo)
	if !ok {
		return
	}
	if why == "" {
		why = m.joinTab(g.tab, m.tabs.Current(), p, side)
	}
	if why != "" {
		m.status = why
	}
}

// endDock removes a dock drag's target, drawn on the tab shown at its
// press, and its status line.
func (m *model) endDock(g *chromeGesture) {
	if g.current != nil {
		g.current.Layout.ClearDrop()
	}
	if g.status != "" && m.status == g.status {
		m.status = ""
	}
}

// cancelChrome drops a held chrome gesture; a dragged border returns to
// its committed position. The button's consumed owner stays until its
// release, which is then swallowed.
func (m *model) cancelChrome() {
	if g := m.chrome; g != nil && g.kind == chromeResize && g.tab != nil {
		g.tab.Layout.CancelPreview()
	}
	if g := m.chrome; g != nil && g.kind == chromeDock {
		m.endDock(g)
	}
	m.chrome = nil
}

// checkChrome cancels a held gesture whose objects changed under it: a tab
// switch, its tab or pane gone, or its split no longer in the layout.
func (m *model) checkChrome() {
	g := m.chrome
	if g == nil {
		return
	}
	switch {
	case m.tabs.Current() != g.current, m.tabs.Index(g.tab) < 0:
	case g.pane != nil && !hasPane(g.tab, g.pane):
	case g.kind == chromeResize && !g.tab.Layout.Valid(g.split, g.vertical):
	case g.tab.Layout.Generation() != g.gen:
		// A layout change of the gesture's tab (equalize, zoom, a split or
		// close, a keyboard resize) cancels it; a retitle, a re-windowed
		// strip or a host resize does not.
	default:
		return
	}
	m.cancelChrome()
}

func hasPane(t *tabs.Tab, p *panes.Pane) bool {
	for _, q := range t.Layout.Panes() {
		if q == p {
			return true
		}
	}
	return false
}

// detachPane closes one pane's view (a detach for a managed view) and
// gives its space to the others; the last pane removes its tab.
func (m *model) detachPane(t *tabs.Tab, p *panes.Pane) {
	m.dropPaneOwners(p)
	m.tabs.ClosePane(t, p)
	m.afterDetach(t)
}

// detachTab closes every pane's view of a tab and removes it, without
// activating it first.
func (m *model) detachTab(t *tabs.Tab) {
	for _, p := range t.Layout.Panes() {
		m.dropPaneOwners(p)
	}
	m.tabs.CloseTab(t)
	m.afterDetach(t)
}

func (m *model) afterDetach(t *tabs.Tab) {
	if m.tabs.Index(t) >= 0 {
		m.layoutTab(t)
	}
	if cur := m.tabs.Current(); cur != nil {
		m.layoutTab(cur)
	} else if m.confirm == nil {
		m.focusPalette = true // no tabs left: input goes to the palette
	}
}
