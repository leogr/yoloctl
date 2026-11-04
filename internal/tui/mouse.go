package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

type mouseSurface interface {
	Frame() terminal.Frame
	MouseMode() terminal.MouseMode
	SendMouse(terminal.Mouse) error
	SendInput([]byte) error
	IsAlt() bool
	AlternateScroll() bool
	ApplicationCursorKeys() bool
	SelectMouse(terminal.MouseAction, int, int, bool, uint64) error
	ClearSelection()
	Scroll(int) int
}

// Each button retains its press owner through focus, tab and size changes.
// A nil surface owns a consumed gesture, including its motion and release.
// A pane owner's origin (row0, col0) is recomputed from its pane's current
// rectangle at every delivery, so a gesture keeps its pane across focus,
// palette and size changes.
type mouseOwner struct {
	surface      mouseSurface
	pane         *panes.Pane
	row0, col0   int
	selection    bool
	captureShift bool
}

// tabTop is the screen row of the tab area's first row: below the tab
// strip, which an open palette pushes down.
func (m *model) tabTop() int {
	if m.focusPalette && !m.standalone {
		return m.tabStripRow() + 1
	}
	return m.chromeRows()
}

// paneAt is the current tab's pane under a screen cell, nil on a border or
// outside the tab area.
func (m *model) paneAt(mo tea.Mouse) *panes.Pane {
	t := m.tabs.Current()
	if t == nil || mo.Y < m.tabTop() {
		return nil
	}
	p, _, _ := m.laidOut(t).At(mo.X, mo.Y-m.tabTop())
	return p
}

// paneOwner is a gesture owner for a pane, at its current origin.
func (m *model) paneOwner(p *panes.Pane, selection bool) mouseOwner {
	r := p.Rect()
	return mouseOwner{surface: p.Session, pane: p, row0: m.tabTop() + r.Y, col0: r.X, selection: selection}
}

// focusPaneAt focuses the pane under a press (D2: the press is then
// delivered to it as usual). It reports whether focus moved.
func (m *model) focusPaneAt(mo tea.Mouse) bool {
	p := m.paneAt(m.mouseHit(mo))
	t := m.tabs.Current()
	if p == nil || p == t.Focused() {
		return false
	}
	return t.Layout.SetFocus(p)
}

// dropPaneOwners ends every gesture owned by a closed pane: its release is
// never retargeted, and its local selection is gone.
func (m *model) dropPaneOwners(p *panes.Pane) {
	for button, owner := range m.mouseOwners {
		if owner.pane == p {
			m.mouseOwners[button] = mouseOwner{}
			if owner.selection {
				m.selectionPointer = nil
			}
		}
	}
}

func (m *model) consoleMouseOrigin(f terminal.Frame) int {
	return 1 + f.GridStart - max(len(f.Lines)-m.overlayContentRows(), 0)
}

func (m *model) mouseTarget(mo tea.Mouse) mouseOwner {
	if m.browserDialog != nil || m.confirm != nil || m.picker != nil || m.activeOverlay() != nil {
		return mouseOwner{}
	}
	mo = m.mouseHit(mo)
	if mo.X < 0 || mo.X >= m.width || mo.Y < 0 || mo.Y >= m.height {
		return mouseOwner{}
	}
	if m.focusPalette {
		if m.console.Busy() && mo.Y >= 1 && mo.Y < m.tabStripRow() {
			f := m.console.Frame()
			row0 := m.consoleMouseOrigin(f)
			if f.Offset == 0 && mo.Y >= row0 && mo.Y < row0+f.Rows {
				return mouseOwner{surface: m.console, row0: row0}
			}
		}
	} else if p := m.paneAt(mo); p != nil && !p.Exited && !p.Empty() && p.Session.Frame().Offset == 0 {
		return m.paneOwner(p, false)
	}
	return mouseOwner{}
}

