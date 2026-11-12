package chatui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// ***both*** is bold and italic, ~~gone~~ is struck through; unpaired or
// single markers stay literal, and the original text is untouched.
func TestBoldItalicAndStrikethrough(t *testing.T) {
	theme := New(Options{}).theme
	base := spaceview.Style{FG: theme.Text}
	text, spans := inlineMarkdown("a ***both*** b ~~gone~~ c ~one~ ~~open", base, theme)
	if text != "a both b gone c ~one~ ~~open" {
		t.Fatalf("rendered %q", text)
	}
	var both, gone bool
	for _, sp := range spans {
		switch text[sp.Start:sp.End] {
		case "both":
			both = sp.Style.Bold && sp.Style.Italic
		case "gone":
			gone = sp.Strike
		}
	}
	if !both || !gone {
		t.Fatalf("styles: bold-italic %v, strike %v", both, gone)
	}
	m, copied := copyingModel(60, 20)
	m.Update(IncomingMsg{Message: Message{ID: "s", Nick: "x", Text: "keep ~~old~~ new"}})
	raw := m.View().Content
	if !strings.Contains(raw, strikeOn+spaceview.StyleText("old", spaceview.Style{FG: theme.Text})) {
		t.Fatalf("struck text is drawn with SGR 9 and reset after it: %q", raw)
	}
	x0, y := cellOf(t, m, "server:s", 0, 0)
	x1, _ := cellOf(t, m, "server:s", 0, len("keep old ne"))
	drag(m, x0, y, x1, y)
	if len(*copied) != 1 || (*copied)[0] != "keep old new" {
		t.Fatalf("struck text copies as shown: %q", *copied)
	}
}

const sampleTable = "| Feature | Syntax | Status |\n| :--- | :---: | ---: |\n| Code | \\`code\\` | **Supported** |\n| Math | `$E = mc^2$` | *Optional* |\n| Pipe \\| cell | x |"

// A table aligns its columns as the delimiter row asks, frames the header,
// renders inline Markdown in cells and copies cells as shown, never the rule.
func TestTablesAlignColumnsAndCopyCells(t *testing.T) {
	m, copied := copyingModel(100, 20)
	m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: sampleTable + "\nafter"}})
	out := view(m)
	for _, want := range []string{
		"Feature     │   Syntax   │    Status",
		"────────────┼────────────┼──────────",
		"Code        │   `code`   │ Supported",
		"Math        │ $E = mc^2$ │  Optional",
		"Pipe | cell │     x      │",
		"after",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "| :---") || strings.Contains(out, "**Supported**") {
		t.Fatalf("table markup must not show:\n%s", out)
	}
	if !strings.Contains(m.View().Content, spaceview.StyleText("Supported", spaceview.Style{FG: m.theme.Text, Bold: true})) {
		t.Fatal("inline Markdown applies inside cells")
	}
	// A drag over the header, the rule and the first row copies the cells
	// as shown and leaves the frame out.
	x0, y0 := cellOf(t, m, "server:t", 0, 0)
	x1, y1 := cellOf(t, m, "server:t", 2, len("Code        │   `code`   │ Supporte"))
	drag(m, x0, y0, x1, y1)
	want := "Feature     │   Syntax   │    Status\nCode        │   `code`   │ Supported"
	if len(*copied) != 1 || (*copied)[0] != want {
		t.Fatalf("copied %q, want %q", *copied, want)
	}
	if m.Messages()[0].Text != sampleTable+"\nafter" {
		t.Fatal("rendering changed the original text")
	}
}

// Only a header followed by a matching delimiter row is a table; source
// view, fenced code and mismatched rows stay literal.
func TestTablesNeedHeaderAndDelimiter(t *testing.T) {
	theme := New(Options{}).theme
	for _, literal := range []string{
		"a | b\nnot a delimiter",
		"a | b\n| --- |",
		"| a |\n| -x- |",
		"just --- dashes\n---",
	} {
		for _, dl := range displayLines(ansi.WcWidth, literal, false, theme, "") {
			if strings.Contains(dl.Text, "│") || strings.Contains(dl.Text, "┼") {
				t.Fatalf("%q must stay literal, got %q", literal, dl.Text)
			}
		}
	}
	for _, dl := range displayLines(ansi.WcWidth, "```\n"+sampleTable+"\n```", false, theme, "") {
		if strings.Contains(dl.Text, "┼") {
			t.Fatal("a table inside fenced code stays literal")
		}
	}
	if lines := displayLines(ansi.WcWidth, sampleTable, true, theme, ""); lines[1].Text != "| :--- | :---: | ---: |" {
		t.Fatalf("source view keeps the table markup: %q", lines[1].Text)
	}
	// Ragged rows are padded or cut to the header's columns.
	lines := displayLines(ansi.WcWidth, "| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |", false, theme, "")
	if len(lines) != 4 || lines[2].Text != "1 │" || lines[3].Text != "1 │ 2" {
		t.Fatalf("ragged rows: %q", []string{lines[2].Text, lines[3].Text})
	}
}

