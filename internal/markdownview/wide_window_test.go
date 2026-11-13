package markdownview

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// A short before/after table with a few long cells, as in img1/img2.
func wideWindowTable() string {
	s := "|Situation|Before|After|\n|---|---|---|\n"
	for i := range 14 {
		s += fmt.Sprintf("|case %d|%s|%s|\n", i, strings.Repeat("before ", i%3+1), strings.Repeat("after words ", i%4+2))
	}
	return s + "|box stop during start/restart/recreate/account/space op|waits|cancels it, then stops|\n"
}

func TestWideningTableKeepsAnAdmittedLayout(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, compact := range []bool{false, true} {
			t.Run(fmt.Sprintf("method%d/compact%t", method, compact), func(t *testing.T) {
				p := documentOptions()
				p.Method = method
				doc := Parse(wideWindowTable(), p)
				original := RenderText(doc)
				options := LayoutOptions{Method: method, Theme: p.Theme, CompactTables: compact, UnlimitedRows: true}
				admitted := false
				for width := 40; width <= 200; width++ {
					rows := Layout(doc, width, options).Rows
					labelled := strings.HasPrefix(tableRowsText(rows), "Table ")
					if admitted && labelled {
						t.Fatalf("widening to%d lost the table", width)
					}
					if !labelled {
						admitted = true
					}
					if !labelled {
						for _, row := range rows {
							painted := Paint(method, row.Runs, width, 0, nil, spaceview.Style{})
							screen := uv.NewWindow(width+2, 1, method)
							uv.NewStyledString(painted+"\x1b[0m§").Draw(screen, screen.Bounds())
							if cell := screen.CellAt(width, 0); cell == nil || cell.Content != "§" {
								t.Fatalf("width%d overflow", width)
							}
						}
					}
				}
				if !admitted {
					t.Fatal("table never admitted")
				}
				if RenderText(doc) != original {
					t.Fatal("resize changed logical text")
				}
				if got := TableText(doc.Lines, 2, 2, 0, len(doc.Lines[2].Text)); len(got) != 1 || got[0].Text != "case 0\tbefore\tafter words after words" {
					t.Fatal("logical cells/Quote changed", got)
				}
			})
		}
	}
}

func TestWideBudgetRetryUsesWidestAdmittedAllocation(t *testing.T) {
	p := documentOptions()
	doc := Parse(wideWindowTable(), p)
	options := LayoutOptions{Method: p.Method, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
	_, reason, _, _ := compactTable(p.Method, doc.Lines, 0, 0, 110, 10000, p.Theme, nil)
	if reason != TableOverBudget {
		t.Fatal("fixture no longer crosses the budget at110", reason)
	}
	rows, reason, _ := fitTableInView(p.Method, doc.Lines, 0, 0, 110, 10000, p.Theme, options)
	if rows == nil || reason != TableLaidOut {
		t.Fatal("did not retain a table", reason)
	}
	used := 0
	for _, row := range Layout(doc, 110, options).Rows {
		for _, run := range row.Runs {
			used += len(run.Text)
		}
	}
	if used > 2*doc.Lines[0].Table.Source+TableSlack {
		t.Fatal("retry bypassed byte budget", used)
	}
	actual := 0
	for _, run := range rows[1][0].Runs {
		actual += p.Method.StringWidth(run.Text)
	}
	if actual >= 110 {
		t.Fatal("retry did not tighten the allocation", actual)
	}
	for width := actual + 1; width <= 110; width++ {
		_, why, _, _ := compactTable(p.Method, doc.Lines, 0, 0, width, 10000, p.Theme, nil)
		if why == TableLaidOut || why == TableFits {
			t.Fatalf("skipped wider admitted allocation%d", width)
		}
	}
}

func TestWideRetryMatchesNarrowAdmission(t *testing.T) {
	p := documentOptions()
	source := "|" + strings.Repeat("h", 2000) + "|b|\n|---|---|\n" + strings.Repeat("|x|y|\n", 512)
	doc := Parse(source, p)
	options := LayoutOptions{Method: p.Method, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
	for _, width := range []int{20, 21, 22, 40, 60, 80} {
		rows := Layout(doc, width, options).Rows
		if strings.HasPrefix(tableRowsText(rows), "Table ") {
			t.Fatalf("width%d lost the narrower admitted layout", width)
		}
	}
}
