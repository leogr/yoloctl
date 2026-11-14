package markdownview

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// mapped parses text with its source map, at both width methods.
func mapped(t *testing.T, text string, f func(wm ansi.Method, doc Document)) {
	t.Helper()
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		options := documentOptions()
		options.Method, options.MapSource = wm, true
		f(wm, Parse(text, options))
	}
}

// B3: a heading's closing sequence of unescaped '#' (after a space or TAB,
// with only spaces or TABs after it) is dropped; a whole-line Quote still
// takes the source line through its end.
func TestHeadingClosingHashes(t *testing.T) {
	for input, want := range map[string]string{
		"## T ##":     "T",
		"## T #   ":   "T",
		"## T\t###":   "T",
		"## T#":       "T#",
		"## C#":       "C#",
		"## T \\#":    "T #",
		"### T \\###": "T ###",
		"# #":         "",
		"#   ###  ":   "",
		"## a # b ##": "a # b",
	} {
		mapped(t, input, func(_ ansi.Method, doc Document) {
			dl := doc.Lines[0]
			if dl.Text != want || want != "" && (len(dl.Pieces) == 0 || !dl.Pieces[0].Style.Bold) {
				t.Fatalf("%q shows %q (bold %v), want %q", input, dl.Text, len(dl.Pieces) > 0 && dl.Pieces[0].Style.Bold, want)
			}
			if want == "" {
				return
			}
			src := doc.Sources[0]
			if _, wide, ok := LineEnd(src, len(dl.Text)); !ok || wide != len(input) {
				t.Fatalf("%q: a whole-line end maps to %d, not %d", input, wide, len(input))
			}
			dropped := map[string]bool{"## T ##": true, "## T #   ": true, "## T\t###": true, "## a # b ##": true}
			if bare, _, ok := LineEnd(src, len(dl.Text)); !ok || dropped[input] == (bare == len(input)) {
				t.Fatalf("%q: the bare end %d takes the closing sequence", input, bare)
			}
		})
	}
}

// B4: every rule spelling with up to 3 columns of indentation is a rule;
// anything else stays what it was.
func TestRuleSpellings(t *testing.T) {
	for input, rule := range map[string]bool{
		"---": true, "***": true, "___": true, "- - -": true, "* * *": true, "_ _ _": true, "-----": true,
		"   ---": true, "\t---": false, "    ***": false, " -\t-\t-": true,
		"- - - x": false, "--": false, "**": false, "-*-": false, "---a": false,
	} {
		mapped(t, input, func(_ ansi.Method, doc Document) {
			if got := len(doc.Lines) == 1 && doc.Lines[0].Rule; got != rule {
				t.Fatalf("%q: rule %v, want %v (%+v)", input, got, rule, doc.Lines)
			}
		})
	}
	// A table's delimiter line stays the table's.
	mapped(t, "| a | b |\n| --- | --- |\n| 1 | 2 |", func(_ ansi.Method, doc Document) {
		for _, dl := range doc.Lines {
			if dl.Table == nil {
				t.Fatalf("table line %q is not part of the table", dl.Text)
			}
		}
	})
}

// B2, B2b: '>' with or without a space, up to 3 columns of indentation, and
// every nested level stripped with one bar each.
func TestQuoteMarkers(t *testing.T) {
	for _, tc := range []struct {
		input string
		depth int
		text  string
	}{
		{">x", 1, "x"}, {"> x", 1, "x"}, {">>x", 2, "x"}, {"> > x", 2, "x"}, {">  > x", 2, "x"},
		{"   > x", 1, "x"}, {"    > x", 0, ""}, {"\t> x", 0, ""},
		{"> - item", 1, "- item"}, {"> ```", 1, "```"}, {"> | a | b |", 1, "| a | b |"},
		{strings.Repeat(">", 8) + " deep", 8, "deep"}, {"x > y", 0, ""},
	} {
		mapped(t, tc.input, func(_ ansi.Method, doc Document) {
			dl := doc.Lines[0]
			if dl.QuoteDepth != tc.depth || dl.Quote != (tc.depth > 0) || tc.depth > 0 && dl.Text != tc.text {
				t.Fatalf("%q: depth %d text %q, want %d %q", tc.input, dl.QuoteDepth, dl.Text, tc.depth, tc.text)
			}
			if tc.depth > 0 {
				if _, wide, ok := LineEnd(doc.Sources[0], len(dl.Text)); !ok || wide != len(tc.input) {
					t.Fatalf("%q: line end maps to %d", tc.input, wide)
				}
			}
		})
	}
}

