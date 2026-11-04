// Package tabs is the TUI's tab registry. Every palette command opens a
// tab hosting one yoloctl subprocess under a PTY (internal/tui/term.Session);
// a tab can be split into panes (internal/tui/panes), each hosting one such
// subprocess. Closing a pane or tab kills only those client subprocesses —
// for `box attach` that is a session detach; the agent keeps running.
package tabs

import (
	"errors"

	"github.com/leogr/yoloctl/internal/tui/panes"
)

// Tab is one pane layout. A tab with one pane is a tab as it always was.
type Tab struct {
	Layout *panes.Layout
	Badge  bool // attention: bell/OSC 9/exit/request while unfocused
}

// New starts a tab with one pane.
func New(p *panes.Pane) *Tab { return &Tab{Layout: panes.New(p)} }

// Focused is the tab's focused pane: its session takes keys, and it is the
// single source of the window title, cursor and host mouse mode.
func (t *Tab) Focused() *panes.Pane { return t.Layout.Focus() }

// Title is the focused pane's title; while the empty pane is focused, the
// first other pane's.
func (t *Tab) Title() string {
	if p := t.Focused(); p != nil && !p.Empty() {
		return p.Title()
	}
	for _, p := range t.Layout.Panes() {
		if !p.Empty() {
			return p.Title()
		}
	}
	return ""
}

// Model is the ordered tab set plus the active index.
type Model struct {
	Tabs   []*Tab
	Active int
}

// Add appends a tab and focuses it.
func (m *Model) Add(t *Tab) {
	m.Tabs = append(m.Tabs, t)
	m.Active = len(m.Tabs) - 1
	t.Badge = false
}

// Current returns the active tab, nil when none exist.
func (m *Model) Current() *Tab {
	if len(m.Tabs) == 0 {
		return nil
	}
	if m.Active >= len(m.Tabs) {
		m.Active = len(m.Tabs) - 1
	}
	return m.Tabs[m.Active]
}

// Focused is the current tab's focused pane, nil when no tab exists.
func (m *Model) Focused() *panes.Pane {
	if t := m.Current(); t != nil {
		return t.Focused()
	}
	return nil
}

// CloseCurrent kills the current tab's focused pane (detach semantics) and
// removes it; the last pane removes its tab.
func (m *Model) CloseCurrent() {
	t := m.Current()
	if t == nil {
		return
	}
	m.ClosePane(t, t.Focused())
}

// ClosePane kills one pane's subprocess and removes it from its tab,
// removing the tab when it was the last pane, or when only the empty pane
// is left (an empty pane belongs to a split). It reports whether the tab
// was removed.
func (m *Model) ClosePane(t *Tab, p *panes.Pane) bool {
	i := m.index(t)
	if i < 0 || p == nil {
		return false
	}
	if !p.Empty() {
		p.Session.Close()
	}
	if !t.Layout.Remove(p) && !onlyEmpty(t) {
		return false
	}
	m.remove(i)
	return true
}

// onlyEmpty reports a tab whose panes are all empty.
func onlyEmpty(t *Tab) bool {
	for _, p := range t.Layout.Panes() {
		if !p.Empty() {
			return false
		}
	}
	return true
}

// remove takes tab i out of the strip, which renumbers; the active index
// keeps the active tab, or moves to its left neighbour when i was active.
func (m *Model) remove(i int) {
	m.Tabs = append(m.Tabs[:i], m.Tabs[i+1:]...)
	if m.Active >= len(m.Tabs) && m.Active > 0 || i < m.Active {
		m.Active--
	}
}

// Move refusals; panes.ErrTooSmall refuses a tree that does not fit.
var (
	ErrSameTab    = errors.New("that is this tab")
	ErrNoTab      = errors.New("the tab is gone")
	ErrEmptyPane  = errors.New("the tab has an empty pane")
	ErrUndivided  = errors.New("already its own tab")
	ErrEmptyBreak = errors.New("an empty pane cannot be its own tab")
)

// Join moves src's whole pane tree into dst, beside target on side d or in
// target's place when target is dst's empty pane, as one operation: either
// everything changes or nothing does. src leaves the strip, which
// renumbers; dst becomes the active tab at its new index. No session
// starts or closes. A source holding an empty pane is refused.
func (m *Model) Join(src, dst *Tab, target *panes.Pane, d panes.Dir) error {
	if err := m.CanJoin(src, dst, target, d); err != nil {
		return err
	}
	si := m.index(src)
	if err := dst.Layout.Graft(target, d, src.Layout); err != nil {
		return err
	}
	m.Tabs = append(m.Tabs[:si], m.Tabs[si+1:]...)
	m.Active = m.index(dst)
	dst.Badge = false
	return nil
}

// CanJoin is Join's preflight, changing nothing.
func (m *Model) CanJoin(src, dst *Tab, target *panes.Pane, d panes.Dir) error {
	switch {
	case src == dst:
		return ErrSameTab
	case m.index(src) < 0 || m.index(dst) < 0:
		return ErrNoTab
	case src.Layout.Empty() != nil:
		return ErrEmptyPane
	}
	return dst.Layout.CanGraft(target, d, src.Layout)
}

// Break moves pane p of a divided tab into a new tab at the end of the
// strip, which becomes active. The pane keeps its session; when only the
// empty pane is left behind, that tab goes too.
func (m *Model) Break(t *Tab, p *panes.Pane) (*Tab, error) {
	switch {
	case m.index(t) < 0:
		return nil, ErrNoTab
	case p.Empty():
		return nil, ErrEmptyBreak
	case !t.Layout.Bars():
		return nil, ErrUndivided
	}
	t.Layout.Remove(p)
	if onlyEmpty(t) {
		m.remove(m.index(t))
	}
	n := New(p)
	m.Add(n)
	return n, nil
}

// CloseTab kills every pane's subprocess of a tab (a detach for managed
// views) and removes the tab; the strip renumbers and the active index
// adjusts as ClosePane does.
func (m *Model) CloseTab(t *Tab) {
	for _, p := range t.Layout.Panes() {
		if m.ClosePane(t, p) {
			return
		}
	}
}

// Index is a tab's position in the strip, -1 when it is gone.
func (m *Model) Index(t *Tab) int { return m.index(t) }

func (m *Model) index(t *Tab) int {
	for i, tab := range m.Tabs {
		if tab == t {
			return i
		}
	}
	return -1
}

// Owner finds the tab and pane hosting a session.
func (m *Model) Owner(match func(*panes.Pane) bool) (*Tab, *panes.Pane) {
	for _, t := range m.Tabs {
		for _, p := range t.Layout.Panes() {
			if match(p) {
				return t, p
			}
		}
	}
	return nil, nil
}

// Next / Prev cycle focus; Select jumps to index i when valid. The newly
// focused tab's badge clears, and so does its focused pane's mark.
func (m *Model) Next() { m.step(1) }
func (m *Model) Prev() { m.step(-1) }

func (m *Model) step(d int) {
	if len(m.Tabs) == 0 {
		return
	}
	m.Select((m.Active + d + len(m.Tabs)) % len(m.Tabs))
}

func (m *Model) Select(i int) {
	if i >= 0 && i < len(m.Tabs) {
		m.Active = i
		m.Tabs[i].Badge = false
		if p := m.Tabs[i].Focused(); p != nil {
			p.Mark = false
		}
	}
}
