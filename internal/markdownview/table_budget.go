package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// A necessary cost bound, not another admission policy. Cache the cell scans
// once; impossible candidate widths then need no wrapping or fragment arrays.
type tableBudgetCell struct {
	width, bytes, minWidth, minBytes int
}
type tableBudgetMeasure struct {
	table *TableInfo
	rows  [][]tableBudgetCell // header, then body records; no delimiter row
}

func measureTableBudget(wm ansi.Method, lines []DisplayLine) tableBudgetMeasure {
	m := tableBudgetMeasure{table: lines[0].Table}
	for _, line := range lines {
		if line.Table != m.table {
			break
		}
		if line.Cells == nil {
			continue
		}
		row := make([]tableBudgetCell, len(line.Cells))
		for c, span := range line.Cells {
			text := line.Text[span[0]:span[1]]
			spaces := strings.Count(text, " ")
			width := wm.StringWidth(text)
			// DrawnEnd can omit hanging ASCII spaces, but no other cell bytes.
			row[c] = tableBudgetCell{width: width, bytes: len(text), minWidth: max(width-spaces, 0), minBytes: len(text) - spaces}
		}
		m.rows = append(m.rows, row)
	}
	return m
}
func (m tableBudgetMeasure) couldFit(width int, compact bool) bool {
	alloc, _ := compactColumns(m.table, width)
	if alloc == nil {
		return true
	} // The actual admission reports the geometry reason.
	clipped := false
	if compact {
		for _, row := range m.rows[1:] {
			for c, cell := range row {
				clipped = clipped || cell.width > alloc[c]
			}
		}
	}
	compact = compact && clipped
	if compact {
		alloc, _ = compactColumns(m.table, width-2)
		if alloc == nil {
			return true
		}
	}
	sum := 0
	for _, w := range alloc {
		sum += w
	}
	used := len("─")*sum + len("─┼─")*(len(alloc)-1) // header rule
	if compact {
		used += 2 // delimiter gutter
		// The compact admission counts body rules; FitTableLayout does not.
		used += (len("─")*sum + len(" │ ")*(len(alloc)-1) + 2) * max(len(m.rows)-2, 0)
	}
	budget := 2*m.table.Source + TableSlack
	for i, row := range m.rows {
		height := 1
		if !compact || i == 0 {
			for c, cell := range row {
				if alloc[c] > 0 {
					height = max(height, (cell.minWidth+alloc[c]-1)/alloc[c])
				}
			}
		}
		used += len(" │ ") * (len(alloc) - 1) * height
		if compact {
			used += 2 * height
		} // minimum gutter; a toggle glyph can cost more
		for c, cell := range row {
			bytes := cell.minBytes
			if compact && i > 0 {
				bytes = 0
				if cell.width <= alloc[c] {
					bytes = cell.bytes
				}
			}
			if c < len(alloc)-1 {
				// Every physical row fills a nonfinal column, even if its cell is empty.
				used += max(alloc[c]*height, bytes)
			} else {
				used += bytes
			}
		}
		if used > budget {
			return false
		}
	}
	return true
}
