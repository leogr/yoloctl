package panes

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Chrome colours: the inactive tab's blue for unfocused borders, and the
// active tab's orange for the border cells around the focused pane.
var (
	borderColor color.Color = ansi.IndexedColor(67)
	focusColor  color.Color = ansi.IndexedColor(215)
	// dropTint is the background of a dock target that would be taken
	// (decision 10); a refused one is hatched in grey.
	dropTint   color.Color = ansi.IndexedColor(94)
	hatchColor color.Color = ansi.IndexedColor(243)
)

// barColors are a bar's colours: its band, its text, and its ⤢ and ✕
// buttons with their glyphs.
type barColors struct{ band, text, button, glyph color.Color }

var (
	focusedBar   = barColors{ansi.IndexedColor(215), ansi.IndexedColor(235), ansi.IndexedColor(208), ansi.IndexedColor(235)}
	unfocusedBar = barColors{ansi.IndexedColor(67), ansi.IndexedColor(232), ansi.IndexedColor(60), ansi.IndexedColor(255)}
)

// border glyphs by neighbour bits: 1 up, 2 down, 4 left, 8 right.
var borderGlyphs = [16]string{
	1: "│", 2: "│", 1 | 2: "│", 4: "─", 8: "─", 4 | 8: "─",
	1 | 2 | 8: "├", 1 | 2 | 4: "┤", 2 | 4 | 8: "┬", 1 | 4 | 8: "┴", 1 | 2 | 4 | 8: "┼",
	2 | 8: "┌", 2 | 4: "┐", 1 | 8: "└", 1 | 4: "┘",
}

type cell struct {
	uv.Cell
	border bool
}

// Compose renders the visible panes over the w×h tab area as styled rows.
// Each pane contributes its own frame rows (already selection-styled),
// clipped to its rectangle: a wide cell crossing the right edge renders as
// a space in its background, and every pane and border segment ends with a
// reset, so no style crosses a pane edge. Borders and bars are drawn by
// yoloctl, never by a child: every pane's bar is the band row below it,
// orange for the focused pane and blue for the others; a bar with a pane
// below it is that horizontal border. Vertical borders are blue, orange
// beside the focused pane. While a border is dragged the preview's
// rectangles are drawn, with that border in orange. No cursor
// is composed: the host cursor is the focused pane's, placed by the caller;
// other panes show none.
func Compose(l *Layout, rows map[*Pane][]string) []string {
	w, h := l.w, l.h
	grid := make([][]cell, h)
	owned := make([][]bool, h)
	for y := range grid {
		grid[y] = make([]cell, w)
		owned[y] = make([]bool, w)
		for x := range grid[y] {
			grid[y][x] = cell{Cell: uv.Cell{Content: " ", Width: 1}}
		}
	}
	geo, dragged := l.previewGeometry()
	rect := func(p *Pane) (Rect, Rect) {
		if g, ok := geo[p]; ok {
			return g[0], g[1]
		}
		return p.rect, p.bar
	}
	for _, p := range l.Visible() {
		r, _ := rect(p)
		for y := r.Y; y < r.Y+r.H && y < h; y++ {
			for x := r.X; x < r.X+r.W && x < w; x++ {
				owned[y][x] = true
			}
		}
		lines := rows[p]
		for y := 0; y < r.H && y < len(lines) && r.Y+y < h; y++ {
			placeRow(grid[r.Y+y], r.X, r.W, lines[y])
		}
	}
	target := Rect{}
	if d := l.drop; d != nil && l.leaf(d.Pane) != nil {
		r, bar := rect(d.Pane)
		drawDrop(grid, d.rect(r), d.OK)
		if d.OK {
			target = Rect{r.X - 1, r.Y - 1, r.W + 2, bar.Y - r.Y + 2}
		}
	}
	if l.Bars() {
		var hot func(x, y int) bool
		if dragged != nil {
			// The dragged border: its column, or its row, across the split.
			a, _ := rect(firstLeaf(dragged.b))
			r := dragged.rect
			hot = func(x, y int) bool {
				if dragged.vertical {
					return x == a.X-1 && y >= r.Y && y <= r.Y+r.H
				}
				return y == a.Y-1 && x >= r.X && x < r.X+r.W
			}
		}
		if target.W > 0 {
			// A target that would be taken: its border and bar turn orange.
			drag := hot
			hot = func(x, y int) bool { return target.contains(x, y) || drag != nil && drag(x, y) }
		}
		focus, focusBar := rect(l.focus)
		var bars []Rect
		for _, p := range l.Visible() {
			_, bar := rect(p)
			bars = append(bars, bar)
		}
		drawBorders(grid, owned, focus, focusBar, bars, hot)
		for _, p := range l.Visible() {
			_, bar := rect(p)
			drawBar(grid, l, p, bar)
		}
	}
	out := make([]string, h)
	for y, row := range grid {
		out[y] = renderRow(row)
	}
	return out
}

