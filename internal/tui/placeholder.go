package tui

import (
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

// The empty pane (R3): prefix | or -, or a bar's split buttons, split the
// focused pane at once with a pane that has no session and shows what fills
// it. A tab holds at most one. It is filled by a submitted live palette
// command, by an attach request from a pane of its own tab, or by a tab
// brought in with prefix j or a dock drag; Esc on its empty palette line,
// its ✕ or prefix x remove it, and the sibling takes the space. It stays
// through tab switches. Focusing it opens the palette, which is its input.

// openPlaceholder splits t's focused pane with an empty pane, which takes
// focus; a second one, or a split below the minimum, is refused with a
// notice in the menu (paneNotice).
func (m *model) openPlaceholder(t *tabs.Tab, vertical bool) {
	l := m.laidOut(t)
	if l.Empty() != nil {
		m.paneNotice("fill or close the empty pane first")
		return
	}
	if err := l.Split(vertical, &panes.Pane{}); err != nil {
		m.paneNotice(err.Error())
		return
	}
	m.layoutTab(t)
	m.status = ""
}

// fillPlaceholder starts args in t's empty pane e: the new pane takes its
// place and focus. When the command does not start, the empty pane stays
// and the status line says why.
func (m *model) fillPlaceholder(t *tabs.Tab, e *panes.Pane, args []string) {
	m.laidOut(t)
	r := e.Rect()
	p := m.startPane(args, r.H, r.W)
	if p == nil {
		return
	}
	t.Layout.Replace(e, p)
	m.layoutTab(t)
	m.activatePane(t, p)
	m.resetConsoleView()
	m.status = ""
	m.watchPaneExit(p)
}

// inChooser reports the empty pane's Chooser: it is focused, with the
// palette open on an empty line and no suggestion shown, so Esc removes it.
func (m *model) inChooser() bool {
	p := m.tabs.Focused()
	line, _ := m.pal.Line()
	return p != nil && p.Empty() && m.focusPalette && line == "" && len(m.sugg) == 0
}

// syncPlaceholder opens the palette when focus lands on an empty pane: its
// input is the palette. It acts once per landing, so Esc in a draft can
// still hide the palette over it.
func (m *model) syncPlaceholder() {
	p := m.tabs.Focused()
	if p == nil || !p.Empty() {
		m.placeholderFocus = nil
		return
	}
	if p == m.placeholderFocus {
		return
	}
	m.placeholderFocus = p
	switch {
	case m.confirm != nil:
		m.confirm.returnPalette = true
	case m.picker != nil:
		m.picker.returnPalette = true
	case m.browserDialog != nil:
		m.browserDialog.returnPalette = true
	default:
		m.enterPalette()
	}
}

// placeholderRows is the empty pane's hint: the actual ways to fill it.
func (m *model) placeholderRows() []string {
	rows := []string{""}
	for _, l := range []string{
		"what goes here?",
		"type a yoloctl command ↵",
		m.panePrefix().String() + " j N brings tab N here",
		"an attach from this tab's",
		"  space view or chat opens here",
		"Esc removes it",
	} {
		rows = append(rows, cDim+"    "+l+cReset)
	}
	return rows
}

// joinTab moves tab src's whole layout into dst: into dst's empty pane, or
// below target. It reports a refusal for the status line or a picker, and
// "" when the move is done.
func (m *model) joinTab(src, dst *tabs.Tab, target *panes.Pane, d panes.Dir) string {
	if m.tabs.Index(dst) >= 0 {
		m.laidOut(dst)
	}
	if err := m.tabs.Join(src, dst, target, d); err != nil {
		return m.joinRefusal(src, err)
	}
	m.layoutTab(dst)
	if m.focusPalette {
		m.leavePaletteForTab()
	}
	m.scrollToLive()
	return ""
}

// canJoin is joinTab's preflight, as a refusal or "".
func (m *model) canJoin(src, dst *tabs.Tab, target *panes.Pane, d panes.Dir) string {
	if m.tabs.Index(dst) >= 0 {
		m.laidOut(dst)
	}
	if err := m.tabs.CanJoin(src, dst, target, d); err != nil {
		return m.joinRefusal(src, err)
	}
	return ""
}

func (m *model) joinRefusal(src *tabs.Tab, err error) string {
	n := circledNumber(m.tabs.Index(src) + 1)
	switch {
	case errors.Is(err, tabs.ErrSameTab):
		return "that is this tab"
	case errors.Is(err, tabs.ErrEmptyPane):
		return fmt.Sprintf("tab %d has an empty pane", m.tabs.Index(src)+1)
	case errors.Is(err, panes.ErrTooSmall):
		return "too small for tab " + n + "'s panes"
	}
	return err.Error()
}

// joinTarget is where prefix j puts a tab in t: its empty pane when it has
// one, else below the focused pane.
func joinTarget(t *tabs.Tab) *panes.Pane {
	if e := t.Layout.Empty(); e != nil {
		return e
	}
	return t.Focused()
}

// breakPane is prefix !: the focused pane of a divided tab moves to a new
// tab at the end of the strip, which becomes active.
func (m *model) breakPane() {
	t := m.tabs.Current()
	if t == nil {
		return
	}
	n, err := m.tabs.Break(t, t.Focused())
	if err != nil {
		m.paneNotice(err.Error())
		return
	}
	if m.tabs.Index(t) >= 0 {
		m.layoutTab(t)
	}
	m.layoutTab(n)
	m.status = ""
	m.returnToPane()
	m.scrollToLive()
}