// Links and mentions in cells keep their targets, and a table wider than
// the view wraps instead of being cut.
func TestTableCellsKeepTargetsAndWrap(t *testing.T) {
	m, _ := readyModel()
	m.Update(IncomingMsg{Message: Message{ID: "w", Nick: "x", Text: "| who | where |\n|---|---|\n| @sol | https://example.com/" + strings.Repeat("p", 60) + " |"}})
	m.View()
	var link, mention bool
	rows := 0
	for _, row := range m.lastTranscript {
		if row.key != "server:w" || row.kind != rowBody {
			continue
		}
		rows++
		for _, r := range row.runs {
			link = link || r.Act.Kind == actLink
			mention = mention || r.Act.Kind == actMention && r.Act.Value == "sol"
		}
	}
	if !link || !mention || rows < 3 {
		t.Fatalf("cell targets and wrapping: link %v mention %v rows %d", link, mention, rows)
	}
}

// A skewed or oversized table stays literal instead of padding every row to
// its widest cell: one 64 KiB header cell over 1,000 tiny rows must not grow
// to megabytes of display text (root review of follow-up 6).
func TestTablesAreBoundedBeforePadding(t *testing.T) {
	theme := New(Options{}).theme
	// 1,000 rows exceed the row bound; 500 stay within it, so only the
	// byte budget can refuse that one.
	for _, n := range []int{1000, 500} {
		skewed := "| " + strings.Repeat("x", 64<<10) + " | b |\n|---|---|\n" + strings.Repeat("| a | b |\n", n)
		var shown int
		for _, dl := range displayLines(ansi.WcWidth, skewed, false, theme, "") {
			shown += len(dl.Text)
			if n > maxTableRows && dl.Table != nil {
				t.Fatal("row limit bypassed")
			}
			if n <= maxTableRows && dl.Table != nil && !dl.Table.Unpadded {
				t.Fatal("rejected padding rebuilt")
			}
		}
		if shown > 2*len(skewed)+tableSlack {
			t.Fatalf("display text %d bytes for %d source bytes", shown, len(skewed))
		}
	}
	tall := "| a | b |\n|---|---|\n" + strings.Repeat("| 1 | 2 |\n", maxTableRows+1)
	wide := "|" + strings.Repeat(" c |", maxTableColumns+1) + "\n|" + strings.Repeat("---|", maxTableColumns+1)
	for name, text := range map[string]string{"rows": tall, "columns": wide} {
		for _, dl := range displayLines(ansi.WcWidth, text, false, theme, "") {
			if strings.Contains(dl.Text, "│") {
				t.Fatalf("a table over its %s bound stays literal", name)
			}
		}
	}
	fits := "| a | b |\n|---|---|\n" + strings.Repeat("| 1 | 2 |\n", maxTableRows)
	if lines := displayLines(ansi.WcWidth, fits, false, theme, ""); lines[0].Text != "a │ b" {
		t.Fatalf("a table within its bounds still renders: %q", lines[0].Text)
	}
}

// The header rule contains multibyte separators too. This table fits if
// their bytes are undercounted, but its rendered text exceeds the budget.
func TestTableBudgetIncludesRuleSeparators(t *testing.T) {
	cell := strings.Repeat("x", 32)
	text := "|" + cell + "|" + cell + "|\n|---:|---:|\n" + strings.Repeat("|x|x|\n", 70)
	theme := New(Options{}).theme
	shown := 0
	for _, dl := range displayLines(ansi.WcWidth, text, false, theme, "") {
		shown += len(dl.Text)
	}
	if shown > 2*len(text)+tableSlack {
		t.Fatalf("display text %d bytes exceeds the budget for %d source bytes", shown, len(text))
	}
}

