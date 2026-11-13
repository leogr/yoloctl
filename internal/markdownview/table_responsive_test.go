package markdownview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrappedTableHasNoGutterOrControls(t *testing.T) {
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		p := documentOptions()
		p.Method = wm
		doc := Parse("|Subject|Description|\n|---|---|\n|one|alpha beta gamma delta|\n|two|short|\n", p)
		opts := LayoutOptions{Method: wm, Theme: p.Theme, CompactTables: true, UnlimitedRows: true, OpenTableRows: map[TableRowID]bool{{0, 0}: true}}
		rows := Layout(doc, 22, opts).Rows
		multi := 0
		for _, r := range rows {
			if r.Line == 2 {
				multi++
			}
			if r.Closed {
				t.Fatal("A row closed")
			}
			for _, run := range r.Runs {
				if run.Act.Kind == IntentToggleTableRow {
					t.Fatal("A has arrow action")
				}
			}
		}
		if multi < 2 {
			t.Fatal("A did not wrap")
		}
		if strings.HasPrefix(tableRowsText(rows), "  ") {
			t.Fatal("A reserved gutter")
		}
	}
}

func TestResponsiveTableHeadersAndDerivedFloor(t *testing.T) {
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		p := documentOptions()
		p.Method = wm
		for _, text := range []string{"longheader", "中文標題", "▶️▶️title", "éclair"} {
			doc := Parse("|"+text+"|Value|\n|---|---|\n|a|bbbbbbbbbbbbbbbbbbbb|\n", p)
			want := min(doc.Lines[0].Table.Natural[0], WidestGrapheme(wm, text)+wm.StringWidth("…"))
			if doc.Lines[0].Table.CompactFloor[0] != want {
				t.Fatal("derived floor", text, want)
			}
			o := LayoutOptions{Method: wm, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
			minWidth := 0
			for _, w := range doc.Lines[0].Table.CompactFloor {
				minWidth += w
			}
			minWidth += 3 + 2 // separator and live B arrow
			for _, width := range []int{minWidth - 1, minWidth, minWidth + 2} {
				rows := Layout(doc, width, o).Rows
				labelled := strings.HasPrefix(tableRowsText(rows), "Table ")
				if labelled != (width < minWidth) {
					t.Fatalf("%q width%d labelled%t edge%d", text, width, labelled, minWidth)
				}
				if labelled {
					continue
				}
				alloc, _ := compactColumns(doc.Lines[0].Table, width-2)
				if tableRunWidth(wm, text) <= alloc[0] {
					continue
				} // CJK/emoji runs may wrap whole.
				for _, row := range rows {
					if row.Line != 0 || row.Decor {
						continue
					}
					if countLine(rows, 0) != 1 {
						t.Fatal("overwide header word split without ellipsis", text)
					}
					found := false
					for _, run := range row.Runs {
						if run.Text == "…" && run.Tail > 0 {
							found = true
						}
					}
					if !found {
						t.Fatal("header not truncated", text, tableRowsText(rows))
					}
				}
			}
		}
	}
}

func TestResponsiveSixTableWidthProperty(t *testing.T) {
	files, err := os.ReadDir("testdata/table-shapes")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			source, err := os.ReadFile(filepath.Join("testdata/table-shapes", file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			p := documentOptions()
			p.Method = wm
			doc := Parse(string(source), p)
			o := LayoutOptions{Method: wm, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
			previous, previousClosed := 0, 100000
			for width := 8; width <= 180; width++ {
				rows := Layout(doc, width, o).Rows
				level := 2
				closed := 0
				if strings.HasPrefix(tableRowsText(rows), "Table ") {
					level = 0
				} else {
					for _, row := range rows {
						if row.Closed {
							closed++
						}
						for _, run := range row.Runs {
							if run.Act.Kind == IntentToggleTableRow || run.Tail > 0 {
								level = 1
							}
						}
					}
				}
				if level < previous || level == 1 && previous == 1 && closed > previousClosed {
					t.Fatalf("%s method%d width%d level%d->%d closed%d->%d", file.Name(), wm, width, previous, level, previousClosed, closed)
				}
				for _, row := range rows {
					if level > 0 && wm.StringWidth(strings.TrimRight(rowText(row), " ")) > width {
						t.Fatalf("%s width%d overflow", file.Name(), width)
					}
				}
				previous, previousClosed = level, closed
			}
		}
	}
}

func rowText(row BodyRow) string {
	var s strings.Builder
	for _, run := range row.Runs {
		s.WriteString(run.Text)
	}
	return s.String()
}

// Contract4's concrete header edge: whole-word wrapping at50, one visibly
// truncated line at30. Selecting a cut label through its ellipsis reaches
// the original cell end, so both views copy the complete header.
func TestProgressHeaderThirtyFiftyAndWholeCopy(t *testing.T) {
	source, err := os.ReadFile("testdata/table-shapes/progress.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		p := documentOptions()
		p.Method = wm
		doc := Parse(string(source), p)
		o := LayoutOptions{Method: wm, Theme: p.Theme, CompactTables: true, UnlimitedRows: true}
		for _, width := range []int{30, 50} {
			rows := Layout(doc, width, o).Rows
			want := 1
			if width == 50 {
				want = 2
			}
			if got := countLine(rows, 0); got != want {
				t.Fatalf("method%d width%d header rows%d want%d", wm, width, got, want)
			}
			if width == 50 {
				continue
			}
			header := rows[0]
			x := 0
			copied := 0
			for _, run := range header.Runs {
				if run.Tail > 0 {
					_, end := HitRun(wm, header.Runs, x, header.End)
					for _, cell := range doc.Lines[0].Cells {
						if cell[1] == run.Tail {
							if got := doc.Lines[0].CopyText(cell[0], end); got != doc.Lines[0].Text[cell[0]:cell[1]] {
								t.Fatalf("cut header copy=%q", got)
							}
							copied++
						}
					}
				}
				x += wm.StringWidth(run.Text)
			}
			if copied != 5 {
				t.Fatalf("method%d cut/copy header count%d", wm, copied)
			}
		}
	}
}
