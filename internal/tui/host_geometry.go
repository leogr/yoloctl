package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

// Only CSI 16t measures a cell. TIOCGWINSZ can include window padding in its
// pixel totals, so division by its rows/columns is not a valid substitute.
type hostGeometry struct {
	width, height  int
	pending, dirty bool
	request        uint64
}

type geometryTimeout uint64

func (m *model) measureHostGeometry() tea.Cmd {
	h := &m.hostGeometry
	h.dirty = true
	return m.syncHostGeometry()
}

func (m *model) syncHostGeometry() tea.Cmd {
	h := &m.hostGeometry
	if h.pending || !h.dirty {
		return nil
	}
	h.dirty, h.pending = false, true
	h.request++
	id := h.request
	return tea.Sequence(tea.Raw("\x1b[16t"), tea.Tick(time.Second, func(time.Time) tea.Msg { return geometryTimeout(id) }))
}

func (m *model) acceptHostCellSize(width, height int) {
	g := terminal.Geometry{Cols: 1, Rows: 1, CellWidth: width, CellHeight: height}
	if !g.Valid() {
		return
	}
	h := &m.hostGeometry
	h.pending = false
	if h.width == width && h.height == height {
		return
	}
	h.width, h.height = width, height
	m.resizeSurfaces()
}

func (m *model) surfaceGeometry(rows, cols int) terminal.Geometry {
	return terminal.Geometry{Rows: max(rows, 1), Cols: max(cols, 1), CellWidth: m.hostGeometry.width, CellHeight: m.hostGeometry.height}
}

// resizeSurfaces lays every tab's panes out over the tab area and resizes
// each visible pane's session to its own rectangle with the shared measured
// cell pixels (unknown pixels stay unknown per pane). A zoomed tab's hidden
// panes keep their last size.
func (m *model) resizeSurfaces() {
	m.console.ResizeGeometry(m.surfaceGeometry(m.overlayMaxContentRows(), m.width))
	for _, t := range m.tabs.Tabs {
		m.layoutTab(t)
	}
}

// laidOut returns t's layout with its pane rectangles sized to the current
// tab area. Rectangles are pure geometry; only layoutTab also resizes the
// panes' sessions.
func (m *model) laidOut(t *tabs.Tab) *panes.Layout {
	t.Layout.Method = m.tabWidthMethod() // bar titles measure as the strip's labels
	w, h := max(m.width, 1), m.viewRows()
	if lw, lh := t.Layout.Size(); lw != w || lh != h {
		t.Layout.Resize(w, h)
	}
	return t.Layout
}

func (m *model) layoutTab(t *tabs.Tab) {
	t.Layout.Resize(max(m.width, 1), m.viewRows())
	for _, p := range t.Layout.Visible() {
		if p.Empty() {
			continue // no session to size
		}
		r := p.Rect()
		if err := p.Session.ResizeGeometry(m.surfaceGeometry(r.H, r.W)); err != nil {
			m.status = err.Error()
		}
	}
}
