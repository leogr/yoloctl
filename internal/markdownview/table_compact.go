package markdownview

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// TableRowID is local to one document. Views own message/file identity.
type TableRowID struct{ Table, Row int }

func (id TableRowID) String() string { return fmt.Sprintf("%d:%d", id.Table, id.Row) }
func ParseTableRowID(s string) (TableRowID, bool) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return TableRowID{}, false
	}
	table, e1 := strconv.Atoi(a)
	row, e2 := strconv.Atoi(b)
	return TableRowID{table, row}, e1 == nil && e2 == nil && table >= 0 && row >= 0
}

// TableRecord also supplies follow's stable refresh identity. Occurrence counts
// equal first-cell texts within the same table, including currently closed rows.
type TableRecord struct {
	ID                        TableRowID
	Line, Columns, Occurrence int
	First                     string
}

func TableRecords(doc Document) []TableRecord {
	var out []TableRecord
	table, first := -1, 0
	var previous *TableInfo
	var counts map[string]int
	for n, dl := range doc.Lines {
		if dl.Table == nil {
			previous = nil
			continue
		}
		if dl.Table != previous {
			table++
			first = n
			previous = dl.Table
			counts = map[string]int{}
		}
		if n < first+2 || dl.Cells == nil {
			continue
		}
		text := ""
		if len(dl.Cells) > 0 {
			r := dl.Cells[0]
			text = dl.Text[r[0]:r[1]]
		}
		out = append(out, TableRecord{ID: TableRowID{table, n - first - 2}, Line: n, Columns: len(dl.Cells), First: text, Occurrence: counts[text]})
		counts[text]++
	}
	return out
}

// CompactSelection keeps partial within-cell drags, but includes full reached
// cells when crossing compact fields/records. Ellipsis hits already name the
// full cell end; decorative bytes never enter the logical selection.
func CompactSelection(lines []DisplayLine, closed map[int]bool, from, lo, to, hi int, crossRecords bool) (int, int) {
	if from < 0 || to >= len(lines) || from > to {
		return lo, hi
	}
	reached := func(dl DisplayLine, a, b int) (int, int) {
		first, last := -1, -1
		for c, r := range dl.Cells {
			if max(a, r[0]) < min(b, r[1]) {
				if first < 0 {
					first = c
				}
				last = c
			}
		}
		return first, last
	}
	if from == to {
		if closed[from] {
			a, b := reached(lines[from], lo, hi)
			if a >= 0 && (a != b || crossRecords) {
				return lines[from].Cells[a][0], lines[from].Cells[b][1]
			}
		}
		return lo, hi
	}
	if closed[from] {
		a, _ := reached(lines[from], lo, len(lines[from].Text))
		if a >= 0 {
			lo = lines[from].Cells[a][0]
		}
	}
	if closed[to] {
		_, b := reached(lines[to], 0, hi)
		if b >= 0 {
			hi = lines[to].Cells[b][1]
		}
	}
	return lo, hi
}

func compactColumns(tbl *TableInfo, width int) ([]int, TableFitReason) {
	alloc, ok := WaterFill(tbl.Natural, tbl.CompactFloor, width-3*(len(tbl.Natural)-1))
	if !ok {
		return nil, TableTooNarrow
	}
	return alloc, TableLaidOut
}
func rowClipped(wm ansi.Method, dl DisplayLine, alloc []int) bool {
	for c, r := range dl.Cells {
		if wm.StringWidth(dl.Text[r[0]:r[1]]) > alloc[c] {
			return true
		}
	}
	return false
}

// compactRecord has a count-only pass: padding and output runs are built only
// after the complete closed table fits its byte and physical-row allowances.
func compactRecord(wm ansi.Method, dl DisplayLine, line int, alloc []int, theme spaceview.Theme, build bool) (BodyRow, int) {
	row := BodyRow{Line: line, Start: len(dl.Text), End: len(dl.Text), Closed: true}
	used := 2 // blank gutter; a UTF-8 toggle glyph is counted below
	hasClip := false
	frame := spaceview.Style{FG: theme.Frame}
	for c, cr := range dl.Cells {
		if c > 0 {
			used += len(" │ ")
			if build {
				row.Runs = append(row.Runs, Deco(" │ ", frame))
			}
		}
		text := dl.Text[cr[0]:cr[1]]
		cut := wm.StringWidth(text) > alloc[c]
		hasClip = hasClip || cut
		end := len(text)
		if cut {
			first := WrapRanges(wm, text, alloc[c], true)[0]
			end = DrawnEnd(wm, text, first, alloc[c])
			end = len(Truncate(wm, text[:end], max(alloc[c]-1, 0), ""))
		}
		shown := wm.StringWidth(text[:end])
		if cut {
			shown++
		}
		left, right := CellGaps(dl.Table.Align[c], alloc[c]-shown, c == len(alloc)-1)
		used += left + right + end
		if cut {
			used += len("…")
		}
		if !build {
			continue
		}
		if left > 0 {
			row.Runs = append(row.Runs, Deco(strings.Repeat(" ", left), spaceview.Style{}))
		}
		for _, p := range dl.Pieces {
			a, b := max(cr[0], p.Start), min(cr[0]+end, p.End)
			if a < b {
				row.Runs = append(row.Runs, Run{Text: dl.Text[a:b], Style: p.Style, Copy: true, Start: a, Act: p.Act, Strike: p.Strike})
			}
		}
		if cut {
			row.Runs = append(row.Runs, Run{Text: "…", Style: spaceview.Style{FG: theme.Dim}, Tail: cr[1]})
		}
		if right > 0 {
			row.Runs = append(row.Runs, Deco(strings.Repeat(" ", right), spaceview.Style{}))
		}
		row.Frags = append(row.Frags, cr)
		row.Start = min(row.Start, cr[0])
	}
	if hasClip {
		used += len("▸ ") - 2
	}
	return row, used
}
func tableCost(wm ansi.Method, rows [][]BodyRow, width int, theme spaceview.Theme) (int, int) {
	bytes, count := 0, 0
	for _, rs := range rows {
		for _, r := range rs {
			count++
			for _, p := range r.Runs {
				bytes += len(p.Text)
			}
		}
	}
	if len(rows) > 3 {
		rule := TableRowRule(wm, rows[1][0].Runs[0].Text, width, theme)
		for _, p := range rule {
			bytes += len(p.Text) * (len(rows) - 3)
		}
		count += len(rows) - 3
	}
	return bytes, count
}