// drawDrop draws a dock target over its cells: a passing target keeps
// their text on the dim orange tint, a refused one is hatched in grey.
func drawDrop(grid [][]cell, r Rect, ok bool) {
	space := cell{Cell: uv.Cell{Content: " ", Width: 1}}
	for y := max(r.Y, 0); y < r.Y+r.H && y < len(grid); y++ {
		row := grid[y]
		x0, x1 := max(r.X, 0), min(r.X+r.W, len(row))
		for x := x0; x < x1; x++ {
			if ok {
				row[x].Style.Bg = dropTint
				continue
			}
			row[x].Cell = uv.Cell{Content: "╱", Width: 1, Style: uv.Style{Fg: hatchColor}}
		}
		if ok {
			continue
		}
		// A wide cell cut by the hatch keeps its columns: its other half
		// outside the target becomes a space.
		if x0 > 0 && x0 < x1 && row[x0-1].Width > 1 {
			row[x0-1] = space
		}
		if x1 < len(row) && row[x1].Width == 0 {
			row[x1] = space
		}
	}
}

func firstLeaf(n *node) *Pane {
	for n.pane == nil {
		n = n.a
	}
	return n.pane
}

func placeRow(row []cell, x0, width int, line string) {
	parsed := uv.NewStyledString(line).Lines(ansi.GraphemeWidth)
	if len(parsed) == 0 {
		return
	}
	x := 0
	for _, c := range parsed[0] {
		if c.Width == 0 && c.Content == "" {
			continue
		}
		cw := max(c.Width, 1)
		if x >= width {
			return
		}
		if x+cw > width {
			// A wide cell crossing the pane's right edge becomes a space in
			// its own background; it never spills into the border.
			row[x0+x] = cell{Cell: uv.Cell{Content: " ", Width: 1, Style: uv.Style{Bg: c.Style.Bg}}}
			return
		}
		row[x0+x] = cell{Cell: uv.Cell{Content: c.Content, Width: cw, Style: c.Style, Link: c.Link}}
		for i := 1; i < cw; i++ {
			row[x0+x+i] = cell{Cell: uv.Cell{Width: 0}}
		}
		x += cw
	}
}

