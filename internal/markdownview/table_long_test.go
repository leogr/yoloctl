package markdownview

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func longProgress(t *testing.T, repeats int) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/table-shapes/progress.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	return strings.Join(lines[:2], "\n") + "\n" + strings.Repeat(strings.Join(lines[2:], "\n")+"\n", repeats)
}

func TestLongTablesStayWrappedAtTheWordFloor(t *testing.T) {
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, repeats := range []int{3, 8} {
			p := documentOptions()
			p.Method = wm
			doc := Parse(longProgress(t, repeats), p)
			tbl := doc.Lines[0].Table
			edge := 3 * (len(tbl.Natural) - 1)
			natural := edge
			for c, w := range tbl.WordFloor {
				edge += w
				natural += tbl.Natural[c]
			}
			o := LayoutOptions{Method: wm, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
			previous, previousClosed := 0, 1<<30
			for width := 8; width <= 180; width++ {
				rows := Layout(doc, width, o).Rows
				level, closed := 2, 0
				if strings.HasPrefix(tableRowsText(rows), "Table ") {
					level = 0
				} else {
					for _, row := range rows {
						if row.Closed {
							closed++
						}
						for _, run := range row.Runs {
							if run.Tail > 0 || run.Act.Kind == IntentToggleTableRow {
								level = 1
							}
						}
					}
				}
				if level < previous || level == 1 && previous == 1 && closed > previousClosed {
					t.Fatalf("long table method%d repeats%d width%d regressed", wm, repeats, width)
				}
				previous, previousClosed = level, closed
				if width < edge {
					continue
				}
				for _, r := range rows {
					if r.Closed {
						t.Fatalf("method%d repeats%d width%d unexpectedly closed", wm, repeats, width)
					}
				}
				if width < natural && tableRuleWidth(wm, rows) != width {
					t.Fatalf("repeats%d width%d table narrowed to%d", repeats, width, tableRuleWidth(wm, rows))
				}
			}
		}
	}
}

func tableRuleWidth(wm ansi.Method, rows []BodyRow) int {
	for _, row := range rows {
		if row.Decor && row.Line == 1 {
			return wm.StringWidth(rowText(row))
		}
	}
	return 0
}

func TestLongCollapsedTableCanOpenAllRows(t *testing.T) {
	p := documentOptions()
	doc := Parse(longProgress(t, 8), p)
	open := map[TableRowID]bool{}
	for _, record := range TableRecords(doc) {
		open[record.ID] = true
	}
	result := Layout(doc, 50, LayoutOptions{Method: p.Method, Theme: p.Theme, CompactTables: true, UnlimitedRows: true, OpenTableRows: open})
	if len(result.RefusedTableRows) > 0 {
		t.Fatal("ordinary long table expansion refused", len(result.RefusedTableRows))
	}
	for _, row := range result.Rows {
		if row.Closed {
			t.Fatal("ordinary requested row closed")
		}
	}
}

func TestTableBudgetFloorPreservesOldAllowanceAndNaturalGuard(t *testing.T) {
	for _, source := range []int{0, 64 << 10, 2 << 20, 4 << 20} {
		tbl := &TableInfo{Source: source, Natural: []int{100000, 1}}
		old := 2*source + TableSlack
		if got := tableLayoutBudget(tbl, 100); got != max(old, tableViewBytes) {
			t.Fatal("pane allowance", source, got)
		}
		if got := tableLayoutBudget(tbl, 200000); got != old {
			t.Fatal("natural padding guard changed", source, got)
		}
	}
}

func TestSparseWrappedAndExpandedTablesRemainBounded(t *testing.T) {
	var source strings.Builder
	source.WriteString("|" + strings.Repeat("header|", 32) + "\n|" + strings.Repeat("---|", 32) + "\n")
	for r := range 512 {
		source.WriteString("|")
		for c := range 32 {
			if c == r%32 {
				source.WriteString(strings.Repeat("word ", 800))
			}
			source.WriteString("|")
		}
		source.WriteString("\n")
	}
	if source.Len() > tableViewBytes {
		t.Fatal("sparse fixture exceeds existing whole-file input size")
	}
	p := documentOptions()
	doc := Parse(source.String(), p)
	if doc.Lines[0].Table == nil || !doc.Lines[0].Table.Unpadded {
		t.Fatal("sparse aligned guard lost")
	}
	rows, why := fitTableColumns(p.Method, doc.Lines, 0, 320, 1000000, p.Theme, doc.Lines[0].Table.WordFloor, false, false)
	if rows != nil || why != TableOverBudget {
		t.Fatal("sparse wrapped output allocated", why)
	}
	if measureTableBudget(p.Method, doc.Lines).couldFit(320, true) {
		t.Fatal("sparse impossible wrapping not cheaply rejected")
	}
	open := map[TableRowID]bool{}
	for row := range 32 {
		open[TableRowID{Table: 0, Row: row}] = true
	}
	compact, why, refused, _ := compactTable(p.Method, doc.Lines, 0, 0, 220, 1000000, p.Theme, open)
	if why != TableLaidOut || len(refused) == 0 {
		t.Fatal("sparse expansion guard lost", why, len(refused))
	}
	bytes := 0
	for _, line := range compact {
		for _, row := range line {
			for _, run := range row.Runs {
				bytes += len(run.Text)
			}
		}
	}
	rule := TableRowRule(p.Method, strings.TrimPrefix(rowText(compact[1][0]), "  "), 218, p.Theme)
	ruleBytes := 2
	for _, run := range rule {
		ruleBytes += len(run.Text)
	}
	bytes += ruleBytes * (len(doc.Lines) - 3)
	if bytes > tableLayoutBudget(doc.Lines[0].Table, 218) {
		t.Fatal("sparse output exceeds counted budget", bytes)
	}
}
