package markdownview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// TableFitReason distinguishes a naturally fitting table from a refused layout.
type TableFitReason uint8

const (
	TableFits TableFitReason = iota
	TableLaidOut
	TableTooNarrow
	TableOverBudget
	TableNoRows
)

func tableNotice(wm ansi.Method, line, width int, theme spaceview.Theme, text string) BodyRow {
	return BodyRow{Line: line, Decor: true, Runs: []Run{Deco(Truncate(wm, text, width, "…"), spaceview.Style{FG: theme.Dim})}}
}

type labelledField struct {
	line   int
	label  string
	text   string
	ranges [][2]int
	cell   [2]int
}

// labelledTable first counts labels, cell bytes, indentation, notices and blank
// rows. No repeated-label output is allocated until the entire layout fits.
func labelledTable(wm ansi.Method, lines []DisplayLine, first, width, rowsLeft int, theme spaceview.Theme, reason TableFitReason) [][]BodyRow {
	tbl := lines[0].Table
	end := 1
	for end < len(lines) && lines[end].Table == tbl {
		end++
	}
	lines = lines[:end]
	message := "Table layout exceeds the display limit; showing labelled rows."
	if reason == TableTooNarrow {
		message = fmt.Sprintf("Table too narrow for %d columns; showing labelled rows.", len(tbl.Natural))
	}
	notice := tableNotice(wm, first, width, theme, message)
	// The header is represented only by decorative labels in this layout.
	// A gesture starting on the notice must anchor to the first visible value,
	// rather than copying a hidden logical header on its way to the body.
	if len(lines) > 2 && len(lines[2].Cells) > 0 {
		notice.Line = first + 2
		notice.Start = lines[2].Cells[0][0]
		notice.End = notice.Start
	}
	budget := 2*tbl.Source + TableSlack
	indent := min(2, max(width-1, 0))
	labels := make([]string, len(lines[0].Cells))
	for c, cr := range lines[0].Cells {
		labels[c] = lines[0].Text[cr[0]:cr[1]] + ": "
	}
	// This lower bound rejects pathological repeated headers before constructing
	// even the candidate fragments (e.g. a 2KB header repeated over 512 records).
	used := len(notice.Runs[0].Text) + 1
	records := 0
	for _, dl := range lines[2:] {
		if dl.Cells == nil {
			continue
		}
		if records > 0 {
			used++
		}
		records++
		for c, cr := range dl.Cells {
			used += len(labels[c]) + cr[1] - cr[0]
		}
		if used > budget {
			return nil
		}
	}
	fields := make([]labelledField, 0)
	count := 1 + max(records-1, 0)
	for i, dl := range lines[2:] {
		for c, cr := range dl.Cells {
			text := labels[c] + dl.Text[cr[0]:cr[1]]
			ranges := WrapRanges(wm, text, max(width, 1), true)
			firstEnd := ranges[0][1]
			ranges = ranges[:1]
			if firstEnd < len(text) {
				for _, r := range WrapRanges(wm, text[firstEnd:], max(width-indent, 1), true) {
					ranges = append(ranges, [2]int{r[0] + firstEnd, r[1] + firstEnd})
				}
			}
			used += len(ranges) + indent*max(len(ranges)-1, 0)
			count += len(ranges)
			if used > budget || count >= rowsLeft {
				return nil
			}
			fields = append(fields, labelledField{line: i + 2, label: labels[c], text: text, ranges: ranges, cell: cr})
		}
	}
	if count >= rowsLeft || used > budget {
		return nil
	}
	out := make([][]BodyRow, len(lines))
	out[0] = []BodyRow{notice}
	previous := -1
	for _, field := range fields {
		i := field.line
		if previous >= 0 && previous != i {
			out[i] = append(out[i], BodyRow{Line: first + i, Decor: true})
		}
		previous = i
		dl := lines[i]
		for j, r := range field.ranges {
			row := BodyRow{Line: first + i}
			if j > 0 {
				row.Runs = append(row.Runs, Deco(strings.Repeat(" ", indent), spaceview.Style{}))
			}
			labelEnd := min(r[1], len(field.label))
			if r[0] < labelEnd {
				row.Runs = append(row.Runs, Deco(field.text[r[0]:labelEnd], spaceview.Style{FG: theme.Dim}))
			}
			lo, hi := max(r[0], len(field.label)), max(r[1], len(field.label))
			start, end := field.cell[0]+lo-len(field.label), field.cell[0]+hi-len(field.label)
			row.Start, row.End = start, end
			row.Frags = [][2]int{{start, end}}
			for _, p := range dl.Pieces {
				a, b := max(start, p.Start), min(end, p.End)
				if a < b {
					row.Runs = append(row.Runs, Run{Text: dl.Text[a:b], Style: p.Style, Copy: true, Start: a, Act: p.Act, Strike: p.Strike})
				}
			}
			out[i] = append(out[i], row)
		}
	}
	return out
}

// FitTable retains the existing rows-only seam for callers that do not need a reason.
func FitTable(wm ansi.Method, lines []DisplayLine, first, width, rowsLeft int, theme spaceview.Theme) [][]BodyRow {
	rows, _ := FitTableLayout(wm, lines, first, width, rowsLeft, theme)
	return rows
}
