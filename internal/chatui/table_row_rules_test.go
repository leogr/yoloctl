package chatui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func tableRowRuleDecoration(row bodyRow) bool {
	return row.Decor && len(row.Runs) == 1 && strings.Contains(row.Runs[0].Text, " │ ") && strings.Contains(row.Runs[0].Text, "─")
}

// Logical rows get one quiet divider between them, never between a cell's
// wrapped fragments. Header and body dividers share their column geometry.
func TestTableRowSeparatorsRenderInBothThemesAndWidths(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			for _, width := range []int{100, 40} {
				t.Run(fmt.Sprintf("dark=%t/method=%d/width=%d", dark, wm, width), func(t *testing.T) {
					theme := spaceview.NewTheme(dark, nil, nil)
					lines := displayLines(wm, sampleTable, false, theme, "")
					rows := bodyRows(wm, lines, width, theme)
					var header string
					for _, row := range rows {
						if row.Line == 1 && row.Decor {
							header = ansiText(row.Runs)
						}
					}
					if header == "" {
						t.Fatal("missing header rule")
					}
					count := 0
					for i, row := range rows {
						if !tableRowRuleDecoration(row) {
							continue
						}
						count++
						if row.Line != count+2 || i == 0 || i+1 >= len(rows) || rows[i-1].Line != row.Line-1 || rows[i+1].Line != row.Line || rows[i+1].Decor {
							t.Fatalf("rule is not between logical body rows: %+v", row)
						}
						text := ansiText(row.Runs)
						if strings.ContainsAny(text, "┄┼") || !strings.ContainsRune(header, '┼') || strings.ContainsRune(header, '│') || wm.StringWidth(text) != wm.StringWidth(header) || wm.StringWidth(text) > width {
							t.Fatalf("row style or header changed: %q, header %q", text, header)
						}
						// A body column's vertical divider stays in the same cell,
						// with one blank cell on either side instead of a cross.
						ruleCells, bodyCells := []rune(text), []rune(ansiText(rows[i+1].Runs))
						for column, glyph := range ruleCells {
							if glyph == '│' && (column == 0 || column+1 >= len(ruleCells) || ruleCells[column-1] != ' ' || ruleCells[column+1] != ' ' || column >= len(bodyCells) || bodyCells[column] != '│') {
								t.Fatalf("column divider lost its gaps or alignment: %q, body %q", text, string(bodyCells))
							}
						}
						if row.Runs[0].Style.FG != theme.Dim || row.Runs[0].Copy || row.Runs[0].Act.Kind != actNone || len(row.Frags) != 0 {
							t.Fatalf("divider has text/action authority: %+v", row)
						}
						rendered := paint(wm, row.Runs, width, 0, nil, spaceview.Style{})
						if !strings.Contains(rendered, spaceview.StyleText(text, spaceview.Style{FG: theme.Dim})) {
							t.Fatalf("theme color missing from rendered divider: %q", rendered)
						}
					}
					if count != 2 {
						t.Fatalf("got %d body dividers, want2", count)
					}
				})
			}
		}
	}
}

func TestTableRowSeparatorsDoNotEnterCopyQuoteOrSource(t *testing.T) {
	m, copied := copyingModel(100, 30)
	m.Update(IncomingMsg{Message: Message{ID: "rules", Nick: "x", Text: sampleTable}})
	m.View()
	key := "server:rules"
	lines := m.body(&m.messages[m.indexOf(key)], m.lastTextWidth).lines
	x0, y0 := cellOf(t, m, key, 2, 0)
	x1, y1 := cellOf(t, m, key, 3, len(lines[3].Text)-1)
	drag(m, x0, y0, x1, y1)
	if len(*copied) != 1 || (*copied)[0] != lines[2].Text+"\n"+lines[3].Text {
		t.Fatalf("copy changed logical table text: %q", *copied)
	}
	m.quoteSelection()
	if strings.ContainsAny(m.Draft(), "─│┄┼") || !strings.Contains(m.Draft(), "Code\t") || !strings.Contains(m.Draft(), "Math\t") {
		t.Fatalf("quote contains decorations or lost cells: %q", m.Draft())
	}
	if m.Messages()[0].Text != sampleTable {
		t.Fatal("rendering changed source message")
	}
	theme := m.theme
	for _, source := range []string{sampleTable, "```\n" + sampleTable + "\n```"} {
		literal := displayLines(ansi.WcWidth, source, source == sampleTable, theme, "")
		if slices.ContainsFunc(bodyRows(ansi.WcWidth, literal, 100, theme), tableRowRuleDecoration) {
			t.Fatal("source/fenced table gained row rules")
		}
	}
}

func TestTableRowSeparatorsRespectBodyAndWrappedRowLimits(t *testing.T) {
	theme := New(Options{}).theme
	source := sampleTable + "\n| Long | " + strings.Repeat("syntax words ", 8) + " | Supported |"
	lines := displayLines(ansi.WcWidth, source, false, theme, "")
	fitted := fitTable(ansi.WcWidth, lines, 0, 48, maxBodyRows, theme)
	if fitted == nil {
		t.Fatal("fixture table did not wrap")
	}
	visual := 3 // additional dividers between the four logical body rows
	for _, rows := range fitted {
		visual += len(rows)
	}
	if fitTable(ansi.WcWidth, lines, 0, 48, visual, theme) != nil {
		t.Fatal("wrapped layout ignored divider rows in its admission budget")
	}
	text := strings.Repeat("p\n", maxBodyRows-6) + sampleTable
	rows := bodyRows(ansi.WcWidth, displayLines(ansi.WcWidth, text, false, theme, ""), 100, theme)
	if len(rows) != maxBodyRows+1 || !strings.Contains(ansiText(rows[len(rows)-1].Runs), "display row limit") {
		t.Fatalf("row separators escaped body bound: %d rows", len(rows))
	}
}

func TestTableRowRuleColumnGaps(t *testing.T) {
	theme := spaceview.NewTheme(true, nil, nil)
	for _, tc := range []struct {
		name, header, want string
		width              int
	}{
		{"one column", "───", "───", 3},
		{"two columns", "────┼────", "─── │ ───", 9},
		{"three columns", "──┼───┼──", "─ │ ─ │ ─", 9},
		{"empty", "", "", 10},
		{"too wide", "────┼────", "", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tableRowRule(ansi.WcWidth, tc.header, tc.width, theme)
			if text := ansiText(got); text != tc.want || (tc.want == "" && got != nil) {
				t.Fatalf("row rule = %q, want %q", text, tc.want)
			}
		})
	}
}