func drawBorders(grid [][]cell, owned [][]bool, focus, focusBar Rect, bars []Rect, hot func(x, y int) bool) {
	h := len(grid)
	if h == 0 {
		return
	}
	w := len(grid[0])
	isBorder := func(x, y int) bool { return x >= 0 && y >= 0 && x < w && y < h && !owned[y][x] }
	// barBeside reports whether the cell sits on a bar row beside a bar.
	barBeside := func(x, y int) bool {
		for _, b := range bars {
			if b.W > 0 && b.Y == y && (x == b.X-1 || x == b.X+b.W) {
				return true
			}
		}
		return false
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if owned[y][x] {
				continue
			}
			bits := 0
			if isBorder(x, y-1) {
				bits |= 1
			}
			if isBorder(x, y+1) {
				bits |= 2
			}
			if isBorder(x-1, y) {
				bits |= 4
			}
			if isBorder(x+1, y) {
				bits |= 8
			}
			glyph := borderGlyphs[bits]
			if glyph == "" {
				glyph = " "
			}
			// Orange: the vertical borders beside the focused pane, from its
			// first content row to its own bar row; the bar row above it is
			// that pane's, blue. Blue elsewhere.
			fg := borderColor
			side := (x == focus.X-1 || x == focus.X+focus.W) && y >= focus.Y && y <= focus.Y+focus.H
			isHot := hot != nil && hot(x, y)
			if side || focusBar.contains(x, y) || isHot {
				fg = focusColor
			}
			if barBeside(x, y) {
				// A border crossing a bar row stays on the terminal
				// background: no band crosses it and no junction glyph is
				// drawn. Beside the focused bar it is a half block leaning
				// toward that bar; elsewhere a │.
				switch {
				case y == focusBar.Y && x == focusBar.X-1:
					glyph, fg = "▐", focusColor
				case y == focusBar.Y && x == focusBar.X+focusBar.W:
					glyph, fg = "▌", focusColor
				default:
					glyph = "│"
				}
			}
			grid[y][x] = cell{Cell: uv.Cell{Content: glyph, Width: 1, Style: uv.Style{Fg: fg}}, border: true}
		}
	}
}

// drawBar paints a pane's bar: the band across its row, then its segments
// over it, in the focused pane's orange with dark text and a bold title, or
// the unfocused blue with dark text; each button on its own darker cells;
// the attention mark bold orange. Bars are not underlined.
func drawBar(grid [][]cell, l *Layout, p *Pane, bar Rect) {
	if bar.W <= 0 || bar.Y < 0 || bar.Y >= len(grid) {
		return
	}
	colors := unfocusedBar
	if p == l.focus {
		colors = focusedBar
	}
	band := uv.Style{Fg: colors.text, Bg: colors.band}
	row := grid[bar.Y]
	for x := max(bar.X, 0); x < bar.X+bar.W && x < len(row); x++ {
		row[x] = cell{Cell: uv.Cell{Content: " ", Width: 1, Style: band}, border: true}
	}
	for _, s := range l.barSegs(p, bar) {
		if s.part == BarNone {
			continue
		}
		style := band
		text := s.text
		switch {
		case s.text == "●":
			// The mark stands apart from the title; its leading space is
			// bold orange too.
			style.Fg, style.Attrs = focusColor, uv.AttrBold
			text = " ●"
			s.x--
		case s.part == BarTitle && s.text != "▸":
			if p == l.focus {
				style.Attrs = uv.AttrBold
			}
			text = " " + s.text + " " // the title keeps a space on each side
			s.x--
		case s.part != BarTitle:
			style.Fg, style.Bg = colors.glyph, colors.button
		}
		parsed := uv.NewStyledString(text).Lines(l.method())
		if len(parsed) == 0 {
			continue
		}
		x := bar.X + s.x
		for _, c := range parsed[0] {
			if c.Width == 0 && c.Content == "" {
				continue
			}
			cw := max(c.Width, 1)
			if x+cw > bar.X+bar.W || x >= len(row) {
				break
			}
			c.Style = style
			row[x] = cell{Cell: c, border: true}
			for i := 1; i < cw; i++ {
				row[x+i] = cell{Cell: uv.Cell{Width: 0}, border: true}
			}
			x += cw
		}
	}
}

// renderRow writes pane and border runs, each from its own cells and each
// closed with a reset.
func renderRow(row []cell) string {
	var b strings.Builder
	run := make(uv.Line, 0, len(row))
	border := false
	flush := func() {
		if len(run) > 0 {
			b.WriteString(run.Render())
			b.WriteString(ansi.ResetStyle)
			run = run[:0]
		}
	}
	for _, c := range row {
		if c.Width == 0 {
			continue
		}
		if c.border != border {
			flush()
			border = c.border
		}
		run = append(run, c.Cell)
	}
	flush()
	return b.String()
}
