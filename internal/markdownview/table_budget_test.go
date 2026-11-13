package markdownview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The shortcut is only a necessary lower bound: every admission the existing
// renderer accepts must remain available, including requested open rows.
func TestTableBudgetBoundPreservesAdmission(t *testing.T) {
	sources := []string{
		wideWindowTable(),
		"|" + strings.Repeat("h", 2000) + "|b|\n|---|---|\n" + strings.Repeat("|x|y|\n", 80),
		"|header|aligned|last|\n|:---|:---:|---:|\n" + strings.Repeat("|  a  b  c  |中文 ▶️ é 👩‍💻|tail words|\n", 70),
		"|empty|spaces|end|\n|---|---|---|\n" + strings.Repeat("||x         y|z|\n", 100),
	}
	for n, source := range sources {
		for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			p := documentOptions()
			p.Method = wm
			doc := Parse(source, p)
			m := measureTableBudget(wm, doc.Lines)
			for _, compact := range []bool{false, true} {
				for width := 1; width <= 240; width++ {
					if m.couldFit(width, compact) {
						continue
					}
					for _, open := range []map[TableRowID]bool{nil, {{0, 0}: true, {0, 2}: true}} {
						var why TableFitReason
						applied := false
						if compact {
							_, why, _, applied = compactTable(wm, doc.Lines, 0, 0, width, 100000, p.Theme, open)
						}
						if !applied {
							_, why = FitTableLayout(wm, doc.Lines, 0, width, 100000, p.Theme)
						}
						if why == TableFits || why == TableLaidOut {
							t.Fatalf("source%d method%d compact%t width%d: shortcut rejected admitted table", n, wm, compact, width)
						}
					}
				}
			}
		}
	}
}

func TestSparseRetryRejectsImpossibleWidthsBeforeWrapping(t *testing.T) {
	p := documentOptions()
	doc := Parse("|"+strings.Repeat("h", 65536)+"|b|\n|---|---|\n"+strings.Repeat("|x|y|\n", 512), p)
	m := measureTableBudget(p.Method, doc.Lines)
	for _, width := range []int{5000, 6000, 8000} {
		if m.couldFit(width, true) {
			t.Fatalf("sparse width%d must be rejected without fragment allocation", width)
		}
	}
	opts := LayoutOptions{Method: p.Method, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
	rows, why, _ := fitTableInView(p.Method, doc.Lines, 0, 0, 8000, 100000, p.Theme, opts)
	if rows == nil || why != TableLaidOut {
		t.Fatal("shortcut lost the fitting narrower table", why)
	}
}

func BenchmarkSparseTableRetry(b *testing.B) {
	p := documentOptions()
	doc := Parse("|"+strings.Repeat("h", 65536)+"|b|\n|---|---|\n"+strings.Repeat("|x|y|\n", 512), p)
	opts := LayoutOptions{Method: p.Method, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
	for _, width := range []int{5000, 8000} {
		b.Run(fmt.Sprint(width), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				fitTableInView(p.Method, doc.Lines, 0, 0, width, 100000, p.Theme, opts)
			}
		})
	}
}