func mouseEvent(mo tea.Mouse, row0, col0 int, action terminal.MouseAction) (terminal.Mouse, bool) {
	buttons := map[tea.MouseButton]uint32{
		tea.MouseNone: 0, tea.MouseLeft: 1, tea.MouseRight: 2, tea.MouseMiddle: 3,
		tea.MouseWheelUp: 4, tea.MouseWheelDown: 5, tea.MouseWheelLeft: 6, tea.MouseWheelRight: 7,
		tea.MouseBackward: 8, tea.MouseForward: 9,
		tea.MouseButton10: 10, tea.MouseButton11: 11,
	}
	button, ok := buttons[mo.Button]
	if !ok || button == 0 && action != terminal.MouseMotion {
		return terminal.Mouse{}, false
	}
	var mods uint16
	for i, bit := range []tea.KeyMod{tea.ModShift, tea.ModCtrl, tea.ModAlt} {
		if mo.Mod&bit != 0 {
			mods |= 1 << i
		}
	}
	return terminal.Mouse{X: mo.X - col0, Y: mo.Y - row0, Button: button, Mods: mods, Action: action}, true
}

func (m *model) deliverMouse(owner mouseOwner, mo tea.Mouse, action terminal.MouseAction, pixels *terminal.MousePixels) {
	if owner.surface == nil {
		return
	}
	// A visible console can move its grid within the trimmed overlay while
	// output arrives. Retain the owner, but use its current displayed origin.
	if m.focusPalette && owner.surface == m.console {
		owner.row0 = m.consoleMouseOrigin(m.console.Frame())
	} else if owner.pane != nil {
		owner = m.paneOwner(owner.pane, owner.selection)
	}
	if owner.selection {
		m.selectMouse(owner, mo, action)
		return
	}
	if pixels == nil && owner.pane != nil && m.tabs.Current() != nil && m.tabs.Current().Layout.Divided() &&
		owner.surface.MouseMode().Format == 4 {
		// A pixel-mouse child in a pane the host is not reporting pixels for
		// (it was not focused) gets the cell's centre in measured pixels;
		// with no measured cell size the event is not deliverable.
		cw, ch := m.hostGeometry.width, m.hostGeometry.height
		if cw <= 0 || ch <= 0 {
			return
		}
		pixels = &terminal.MousePixels{X: mo.X*cw + cw/2, Y: mo.Y*ch + ch/2, CellWidth: cw, CellHeight: ch}
	}
	if event, ok := mouseEvent(mo, owner.row0, owner.col0, action); ok {
		if pixels != nil {
			// Pane origins are whole measured cells: subtract them exactly.
			point := *pixels
			point.X -= owner.col0 * point.CellWidth
			point.Y -= owner.row0 * point.CellHeight
			event.Pixels = &point
		}
		if err := owner.surface.SendMouse(event); err != nil {
			m.status = err.Error()
		}
	}
}

