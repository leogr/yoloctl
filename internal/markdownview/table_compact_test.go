package markdownview

import (
	"reflect"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func compactFixture(t *testing.T, method ansi.Method) (Document, LayoutOptions) {
	t.Helper()
	p := documentOptions()
	p.Method = method
	doc := Parse("|Name|Detail|\n|---|---|\n|alpha|"+(strings.Repeat("long ▶️ words ", 30)+strings.Repeat("x", 200))+"|\n|beta|"+(strings.Repeat("second field ", 20)+strings.Repeat("x", 200))+"|", p)
	return doc, LayoutOptions{Method: method, Theme: p.Theme, CompactTables: true}
}
func countLine(rows []BodyRow, line int) int {
	n := 0
	for _, r := range rows {
		if r.Line == line && !r.Decor {
			n++
		}
	}
	return n
}

func TestCompactRowsCloseByDefaultAndExpandWithoutChangingData(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		doc, o := compactFixture(t, method)
		original := RenderText(doc)
		closed := Layout(doc, 60, o)
		markers := 0
		for _, r := range closed.Rows {
			for _, run := range r.Runs {
				if run.Act.Kind == IntentToggleTableRow {
					markers++
					if run.Text != "▸ " || run.Copy {
						t.Fatal("invalid marker", run)
					}
				}
			}
		}
		if markers != 2 || countLine(closed.Rows, 2) != 1 || countLine(closed.Rows, 3) != 1 {
			t.Fatal("rows not compact by default", markers)
		}
		o.OpenTableRows = map[TableRowID]bool{{Table: 0, Row: 0}: true}
		open := Layout(doc, 60, o)
		if len(open.RefusedTableRows) != 0 || countLine(open.Rows, 2) <= 1 || countLine(open.Rows, 3) != 1 {
			t.Fatal("per-row expansion failed")
		}
		if RenderText(doc) != original {
			t.Fatal("expansion changed logical/source text")
		}
		for _, result := range []LayoutResult{closed, open} {
			for _, r := range result.Rows {
				painted := Paint(method, r.Runs, 60, 0, nil, spaceview.Style{})
				screen := uv.NewWindow(68, 1, method)
				uv.NewStyledString(painted+"\x1b[0m§").Draw(screen, screen.Bounds())
				if c := screen.CellAt(60, 0); c == nil || c.Content != "§" {
					t.Fatalf("method%v overflowing compact row", method)
				}
			}
		}
		o.OpenTableRows = nil
		if !reflect.DeepEqual(Layout(doc, 60, o).Rows, closed.Rows) {
			t.Fatal("close/open round trip changed rows")
		}
	}
}

func TestCompactCellSelectionIncludesHiddenTailAndWholeCrossedCells(t *testing.T) {
	doc, o := compactFixture(t, ansi.GraphemeWidth)
	result := Layout(doc, 60, o)
	closed := map[int]bool{}
	for _, r := range result.Rows {
		if r.Closed {
			closed[r.Line] = true
		}
		if r.Line != 2 || r.Decor {
			continue
		}
		x := 0
		found := false
		for _, run := range r.Runs {
			if run.Tail > 0 {
				lo, hi := HitRun(o.Method, r.Runs, x, r.End)
				if lo != doc.Lines[2].Cells[1][1] || hi != lo || run.Copy || run.Text != "…" {
					t.Fatal("ellipsis lost full logical end", run, lo, hi)
				}
				found = true
			}
			x += o.Method.StringWidth(run.Text)
		}
		if !found {
			t.Fatal("no clipped tail")
		}
	}
	a, b := doc.Lines[2].Cells[0], doc.Lines[2].Cells[1]
	lo, hi := CompactSelection(doc.Lines, closed, 2, b[0]+1, 2, b[0]+4, false)
	if lo != b[0]+1 || hi != b[0]+4 {
		t.Fatal("partial within-cell selection widened")
	}
	lo, hi = CompactSelection(doc.Lines, closed, 2, a[0]+1, 2, b[0]+2, false)
	if lo != a[0] || hi != b[1] {
		t.Fatal("crossed cells lost hidden text")
	}
	lo, hi = CompactSelection(doc.Lines, closed, 2, b[0]+3, 3, doc.Lines[3].Cells[0][0]+2, false)
	if lo != b[0] || hi != doc.Lines[3].Cells[0][1] {
		t.Fatal("cross-record endpoints not whole")
	}
	quoted := TableText(doc.Lines, 2, 2, a[0], b[1])
	if len(quoted) != 1 || strings.Contains(quoted[0].Text, "…") || !strings.Contains(quoted[0].Text, "\t") {
		t.Fatal("control leaked into Quote", quoted)
	}
}

func TestCompactExpansionRespectsRemainingRowsAndBytes(t *testing.T) {
	doc, o := compactFixture(t, ansi.GraphemeWidth)
	rows, reason, refused, applied := compactTable(o.Method, doc.Lines, 0, 0, 60, 8, o.Theme, map[TableRowID]bool{{0, 0}: true})
	if !applied || reason != TableLaidOut || len(refused) != 1 || countLine(flattenTable(rows), 2) != 1 {
		t.Fatal("expansion escaped remaining-row bound", reason, refused)
	}
	source := "|heading|" + strings.Repeat("heading|", 31) + "\n|" + strings.Repeat("---|", 32) + "\n|" + (strings.Repeat("w", 200) + " " + strings.Repeat("word ", 8000)) + "|" + strings.Repeat("x|", 31)
	d := Parse(source, documentOptions())
	if d.Lines[0].Table == nil {
		t.Fatal("fixture must be admitted")
	}
	_, reason, refused, applied = compactTable(o.Method, d.Lines, 0, 0, 170, 100000, o.Theme, map[TableRowID]bool{{0, 0}: true})
	if !applied || reason != TableLaidOut || len(refused) != 1 {
		t.Fatal("byte-bound expansion was not refused", reason, refused)
	}
}
func flattenTable(table [][]BodyRow) []BodyRow {
	var rows []BodyRow
	for _, rs := range table {
		rows = append(rows, rs...)
	}
	return rows
}

func TestTableRecordKeysDisambiguateDuplicatesAndColumns(t *testing.T) {
	o := documentOptions()
	doc := Parse("|k|v|\n|---|---|\n|same|a|\n|same|b|", o)
	records := TableRecords(doc)
	if len(records) != 2 || records[0].Occurrence != 0 || records[1].Occurrence != 1 || records[1].Columns != 2 || records[1].ID != (TableRowID{0, 1}) {
		t.Fatal(records)
	}
	for _, r := range records {
		id, ok := ParseTableRowID(r.ID.String())
		if !ok || id != r.ID {
			t.Fatal("key round trip")
		}
	}
	o.Source = true
	if len(TableRecords(Parse("|k|v|\n|---|---|\n|same|a|", o))) != 0 {
		t.Fatal("source text gained table controls")
	}
}
