package spaceview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Color is a terminal color. The zero value is the terminal default; ix(n)
// is a 256-color index and rgb a truecolor value.
type Color int32

const trueColor Color = 1 << 24

func ix(n int) Color { return Color(n + 1) }

func rgb(r, g, b uint8) Color { return trueColor | Color(r)<<16 | Color(g)<<8 | Color(b) }

// Style is one cell's rendition. The zero value uses the terminal defaults.
type Style struct {
	FG, BG    Color
	Bold      bool
	Underline bool
	Italic    bool
}

func fg(c Color) Style { return Style{FG: c} }

func (s Style) bold() Style      { s.Bold = true; return s }
func (s Style) underline() Style { s.Underline = true; return s }

func (s Style) sgr() string {
	var parts []string
	if s.Bold {
		parts = append(parts, "1")
	}
	if s.Italic {
		parts = append(parts, "3")
	}
	if s.Underline {
		parts = append(parts, "4")
	}
	parts = append(parts, colorSGR(s.FG, 38)...)
	parts = append(parts, colorSGR(s.BG, 48)...)
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func colorSGR(c Color, base int) []string {
	switch {
	case c == 0:
		return nil
	case c&trueColor != 0:
		v := int(c &^ trueColor)
		return []string{fmt.Sprint(base), "2", fmt.Sprint(v >> 16 & 0xff), fmt.Sprint(v >> 8 & 0xff), fmt.Sprint(v & 0xff)}
	default:
		return []string{fmt.Sprint(base), "5", fmt.Sprint(int(c) - 1)}
	}
}

// seg is a run of plain text with one style and an optional click target.
type seg struct {
	text  string
	style Style
	hit   int
}

// line is a horizontal run of segments.
type line []seg

func (l line) width() int {
	w := 0
	for _, s := range l {
		w += textWidth(s.text)
	}
	return w
}

func textWidth(s string) int { return ansi.StringWidthWc(s) }

// truncate shortens plain text to n columns, marking the cut with an ellipsis.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if textWidth(s) <= n {
		return s
	}
	return ansi.TruncateWc(s, n, "…")
}

// fit pads or truncates a line to exactly n columns. Truncation keeps whole
// segments where possible and ellipsizes the one that crosses the edge.
func (l line) fit(n int) line {
	out := make(line, 0, len(l)+1)
	used := 0
	for _, s := range l {
		w := textWidth(s.text)
		if used+w <= n {
			out = append(out, s)
			used += w
			continue
		}
		if room := n - used; room > 0 {
			s.text = truncate(s.text, room)
			out = append(out, s)
			used += textWidth(s.text)
		}
		break
	}
	if used < n {
		out = append(out, seg{text: strings.Repeat(" ", n-used)})
	}
	return out
}

// spread places right after left with padding so the result is n columns.
// When both do not fit, the right side wins its width and left is cut.
func spread(left, right line, n int, pad Style) line {
	rw := right.width()
	if rw > n {
		return right.fit(n)
	}
	if lw := left.width(); lw+rw > n && rw < n {
		// Keep one column between the sides when the left is cut.
		l := append(left.fit(n-rw-1), seg{text: " ", style: pad})
		return append(l, right...)
	}
	l := left.fit(n - rw)
	trimmed := l
	// fit pads with default style; repaint the padding with the requested one.
	if len(trimmed) > 0 && strings.TrimSpace(trimmed[len(trimmed)-1].text) == "" && trimmed[len(trimmed)-1].hit == 0 {
		trimmed[len(trimmed)-1].style = pad
	}
	return append(trimmed, right...)
}

// cell is one terminal column. A wide grapheme occupies its first cell; the
// following cell is a continuation with empty text.
type cell struct {
	text  string
	style Style
	hit   int
	cont  bool
}

// canvas is a fixed grid the layout writes into and the renderer flattens.
type canvas struct {
	w, h  int
	cells [][]cell
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, cells: make([][]cell, h)}
	blank := cell{text: " "}
	for y := range c.cells {
		row := make([]cell, w)
		for x := range row {
			row[x] = blank
		}
		c.cells[y] = row
	}
	return c
}

// put writes l at (x, y), clipping at the right edge.
func (c *canvas) put(x, y int, l line) {
	if y < 0 || y >= c.h {
		return
	}
	row := c.cells[y]
	for _, s := range l {
		rest := s.text
		for len(rest) > 0 {
			g, w := ansi.FirstGraphemeCluster(rest, ansi.WcWidth)
			rest = rest[len(g):]
			if w <= 0 {
				continue
			}
			if x < 0 {
				x += w
				continue
			}
			if x+w > c.w {
				return
			}
			for i := range w {
				eraseGlyph(row, x+i)
			}
			row[x] = cell{text: g, style: s.style, hit: s.hit}
			for i := 1; i < w; i++ {
				row[x+i] = cell{style: s.style, hit: s.hit, cont: true}
			}
			x += w
		}
	}
}

// eraseGlyph clears the entire old glyph when an overlay touches any of its
// cells. Leaving a leader or continuation outside the overlay would change the
// rendered row width. Preserve those outside cells' background and hit target.
func eraseGlyph(row []cell, x int) {
	for x > 0 && row[x].cont {
		x--
	}
	end := x + 1
	for end < len(row) && row[end].cont {
		end++
	}
	for ; x < end; x++ {
		row[x].text, row[x].cont = " ", false
	}
}

// putFirstRow draws l as the view's first row on FirstRowBG: segments
// without a background take it, and cells l leaves unwritten keep it.
func (c *canvas) putFirstRow(l line) {
	c.fill(0, 0, c.w, 1, Style{BG: FirstRowBG}, 0)
	c.put(0, 0, onSurface(l, FirstRowBG))
}

// fill paints a rectangle with a style (spaces) and target.
func (c *canvas) fill(x, y, w, h int, st Style, hit int) {
	for yy := max(y, 0); yy < min(y+h, c.h); yy++ {
		for xx := max(x, 0); xx < min(x+w, c.w); xx++ {
			eraseGlyph(c.cells[yy], xx)
			c.cells[yy][xx] = cell{text: " ", style: st, hit: hit}
		}
	}
}

func (c *canvas) hitAt(x, y int) int {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return 0
	}
	return c.cells[y][x].hit
}

// render flattens the grid into rows of SGR-styled text.
func (c *canvas) render() string {
	var b strings.Builder
	for y, row := range c.cells {
		if y > 0 {
			b.WriteByte('\n')
		}
		var cur Style
		open := false
		for _, cl := range row {
			if cl.cont {
				continue
			}
			if !open || cl.style != cur {
				if open {
					b.WriteString("\x1b[0m")
				}
				b.WriteString(cl.style.sgr())
				cur, open = cl.style, true
			}
			b.WriteString(cl.text)
		}
		if open {
			b.WriteString("\x1b[0m")
		}
	}
	return b.String()
}

// plain flattens the grid without styles (tests and --once without color).
func (c *canvas) plain() string {
	var b strings.Builder
	for y, row := range c.cells {
		if y > 0 {
			b.WriteByte('\n')
		}
		var r strings.Builder
		for _, cl := range row {
			if !cl.cont {
				r.WriteString(cl.text)
			}
		}
		b.WriteString(strings.TrimRight(r.String(), " "))
	}
	return b.String()
}
