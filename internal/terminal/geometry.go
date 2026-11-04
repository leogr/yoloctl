package terminal

import (
	"errors"
	"fmt"
)

// Geometry describes the text grid, excluding any host padding or TUI chrome.
// Cell dimensions are measured pixels, or both zero when unavailable. Never
// infer them by dividing TIOCGWINSZ's pixel totals: some hosts include padding.
type Geometry struct {
	Cols, Rows            int
	CellWidth, CellHeight int
}

func (g Geometry) Valid() bool {
	return validSize(g.Rows, g.Cols) &&
		(g.CellWidth == 0 && g.CellHeight == 0 ||
			g.CellWidth > 0 && g.CellWidth <= 4096 && g.CellHeight > 0 && g.CellHeight <= 4096)
}

func (t *Terminal) Geometry() Geometry {
	return Geometry{Cols: t.Get(1), Rows: t.Get(2), CellWidth: t.Get(17), CellHeight: t.Get(18)}
}

// ResizeGeometry updates rows, columns and measured cell dimensions together.
// The native parser owns size queries and mode-2048 resize notifications.
func (t *Terminal) ResizeGeometry(g Geometry) error {
	if !g.Valid() {
		return errors.New("invalid terminal geometry")
	}
	if status := t.call("ghostty_terminal_resize", t.ptr, uint64(g.Cols), uint64(g.Rows), uint64(g.CellWidth), uint64(g.CellHeight)); status != 0 && t.err == nil {
		t.err = fmt.Errorf("terminal resize failed (%d)", status)
	}
	return t.err
}
