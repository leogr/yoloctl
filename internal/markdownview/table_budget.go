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
	tbl := m.table
	gutter := 0
	alloc, _ := WaterFill(tbl.Natural, tbl.Floor, width-3*(len(tbl.Natural)-1))
	if compact {
		alloc, _ = WaterFill(tbl.Natural, tbl.WordFloor, width-3*(len(tbl.Natural)-1))
		if alloc != nil {
			compact = false // A has no control gutter or closed rows.
		} else {
			alloc, _ = compactColumns(tbl, width)
			if alloc != nil {
				for _, row := range m.rows[1:] {
					for c, cell := range row {
						if cell.width > alloc[c] {
							gutter = 2
						}
					}
				}
			}
			if gutter > 0 {
				alloc, _ = compactColumns(tbl, width-gutter)
			}
		}
	}
	if alloc == nil {
		return true
	} // Exact admission reports the geometry reason.

	sum := 0
	for _, w := range alloc {
		sum += w
	}
	used := len("─")*sum + len("─┼─")*(len(alloc)-1) // header rule
	if compact {
		used += gutter // delimiter gutter
		// The compact admission counts body rules; FitTableLayout does not.
		used += (len("─")*sum + len(" │ ")*(len(alloc)-1) + gutter) * max(len(m.rows)-2, 0)
	}
	budget := 2*m.table.Source + TableSlack
	for i, row := range m.rows {
		height := 1
		if !compact {
			for c, cell := range row {
				if alloc[c] > 0 {
					height = max(height, (cell.minWidth+alloc[c]-1)/alloc[c])
				}
			}
		}
		used += len(" │ ") * (len(alloc) - 1) * height
		if compact {
			used += gutter * height
		} // minimum gutter; a toggle glyph can cost more
		for c, cell := range row {
			bytes := cell.minBytes
			if compact {
				bytes = 0
				if i > 0 && cell.width <= alloc[c] {
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