func (m *model) handleMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.browserDialog != nil {
		switch msg.(type) {
		case tea.MouseClickMsg, tea.MouseWheelMsg:
			return m, nil
		}
		// Finish gestures which began before the dialog opened at their
		// original owners; new motion cannot select an obscured surface.
	}
	mo := msg.(tea.MouseMsg).Mouse()
	if m.hostMouseEncoding.failed {
		return m, nil
	}
	var pixels *terminal.MousePixels
	if m.hostMouseEncoding.pixels {
		cw, ch := m.hostGeometry.width, m.hostGeometry.height
		if cw <= 0 || ch <= 0 {
			m.status = "pixel mouse report has no measured host cell size"
			return m, nil
		}
		// UV preserves its cell-coordinate convention for every SGR report.
		// Mode1016 uses zero-origin pixels, so undo that subtraction exactly.
		pixels = &terminal.MousePixels{X: mo.X + 1, Y: mo.Y + 1, CellWidth: cw, CellHeight: ch}
		mo.X, mo.Y = floorCell(pixels.X, cw), floorCell(pixels.Y, ch)
	}
	// The pane menu and the inert notice take presses and the wheel before
	// the prefix and chrome/child routing (R2).
	if cmd, taken := m.overlayMouse(msg.(tea.MouseMsg), mo); taken {
		return m, cmd
	}
	if _, press := msg.(tea.MouseClickMsg); press {
		m.disarmPrefix() // a press ends a resize window, then takes its route
	}
	if m.picker != nil {
		switch msg.(type) {
		case tea.MouseClickMsg:
			// A press acts on a picker row or closes the list; it is
			// consumed and stays chrome-owned through its release.
			if m.mouseOwners == nil {
				m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
			}
			m.mouseOwners[mo.Button] = mouseOwner{}
			m.pickerClick(m.mouseHit(mo))
			return m, nil
		case tea.MouseWheelMsg:
			return m, nil
		}
		// Gestures begun before it opened finish at their owners.
	}
	if m.confirm != nil {
		switch msg.(type) {
		case tea.MouseClickMsg:
			// A press closes the popup, on a button or outside; it stays
			// chrome-owned through its release.
			if m.mouseOwners == nil {
				m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
			}
			m.mouseOwners[mo.Button] = mouseOwner{}
			m.confirmClick(m.mouseHit(mo))
			return m, nil
		case tea.MouseWheelMsg:
			return m, nil
		}
		// Gestures begun before the popup opened finish at their owners.
	}
	if handled := m.chromeMouse(msg, mo); handled {
		return m, nil
	}
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		if mo.Button == tea.MouseLeft || mo.Button == tea.MouseNone {
			m.selectionPointer = nil
		}
		if owner, ok := m.mouseOwners[mo.Button]; ok {
			delete(m.mouseOwners, mo.Button)
			m.deliverMouse(owner, mo, terminal.MouseRelease, pixels)
		} else if mo.Button == tea.MouseNone {
			// Legacy host reports do not identify the released button. End
			// captured gestures at their owners rather than retargeting them.
			for button := tea.MouseLeft; button <= tea.MouseButton11; button++ {
				owner, ok := m.mouseOwners[button]
				if !ok {
					continue
				}
				mo.Button = button
				m.deliverMouse(owner, mo, terminal.MouseRelease, pixels)
				delete(m.mouseOwners, button)
			}
		}
		return m, nil
	case tea.MouseMotionMsg:
		if mo.Button != tea.MouseNone {
			// No observed press means no application-owned drag.
			owner := m.mouseOwners[mo.Button]
			m.deliverMouse(owner, mo, terminal.MouseMotion, pixels)
			if owner.selection {
				m.selectionPointer = &mo
				return m, m.selectionScrollCmd()
			}
		} else if owner := m.mouseTarget(mo); owner.pane == nil || owner.pane == m.tabs.Focused() {
			// Hover reaches only the focused pane, whose tracking the host uses.
			m.deliverMouse(owner, mo, terminal.MouseMotion, pixels)
		}
		return m, nil
	case tea.MouseClickMsg:
		if mo.Button == tea.MouseLeft {
			m.selectionPointer = nil
		}
		if !m.focusPalette {
			// D2: a press in an unfocused pane focuses it, then is delivered.
			m.focusPaneAt(mo)
		}
		owner := m.mouseTarget(mo)
		if mo.Button == tea.MouseLeft {
			if local := m.selectionTarget(mo); local.surface != nil {
				owner = local
			}
		}
		if owner.surface == nil || !owner.selection && owner.surface.MouseMode().Tracking == 0 {
			owner = mouseOwner{}
			if mo.Button == tea.MouseLeft {
				m.handleChromeClick(mo)
			}
		}
		if owner.surface != nil && !owner.selection {
			owner.surface.ClearSelection()
			// Keep a captured gesture reportable if focus changes while Shift
			// is held. A palette/tab change must not turn its release into a
			// host selection event and leave the original application stuck.
			owner.captureShift = mo.Mod&tea.ModShift != 0 || owner.surface.MouseMode().ShiftCapture == terminal.MouseShiftReport
		}
		if m.mouseOwners == nil {
			m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
		}
		m.mouseOwners[mo.Button] = owner
		m.deliverMouse(owner, mo, terminal.MousePress, pixels)
		return m, nil
	case tea.MouseWheelMsg:
		owner := m.mouseTarget(mo)
		if owner.surface != nil {
			if owner.surface.MouseMode().Tracking != 0 {
				m.deliverMouse(owner, mo, terminal.MousePress, pixels)
				return m, nil
			}
			if owner.surface.IsAlt() && owner.surface.AlternateScroll() {
				if seq := alternateScrollKey(mo.Button, owner.surface.ApplicationCursorKeys()); seq != nil {
					m.sendInput(owner.surface, seq)
					return m, nil
				}
			}
		}
		// One row per wheel event preserves high-resolution host scrolling.
		// The wheel scrolls the pane under the pointer, focused or not.
		delta := map[tea.MouseButton]int{tea.MouseWheelUp: 1, tea.MouseWheelDown: -1}[mo.Button]
		if p := m.paneAt(m.mouseHit(mo)); !m.focusPalette && p != nil && p != m.tabs.Focused() {
			if delta != 0 && !p.Empty() {
				p.Session.Scroll(delta)
			}
		} else if delta != 0 {
			m.scrollBy(delta)
		}
		if owner := m.mouseOwners[tea.MouseLeft]; owner.selection {
			m.deliverMouse(owner, mo, terminal.MouseMotion, pixels)
		}
		return m, nil
	}
	return m, nil
}

