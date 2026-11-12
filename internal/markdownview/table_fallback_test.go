package markdownview

import (
	"os"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func tableRowsText(rows []BodyRow) string {
	var b strings.Builder
	for _, r := range rows {
		for _, p := range r.Runs {
			b.WriteString(p.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestTableReadabilityOnlyAppliesToSqueezedColumns(t *testing.T) {
	o := documentOptions()
	doc := Parse("| Name |\n|---|\n|abcdefghijklmnopqrst|", o)
	for _, tc := range []struct {
		width  int
		reason TableFitReason
	}{{20, TableFits}, {16, TableLaidOut}, {15, TableTooNarrow}} {
		_, reason := FitTableLayout(o.Method, doc.Lines, 0, tc.width, 100, o.Theme)
		if reason != tc.reason {
			t.Fatalf("width%d reason=%v want%v", tc.width, reason, tc.reason)
		}
	}
	small := Parse("|a|b|c|d|e|f|g|h|\n|---|---|---|---|---|---|---|---|\n|1|2|3|4|5|6|7|8|", o)
	_, reason := FitTableLayout(o.Method, small.Lines, 0, 60, 100, o.Theme)
	if reason != TableFits {
		t.Fatal("naturally short columns must not trigger cutoff", reason)
	}
}

func TestLabelledTablePreservesLogicalCopyAndQuote(t *testing.T) {
	o := documentOptions()
	doc := Parse("| Field | Detail |\n|---|---|\n| **alpha** | "+strings.Repeat("detail ", 12)+"|\n|beta|last|", o)
	before := RenderText(doc)
	rows := Layout(doc, 23, LayoutOptions{Method: o.Method, Theme: o.Theme}).Rows
	text := tableRowsText(rows)
	if rows[0].Line != 2 {
		t.Fatal("notice anchors to an invisible header")
	}
	if !strings.Contains(text, "Table too narrow") || !strings.Contains(text, "Field: alpha") {
		t.Fatal(text)
	}
	for _, row := range rows {
		for _, r := range row.Runs {
			if r.Copy && strings.Contains(r.Text, "Field:") {
				t.Fatal("label copied")
			}
		}
	}
	if RenderText(doc) != before {
		t.Fatal("layout rewrote logical text")
	}
	quotes := TableText(doc.Lines, 2, 2, 0, len(doc.Lines[2].Text))
	if len(quotes) != 1 || quotes[0].Text != "alpha\t"+strings.TrimSpace(strings.Repeat("detail ", 12)) {
		t.Fatalf("quote=%+v", quotes)
	}
	cr := doc.Lines[2].Cells[0]
	if got := doc.Lines[2].CopyText(cr[0], cr[1]); got != "alpha" {
		t.Fatal(got)
	}
}

func TestLabelledTableCountsHeadersAndAllRowsBeforeEmission(t *testing.T) {
	o := documentOptions()
	source := "|" + strings.Repeat("h", 2000) + "|b|\n|---|---|\n" + strings.Repeat("|x|y|\n", 512)
	doc := Parse(source, o)
	if doc.Lines[0].Table == nil || !doc.Lines[0].Table.Unpadded {
		t.Fatal("fixture must retain unpadded cells")
	}
	// Even geometric fit must not rebuild the rejected padded representation.
	_, reason := FitTableLayout(o.Method, doc.Lines, 0, 4000, 10000, o.Theme)
	if reason != TableOverBudget {
		t.Fatal("natural fit bypassed byte limit", reason)
	}
	if labelledTable(o.Method, doc.Lines, 0, 60, 10000, o.Theme, reason) != nil {
		t.Fatal("repeated labels exceeded original budget")
	}
	rows := Layout(doc, 60, LayoutOptions{Method: o.Method, Theme: o.Theme, UnlimitedRows: true}).Rows
	if !strings.Contains(tableRowsText(rows[:1]), "display limit") {
		t.Fatal("missing refusal notice")
	}
	for _, dl := range doc.Lines {
		if len(dl.Text) > 2100 {
			t.Fatal("rejected padding rebuilt")
		}
	}
	small := Parse("|Header|Other|\n|---|---|\n|"+strings.Repeat("words ", 15)+"|value|", o)
	fits := labelledTable(o.Method, small.Lines, 0, 24, 10000, o.Theme, TableTooNarrow)
	if fits == nil {
		t.Fatal("ordinary labelled table refused")
	}
	count, bytes := 0, 0
	for _, rs := range fits {
		for _, r := range rs {
			count++
			bytes++
			for _, p := range r.Runs {
				bytes += len(p.Text)
			}
		}
	}
	if bytes > 2*small.Lines[0].Table.Source+TableSlack {
		t.Fatal("label bytes escaped budget")
	}
	if labelledTable(o.Method, small.Lines, 0, 24, count, o.Theme, TableTooNarrow) != nil {
		t.Fatal("row allowance not reserved")
	}
}

func TestRejectedTableCountsGiveOneNoticeAndKeepSource(t *testing.T) {
	o := documentOptions()
	for _, source := range []string{
		"|" + strings.Repeat("c|", 33) + "\n|" + strings.Repeat("---|", 33),
		"|a|\n|---|\n" + strings.Repeat("|x|\n", 513),
	} {
		doc := Parse(source, o)
		text := tableRowsText(Layout(doc, 60, LayoutOptions{Method: o.Method, Theme: o.Theme, UnlimitedRows: true}).Rows)
		if strings.Count(text, "Table over the display limit") != 1 {
			t.Fatalf("notice missing/repeated: %.100s", text)
		}
		o.Source = true
		if got := RenderText(Parse(source, o)); strings.TrimSuffix(got, "\n") != strings.TrimSuffix(source, "\n") {
			t.Fatal("source changed")
		}
		o.Source = false
	}
}

func TestScreenshotDerivedTableAtMatchedWidths(t *testing.T) {
	raw, err := os.ReadFile("testdata/long-table.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		o := documentOptions()
		o.Method = method
		doc := Parse(string(raw), o)
		if doc.Lines[0].Table == nil {
			t.Fatal("budget-rejected table lost structured cells")
		}
		for _, width := range []int{60, 92, 100} {
			result := Layout(doc, width, LayoutOptions{Method: method, Theme: o.Theme, UnlimitedRows: true})
			if result.Limited {
				t.Fatal("follow unlimited rows hit chat cap")
			}
			if strings.Contains(tableRowsText(result.Rows), "|---") {
				t.Fatal("fell back to unexplained raw pipes")
			}
			for _, r := range result.Rows {
				painted := Paint(method, r.Runs, width, 0, nil, spaceview.Style{})
				screen := uv.NewWindow(width+4, 1, method)
				uv.NewStyledString(painted+"\x1b[0m§").Draw(screen, screen.Bounds())
				if c := screen.CellAt(width, 0); c == nil || c.Content != "§" {
					t.Fatalf("method%v width%d overflow", method, width)
				}
			}
		}
	}
}

func TestHeaderOnlyTablesKeepTheirTextAtEveryLayout(t *testing.T) {
	source := "| alphaheader | betaheader |\n|---|---|"
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		o := documentOptions()
		o.Method = method
		doc := Parse(source, o)
		before := RenderText(doc)
		for _, width := range []int{16, 40, 100} {
			rows := Layout(doc, width, LayoutOptions{Method: method, Theme: o.Theme}).Rows
			var copied strings.Builder
			for _, r := range rows {
				for _, p := range r.Runs {
					if p.Copy {
						copied.WriteString(p.Text)
					}
				}
			}
			if got := copied.String(); !strings.Contains(got, "alphaheader") || !strings.Contains(got, "betaheader") {
				t.Fatalf("method%v width%d lost header: %q", method, width, got)
			}
			if RenderText(doc) != before {
				t.Fatal("logical header changed")
			}
			quoted := TableText(doc.Lines, 0, 0, 0, len(doc.Lines[0].Text))
			if len(quoted) != 1 || quoted[0].Text != "alphaheader\tbetaheader" {
				t.Fatalf("header quote=%+v", quoted)
			}
		}
		o.Source = true
		if got := RenderText(Parse(source, o)); got != source {
			t.Fatal("source changed", got)
		}
	}
}
