package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// tableRunWidth shares the table-only soft breaks with tableWrapRanges.
// Trailing spaces may hang beyond the column; punctuation stays with its run.
func tableRunWidth(wm ansi.Method, text string) int {
	start, widest := 0, 0
	previousWide := false
	take := func(end int) {
		widest = max(widest, wm.StringWidth(strings.TrimRight(text[start:end], " ")))
		start = end
	}
	for o := 0; o < len(text); {
		g, w := ansi.FirstGraphemeCluster(text[o:], wm)
		if previousWide && w == 2 {
			take(o)
		}
		next := o + len(g)
		if g == " " || g == "-" || g == "/" || g == "_" {
			take(next)
		}
		previousWide = w == 2
		o = next
	}
	take(len(text))
	return widest
}

// Literal replacements can change break opportunities without changing the
// token-sized table geometry. Refresh just these private floors for layout;
// the document's logical cells and shared table metadata remain untouched.
func tableLayoutLines(wm ansi.Method, lines []DisplayLine) []DisplayLine {
	tbl := lines[0].Table
	end, changed := 0, false
	for end < len(lines) && lines[end].Table == tbl {
		changed = changed || len(lines[end].Subs) > 0
		end++
	}
	if !changed {
		return lines
	}
	copyTable := *tbl
	copyTable.WordFloor = make([]int, len(tbl.Natural))
	copyTable.CompactFloor = make([]int, len(tbl.Natural))
	out := append([]DisplayLine(nil), lines[:end]...)
	for i, line := range out {
		out[i].Table = &copyTable
		for c, span := range line.Cells {
			text := line.Text[span[0]:span[1]]
			copyTable.WordFloor[c] = max(copyTable.WordFloor[c], tableRunWidth(wm, text))
			copyTable.CompactFloor[c] = min(tbl.Natural[c], max(copyTable.CompactFloor[c], WidestGrapheme(wm, text)+wm.StringWidth("…")))
		}
	}
	return out
}