// chromeMouse routes the mouse to the chrome: a left press on a strip
// label or ✕, a bar or a border starts a chrome gesture, which then owns
// its motion and release; while it is held, hover is suppressed, the wheel
// is consumed, and another button's press cancels it and is consumed.
func (m *model) chromeMouse(msg tea.Msg, mo tea.Mouse) bool {
	if m.standalone {
		return false
	}
	if m.chrome == nil {
		click, ok := msg.(tea.MouseClickMsg)
		if !ok || click.Button != tea.MouseLeft {
			return false
		}
		g, ok := m.chromeAt(m.mouseHit(mo))
		if !ok {
			return false
		}
		m.selectionPointer = nil
		if m.mouseOwners == nil {
			m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
		}
		m.mouseOwners[tea.MouseLeft] = mouseOwner{} // consumed through its release
		m.startChrome(g)
		return true
	}
	// Its objects may have changed since the last event: an exit, a close
	// or a tab switch cancels it before the event acts on stale objects.
	if m.checkChrome(); m.chrome == nil {
		// The left button's consumed owner swallows the rest of the cancelled
		// gesture; every other event takes its normal route.
		return false
	}
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.cancelChrome()
		if m.mouseOwners == nil {
			m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
		}
		m.mouseOwners[mo.Button] = mouseOwner{}
	case tea.MouseMotionMsg:
		switch mo.Button {
		case tea.MouseLeft:
			m.chromeMotion(m.mouseHit(mo))
		case tea.MouseNone: // hover is suppressed while a chrome gesture is held
		default:
			return false // another button's gesture keeps its motion at its owner
		}
	case tea.MouseReleaseMsg:
		if mo.Button != tea.MouseLeft && mo.Button != tea.MouseNone {
			return false // another button's gesture ends at its own owner
		}
		delete(m.mouseOwners, tea.MouseLeft)
		m.chromeRelease(m.mouseHit(mo))
		// A legacy release names no button: the chrome's left gesture ends
		// here once, and the normal route then ends every other captured
		// gesture at its own owner.
		return mo.Button == tea.MouseLeft
	}
	return true
}

func floorCell(pixel, size int) int {
	cell := pixel / size
	if pixel < 0 && pixel%size != 0 {
		cell--
	}
	return cell
}

// Host padding can yield negative terminal-space pixels even for an admitted
// press. Hit testing chooses the edge cell like a native terminal; forwarding
// and drag/selection coordinates retain the original signed position.
func (m *model) mouseHit(mo tea.Mouse) tea.Mouse {
	if m.hostMouseEncoding.pixels {
		mo.X = min(max(mo.X, 0), max(m.width-1, 0))
		mo.Y = min(max(mo.Y, 0), max(m.height-1, 0))
	}
	return mo
}