// Nested bars take two cells each; at a narrow width only as many are drawn
// as leave the text one cell, and the text is still shown.
func TestNestedQuoteBarsAtNarrowWidths(t *testing.T) {
	options := documentOptions()
	doc := Parse(strings.Repeat(">", 8)+" deep text", options)
	for width, bars := range map[int]int{80: 8, 20: 8, 16: 7, 4: 1, 2: 1} {
		rows := Layout(doc, width, LayoutOptions{Method: options.Method, Theme: options.Theme}).Rows
		var shown strings.Builder
		for _, row := range rows {
			if got := strings.Count(row.Runs[0].Text, "│"); got != bars {
				t.Fatalf("width %d: %d bars, want %d", width, got, bars)
			}
			for _, r := range row.Runs[1:] {
				shown.WriteString(r.Text)
			}
		}
		if strings.ReplaceAll(shown.String(), " ", "") != "deeptext" {
			t.Fatalf("width %d: text %q", width, shown.String())
		}
	}
}

// The list context's content columns are source columns: the marker column
// (TABs expanded), the marker's width and the spaces after it (1 to 4, else 1).
func TestListMarkerColumns(t *testing.T) {
	for line, want := range map[string][2]int{
		"- a": {0, 2}, "-  a": {0, 3}, "-    a": {0, 5}, "-     a": {0, 2}, "* a": {0, 2}, "+ a": {0, 2},
		"1. a": {0, 3}, "10. a": {0, 4}, "3) a": {0, 3}, "  - b": {2, 4}, "\t- c": {4, 6}, " \t- c": {4, 6}, "- é": {0, 2},
	} {
		marker, content, ok := listMarker(line)
		if !ok || marker != want[0] || content != want[1] {
			t.Fatalf("%q: marker %d content %d ok %v, want %v", line, marker, content, ok, want)
		}
	}
	for _, line := range []string{"-a", "1.a", "x", "", "1234567890. a"} {
		if _, _, ok := listMarker(line); ok {
			t.Fatalf("%q read as a marker", line)
		}
	}
}

// B1: inside a list a TAB-indented marker line is a nested item, its
// indentation shown as the spaces it expands to; outside a list it stays text.
func TestTabIndentedListItems(t *testing.T) {
	for _, tc := range []struct {
		text   string
		line   int
		item   bool
		marker int
	}{
		{"- a\n\t- b", 1, true, 4},
		{"- a\n  - b\n\t- c", 2, true, 4},
		{"1. a\n\t- b", 1, true, 4},
		{"- é\n\t- b", 1, true, 4},
		{"- a\n\n\t- b", 2, true, 4},
		{"- a\n    more\n\t- b", 2, true, 4},
		{"text\n\t- b", 1, false, 0},
		{"\n\t- b", 1, false, 0},
		{"- a\nplain\n\t- b", 2, false, 0},
	} {
		mapped(t, tc.text, func(_ ansi.Method, doc Document) {
			dl := doc.Lines[tc.line]
			if (dl.List.Content > 0) != tc.item || tc.item && (dl.List.Marker != tc.marker || !strings.HasPrefix(dl.Text, "    • ")) {
				t.Fatalf("%q line %d: %q list %+v", tc.text, tc.line, dl.Text, dl.List)
			}
			// A whole-line Quote still takes the source line, TAB included.
			src := doc.Sources[tc.line]
			if _, wide, ok := LineEnd(src, len(dl.Text)); !ok || wide != src.End {
				t.Fatalf("%q line %d: end maps to %d, not %d", tc.text, tc.line, wide, src.End)
			}
			if bare, _, ok := LineStart(src, 0); !ok || bare != src.Start {
				t.Fatalf("%q line %d: start maps to %d, not %d", tc.text, tc.line, bare, src.Start)
			}
		})
	}
	// The context's nesting follows source columns: 10. opens content
	// column 4, so a 3-column item does not nest and a 4-column one does.
	var c listContext
	c.item(0, 4)
	c.item(3, 5)
	if len(c.open) != 2 || c.open[1].marker != 3 {
		t.Fatalf("3-column item %+v", c.open)
	}
	c = listContext{}
	c.item(0, 4)
	c.item(4, 6)
	if len(c.open) != 2 {
		t.Fatalf("4-column item %+v", c.open)
	}
	c.item(0, 2)
	if len(c.open) != 1 || c.open[0] != (listOpen{0, 2}) {
		t.Fatalf("a sibling closes the nested items: %+v", c.open)
	}
	c.other("    deeper text")
	c.other("")
	if !c.active() {
		t.Fatal("context ended at an indented or blank line")
	}
	c.other("x")
	if c.active() {
		t.Fatal("context continued past an unindented line")
	}
}