// An opening fence wins over table detection, even when it contains pipes,
// and escaped pipes are decided by one scan (root review of follow-up 6).
func TestTableFencePrecedenceAndEscapedPipes(t *testing.T) {
	theme := New(Options{}).theme
	for _, dl := range displayLines(ansi.WcWidth, "``` a | b\n|---|---|\n| x | y |\n```", false, theme, "") {
		if strings.Contains(dl.Text, "┼") || strings.Contains(dl.Text, "│") {
			t.Fatalf("fenced content became a table: %q", dl.Text)
		}
	}
	cells, ok := tableCells(`| a \\| b |`)
	if !ok || len(cells) != 2 || cells[0] != `a \\` || cells[1] != "b" {
		t.Fatalf("an escaped backslash leaves the pipe a separator: %q", cells)
	}
	cells, ok = tableCells(`| x \| y |`)
	if !ok || len(cells) != 1 || cells[0] != `x \| y` {
		t.Fatalf("an escaped pipe stays in its cell: %q", cells)
	}
	if cells, ok = tableCells(`only \| escaped`); ok {
		t.Fatalf("a line with only escaped pipes is not a row: %q", cells)
	}
	if cells, _ = tableCells(`a | b \\|`); len(cells) != 2 || cells[1] != `b \\` {
		t.Fatalf("a trailing pipe after an escaped backslash is a real trailing pipe: %q", cells)
	}
	lines := displayLines(ansi.WcWidth, "| a \\\\| b |\n|---|---|\n| x \\| y | z |", false, theme, "")
	if lines[0].Text != `a \   │ b` || lines[2].Text != "x | y │ z" { // padded to "x | y"
		t.Fatalf("escapes render once: %q / %q", lines[0].Text, lines[2].Text)
	}
}

// An over-bound table is consumed once as literal text: a run of
// delimiter-like lines must cost about what the same amount of plain text
// costs, not a rescan of up to maxTableRows rows from every line (Opus B1).
func TestOverBoundTablesAreScannedOnce(t *testing.T) {
	theme := New(Options{}).theme
	const lines = 16 << 10 // 64 KiB of four-byte lines
	plainText := strings.Repeat("x-x\n", lines)
	delimiters := strings.Repeat("|-|\n", lines)
	cost := func(text string) float64 {
		return testing.AllocsPerRun(1, func() { displayLines(ansi.WcWidth, text, false, theme, "") })
	}
	base, table := cost(plainText), cost(delimiters)
	if table > 2*base+1024 {
		t.Fatalf("%.0f allocations for delimiter-like lines, %.0f for plain text", table, base)
	}
	// The shape Opus measured within the room's default limits (1024 lines,
	// 64 KiB per message): 16-column delimiter rows, about 35 KB.
	row := "|" + strings.Repeat("-|", 16)
	wide, flat := strings.Repeat(row+"\n", 1023)+row, strings.Repeat(strings.Repeat("x", len(row))+"\n", 1023)+row
	if a, b := cost(wide), cost(flat); a > 2*b+1024 {
		t.Fatalf("%.0f allocations for 1024 16-column delimiter rows, %.0f for plain text", a, b)
	}
	out := displayLines(ansi.WcWidth, delimiters, false, theme, "")
	if len(out) != lines+1 || out[0].Text != "|-|" || out[lines-1].Text != "|-|" {
		t.Fatalf("the rejected span stays literal: %d lines, first %q", len(out), out[0].Text)
	}
	// A table refused by the byte budget is consumed as one span too: its
	// delimiter-like rows must not become a second table.
	skewed := "| " + strings.Repeat("x", 64<<10) + " | b |\n|---|---|\n" + strings.Repeat("|-|-|\n", maxTableRows)
	for _, dl := range displayLines(ansi.WcWidth, skewed, false, theme, "") {
		if dl.Table != nil && !dl.Table.Unpadded {
			t.Fatal("rejected padding rebuilt")
		}
	}
	// A valid table right after a rejected span still renders.
	after := strings.Repeat("|-|\n", maxTableRows+3) + "\nok\n\n| a | b |\n|---|---|\n| 1 | 2 |"
	shown := displayLines(ansi.WcWidth, after, false, theme, "")
	if got := shown[len(shown)-1].Text; got != "1 │ 2" {
		t.Fatalf("a later valid table renders: %q", got)
	}
}

// A heading, quote, list or fence ends a table, as in GFM, even when that
// line contains pipes (Opus review, minor).
func TestBlocksEndTables(t *testing.T) {
	theme := New(Options{}).theme
	for _, next := range []string{"# head | x", "> quote | x", "- item | x", "1. item | x", "```"} {
		lines := displayLines(ansi.WcWidth, "| a | b |\n|---|---|\n| 1 | 2 |\n"+next, false, theme, "")
		if last := lines[len(lines)-1].Text; strings.Contains(last, "│") {
			t.Fatalf("%q continued the table: %q", next, last)
		}
	}
}
