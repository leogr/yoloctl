package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// ActionKind is an inert intent or opaque caller ID; callers own every effect.
type ActionKind int

type Action struct {
	Kind         ActionKind
	Value, Extra string
}

// Run is one styled span of a rendered row. A copyable run shows the bytes
// [start, start+len(text)) of its logical display line verbatim; gutters,
// times, chips, frames and padding are decorations and are never copied.
type Run struct {
	Text  string
	Style spaceview.Style
	Copy  bool
	Start int
	Act   Action
	// strike draws the run struck through (Markdown ~~text~~); the shared
	// style has no such attribute.
	Strike bool
}

func Deco(text string, st spaceview.Style) Run { return Run{Text: text, Style: st} }

// paint renders runs into exactly width cells. hl reports the byte range of
// a copyable run's text to show as selected.
func Paint(wm ansi.Method, runs []Run, width int, fill spaceview.Color, hl func(Run) (int, int), sel spaceview.Style) string {
	var b strings.Builder
	used := 0
	for _, r := range runs {
		if used >= width {
			break
		}
		text := r.Text
		if w := wm.StringWidth(text); used+w > width {
			text = Truncate(wm, text, width-used, "")
		}
		lo, hi := 0, 0
		if hl != nil && r.Copy {
			lo, hi = hl(r)
			lo, hi = min(max(lo, 0), len(text)), min(max(hi, 0), len(text))
		}
		styled := func(text string, st spaceview.Style) string {
			if r.Strike && text != "" {
				return StrikeOn + spaceview.StyleText(text, st) // its reset ends the strike
			}
			return spaceview.StyleText(text, st)
		}
		if lo < hi {
			b.WriteString(styled(text[:lo], r.Style))
			b.WriteString(styled(text[lo:hi], Merge(r.Style, sel)))
			b.WriteString(styled(text[hi:], r.Style))
		} else {
			b.WriteString(styled(text, r.Style))
		}
		used += wm.StringWidth(text)
	}
	if used < width {
		b.WriteString(spaceview.StyleText(strings.Repeat(" ", width-used), spaceview.Style{BG: fill}))
	}
	return b.String()
}

// strikeOn starts struck-through text (SGR 9).
const StrikeOn = "\x1b[9m"

// merge lays a selection over a run, keeping emphasis but replacing colors.
func Merge(base, over spaceview.Style) spaceview.Style {
	base.FG, base.BG = over.FG, over.BG
	return base
}

func RunsWidth(wm ansi.Method, runs []Run) int {
	w := 0
	for _, r := range runs {
		w += wm.StringWidth(r.Text)
	}
	return w
}

// wrapRanges splits display text into rows of at most width cells and
// returns their byte ranges, which together cover the text exactly. Prose
// breaks after spaces; a space at the edge hangs, staying in its row's bytes
// without being drawn. Code and words wider than a row break per grapheme.
func WrapRanges(wm ansi.Method, text string, width int, words bool) [][2]int {
	width = max(width, 1)
	var rows [][2]int
	start, col, brk := 0, 0, -1
	for o := 0; o < len(text); {
		g, w := ansi.FirstGraphemeCluster(text[o:], wm)
		next := o + len(g)
		space := g == " "
		if space && col+w > width && col > 0 {
			rows = append(rows, [2]int{start, next})
			start, col, brk, o = next, 0, -1, next
			continue
		}
		for col+w > width && col > 0 {
			if words && brk > start {
				rows = append(rows, [2]int{start, brk})
				start, col = brk, wm.StringWidth(text[brk:o])
			} else {
				rows = append(rows, [2]int{start, o})
				start, col = o, 0
			}
			brk = -1
		}
		col += w
		if space {
			brk = next
		}
		o = next
	}
	if start < len(text) || len(rows) == 0 {
		rows = append(rows, [2]int{start, len(text)})
	}
	return rows
}

// hitRun maps cell column col of a body row to the display-line bytes of
// the grapheme drawn there, so a half-covered wide grapheme is included
// whole. Columns over the gutter snap to the first copyable byte and columns
// past the text (padding, a hanging space) to the row end.
func HitRun(wm ansi.Method, runs []Run, col, rowEnd int) (lo, hi int) {
	x := 0
	for _, r := range runs {
		w := wm.StringWidth(r.Text)
		if r.Copy {
			if col < x {
				return r.Start, r.Start
			}
			if col < x+w {
				gx := x
				for o := 0; o < len(r.Text); {
					g, gw := ansi.FirstGraphemeCluster(r.Text[o:], wm)
					if col < gx+gw {
						return r.Start + o, r.Start + o + len(g)
					}
					gx += gw
					o += len(g)
				}
			}
		}
		x += w
	}
	return rowEnd, rowEnd
}

// actionAt returns the click target under cell column col.
func ActionAt(wm ansi.Method, runs []Run, col int) Action {
	x := 0
	for _, r := range runs {
		w := wm.StringWidth(r.Text)
		if col >= x && col < x+w {
			return r.Act
		}
		x += w
	}
	return Action{}
}
