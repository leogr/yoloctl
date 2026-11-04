package chatui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// actionKind names what a click on a run does. Targets come from data the
// component already displays; the host re-validates every effect (link
// policy, attachment identity) when it acts.
type actionKind int

const (
	actNone     actionKind = iota
	actLink                // value: URL
	actAuthor              // value: authenticated account ("" if unknown), extra: nick
	actReply               // value: replied-to message ID
	actMention             // value: mentioned nick
	actRef                 // value: referenced message's server ID
	actFilter              // shows all messages again
	actCopyCode            // value: a fenced block's code, copied as is
)

type action struct {
	kind         actionKind
	value, extra string
}

// run is one styled span of a rendered row. A copyable run shows the bytes
// [start, start+len(text)) of its logical display line verbatim; gutters,
// times, chips, frames and padding are decorations and are never copied.
type run struct {
	text  string
	style spaceview.Style
	copy  bool
	start int
	act   action
	// strike draws the run struck through (Markdown ~~text~~); the shared
	// style has no such attribute.
	strike bool
}

func deco(text string, st spaceview.Style) run { return run{text: text, style: st} }

type rowKind int

const (
	rowBody rowKind = iota
	rowHeader
	rowDecor // reply labels, fence frames, separators, errors and limits
	rowSpacer
	// rowEvent is a room notice (join, part, role, topic, guidance). It is
	// keyed to the preceding message only to anchor the reading position:
	// it never shows that message's marks, and clicking it selects nothing.
	rowEvent
)

// transcriptRow is one display row. key/line/offset anchor the reading
// position; body rows also carry [offset, end) of their display line, so a
// selection maps back to exact bytes regardless of wrapping. A wrapped
// table row shows only frags of that range.
type transcriptRow struct {
	key          string
	line, offset int
	end          int
	kind         rowKind
	runs         []run
	// fill paints the cells after the runs, e.g. a code block background.
	fill  spaceview.Color
	frags [][2]int
}

// holds reports whether a wrapped table row shows the display-line byte at
// offset: its [offset, end) envelope alone does not prove it.
func (r transcriptRow) holds(offset int) bool {
	for _, f := range r.frags {
		if f[0] <= offset && offset < f[1] {
			return true
		}
	}
	return false
}

// nextFrag returns where the row's first cell fragment after offset starts,
// or -1: the next text a table shows after an offset in its padding or
// separators.
func (r transcriptRow) nextFrag(offset int) int {
	next := -1
	for _, f := range r.frags {
		if f[0] > offset && (next < 0 || f[0] < next) {
			next = f[0]
		}
	}
	return next
}

// paint renders runs into exactly width cells. hl reports the byte range of
// a copyable run's text to show as selected.
func paint(wm ansi.Method, runs []run, width int, fill spaceview.Color, hl func(run) (int, int), sel spaceview.Style) string {
	var b strings.Builder
	used := 0
	for _, r := range runs {
		if used >= width {
			break
		}
		text := r.text
		if w := wm.StringWidth(text); used+w > width {
			text = truncate(wm, text, width-used, "")
		}
		lo, hi := 0, 0
		if hl != nil && r.copy {
			lo, hi = hl(r)
			lo, hi = min(max(lo, 0), len(text)), min(max(hi, 0), len(text))
		}
		styled := func(text string, st spaceview.Style) string {
			if r.strike && text != "" {
				return strikeOn + spaceview.StyleText(text, st) // its reset ends the strike
			}
			return spaceview.StyleText(text, st)
		}
		if lo < hi {
			b.WriteString(styled(text[:lo], r.style))
			b.WriteString(styled(text[lo:hi], merge(r.style, sel)))
			b.WriteString(styled(text[hi:], r.style))
		} else {
			b.WriteString(styled(text, r.style))
		}
		used += wm.StringWidth(text)
	}
	if used < width {
		b.WriteString(spaceview.StyleText(strings.Repeat(" ", width-used), spaceview.Style{BG: fill}))
	}
	return b.String()
}

// strikeOn starts struck-through text (SGR 9).
const strikeOn = "\x1b[9m"

// merge lays a selection over a run, keeping emphasis but replacing colors.
func merge(base, over spaceview.Style) spaceview.Style {
	base.FG, base.BG = over.FG, over.BG
	return base
}

func runsWidth(wm ansi.Method, runs []run) int {
	w := 0
	for _, r := range runs {
		w += wm.StringWidth(r.text)
	}
	return w
}

// wrapRanges splits display text into rows of at most width cells and
// returns their byte ranges, which together cover the text exactly. Prose
// breaks after spaces; a space at the edge hangs, staying in its row's bytes
// without being drawn. Code and words wider than a row break per grapheme.
func wrapRanges(wm ansi.Method, text string, width int, words bool) [][2]int {
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
func hitRun(wm ansi.Method, runs []run, col, rowEnd int) (lo, hi int) {
	x := 0
	for _, r := range runs {
		w := wm.StringWidth(r.text)
		if r.copy {
			if col < x {
				return r.start, r.start
			}
			if col < x+w {
				gx := x
				for o := 0; o < len(r.text); {
					g, gw := ansi.FirstGraphemeCluster(r.text[o:], wm)
					if col < gx+gw {
						return r.start + o, r.start + o + len(g)
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
func actionAt(wm ansi.Method, runs []run, col int) action {
	x := 0
	for _, r := range runs {
		w := wm.StringWidth(r.text)
		if col >= x && col < x+w {
			return r.act
		}
		x += w
	}
	return action{}
}