func compactTable(wm ansi.Method, lines []DisplayLine, first, table, width, rowsLeft int, theme spaceview.Theme, open map[TableRowID]bool) ([][]BodyRow, TableFitReason, []TableRowID, bool) {
	tbl := lines[0].Table
	// A uses the whole width without a control gutter. Saved B choices are
	// dormant here; no table-row actions are emitted.
	if _, ok := WaterFill(tbl.Natural, tbl.WordFloor, width-3*(len(tbl.Natural)-1)); ok {
		rows, why := fitTableColumns(wm, lines, first, width, rowsLeft, theme, tbl.WordFloor, false, false)
		return rows, why, nil, true
	}
	end := 1
	for end < len(lines) && lines[end].Table == tbl {
		end++
	}
	lines = lines[:end]
	alloc, reason := compactColumns(tbl, width)
	if alloc == nil {
		return nil, reason, nil, true
	}
	gutter := 0
	for _, line := range lines[2:] {
		if rowClipped(wm, line, alloc) {
			gutter = 2
			break
		}
	}
	if gutter > 0 {
		alloc, reason = compactColumns(tbl, width-gutter)
	}
	if alloc == nil {
		return nil, reason, nil, true
	}
	header, reason := fitTableColumns(wm, lines[:2], first, width-gutter, rowsLeft, theme, tbl.CompactFloor, false, true)
	if header == nil {
		return nil, reason, nil, true
	}
	headerRule := header[1][0].Runs[0].Text
	for i := range header {
		for j := range header[i] {
			if gutter > 0 {
				header[i][j].Runs = append([]Run{Deco("  ", spaceview.Style{})}, header[i][j].Runs...)
			}
		}
	}
	// Count all closed rows before building their padding. Header/rule output
	// is already bounded by FitTableLayout; body rules are shared decorations.
	base := make([][]BodyRow, len(lines))
	copy(base, header)
	bytes, count := tableCost(wm, header, width-gutter, theme)
	for _, dl := range lines[2:] {
		_, n := compactRecord(wm, dl, 0, alloc, theme, false)
		bytes += n - 2 + gutter
		count++
	}
	rule := TableRowRule(wm, headerRule, width-gutter, theme)
	for _, p := range rule {
		bytes += (len(p.Text) + gutter) * max(len(lines)-3, 0)
	}
	count += max(len(lines)-3, 0)
	budget := 2*tbl.Source + TableSlack
	if bytes > budget {
		return nil, TableOverBudget, nil, true
	}
	if count >= rowsLeft {
		return nil, TableNoRows, nil, true
	}
	var refused []TableRowID
	for i, dl := range lines[2:] {
		index := i + 2
		id := TableRowID{table, i}
		clipped := rowClipped(wm, dl, alloc)
		row, _ := compactRecord(wm, dl, first+index, alloc, theme, true)
		row.Closed = clipped
		marker := "  "
		act := Action{}
		if clipped {
			marker = "▸ "
			act = Action{Kind: IntentToggleTableRow, Value: id.String()}
		}
		if gutter > 0 {
			row.Runs = append([]Run{{Text: marker, Style: spaceview.Style{FG: theme.Label}, Act: act}}, row.Runs...)
		}
		base[index] = []BodyRow{row}
		if !clipped || !open[id] {
			continue
		}
		subset := []DisplayLine{lines[0], lines[1], dl}
		fitted, why := fitTableColumns(wm, subset, first, width-gutter, rowsLeft, theme, tbl.CompactFloor, false, true)
		if fitted == nil || why != TableLaidOut {
			refused = append(refused, id)
			continue
		}
		expanded := fitted[2]
		newBytes, oldBytes := 0, 0
		for _, p := range row.Runs {
			oldBytes += len(p.Text)
		}
		for j, r := range expanded {
			newBytes += 2
			if j == 0 {
				newBytes += len("▾ ") - 2
			}
			for _, p := range r.Runs {
				newBytes += len(p.Text)
			}
		}
		if bytes+newBytes-oldBytes > budget || count+len(expanded)-1 >= rowsLeft {
			refused = append(refused, id)
			continue
		}
		bytes += newBytes - oldBytes
		count += len(expanded) - 1
		for j := range expanded {
			text := "  "
			action := Action{}
			if j == 0 {
				text = "▾ "
				action = act
			}
			expanded[j].Line = first + index
			expanded[j].Runs = append([]Run{{Text: text, Style: spaceview.Style{FG: theme.Label}, Act: action}}, expanded[j].Runs...)
		}
		base[index] = expanded
	}
	return base, TableLaidOut, refused, true
}
