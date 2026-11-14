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

// A hidden line keeps its place but gets no row and is never copied; a
// message of only hidden lines has no row at all.
func TestHiddenLinesHaveNoRows(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	doc := Parse("a\nx\nb", options)
	doc.Lines[1].Hidden, doc.Lines[1].Decor = true, true
	for _, width := range []int{4, 20, 80} {
		_, shown := painted(t, doc, options, width)
		if strings.Join(shown, "|") != "a|b" {
			t.Fatalf("width %d rows %q", width, shown)
		}
	}
	if len(doc.Lines) != len(doc.Sources) || RenderText(doc) != "a\nb" {
		t.Fatalf("lines %d sources %d copy %q", len(doc.Lines), len(doc.Sources), RenderText(doc))
	}
	only := Parse("x", options)
	only.Lines[0].Hidden, only.Lines[0].Decor = true, true
	if rows := Layout(only, 20, LayoutOptions{Method: options.Method, Theme: options.Theme}).Rows; len(rows) != 0 {
		t.Fatalf("hidden-only message rows %+v", rows)
	}
}

// C3: every row of the plan's setext table. Each display line is described
// as h<level>, hidden, rule, list, quote, table or text.
func TestSetextHeadings(t *testing.T) {
	kind := func(doc Document, n int) string {
		dl := doc.Lines[n]
		theme := documentOptions().Theme
		switch {
		case dl.Hidden:
			return "hidden"
		case dl.Rule:
			return "rule"
		case dl.Table != nil:
			return "table"
		case dl.Quote:
			return "quote"
		case dl.List.Content > 0:
			return "list"
		case len(dl.Pieces) > 0 && dl.Pieces[0].Style.Bold && dl.Pieces[0].Style.FG == HeadingColor(theme, 1):
			return "h1"
		case len(dl.Pieces) > 0 && dl.Pieces[0].Style.Bold && dl.Pieces[0].Style.FG == HeadingColor(theme, 2):
			return "h2"
		}
		return "text"
	}
	for text, want := range map[string]string{
		"Foo\n===":         "h1 hidden",
		"Foo\n=":           "h1 hidden",
		"Foo\n---":         "h2 hidden",
		"Foo\n-":           "h2 hidden",
		"  Foo  \n  ---  ": "h2 hidden",
		"Foo\nBar\n---":    "h2 h2 hidden",
		"\n---":            "text rule",
		"\n-":              "text text",
		"- Foo\n---":       "list rule",
		"> Foo\n---":       "quote rule",
		"Foo\n- - -":       "text rule",
		"Foo\n***":         "text rule",
		"Foo\n=== x":       "text text",
		"Foo\n= =":         "text text",
		"# Foo\n---":       "h1 rule",
		"a | b\n---|---":   "table table",
		"Foo\n    ---":     "text text",
		"Foo\n\nBar\n===":  "text text h1 hidden",
	} {
		mapped(t, text, func(_ ansi.Method, doc Document) {
			var got []string
			for n := range doc.Lines {
				got = append(got, kind(doc, n))
			}
			if strings.Join(got, " ") != want {
				t.Fatalf("%q: %v, want %s", text, got, want)
			}
		})
	}
	// The heading's text is shown trimmed and maps to its source.
	mapped(t, "  Foo  \n  ---  ", func(_ ansi.Method, doc Document) {
		if doc.Lines[0].Text != "Foo" {
			t.Fatalf("heading text %q", doc.Lines[0].Text)
		}
		if bare, _, ok := LineStart(doc.Sources[0], 0); !ok || bare != 2 {
			t.Fatalf("heading starts at %d", bare)
		}
	})
}

// C2: where an indented code block may start, what it owns and where it ends.
// Each display line is c (code), f (a card frame) or another kind's letter.
func TestIndentedCode(t *testing.T) {
	kind := func(dl DisplayLine) string {
		switch {
		case dl.Hidden:
			return "h"
		case dl.Fence != FenceNone:
			return "f"
		case dl.Code:
			return "c"
		case dl.Rule:
			return "r"
		case dl.Table != nil:
			return "T"
		case dl.Quote:
			return "q"
		case dl.List.Content > 0:
			return "l"
		}
		return "t"
	}
	for text, want := range map[string]string{
		"    x = 1":                    "f c f",
		"\n    x = 1":                  "t f c f",
		"# H\n    x":                   "t f c f",
		"***\n    x":                   "r f c f",
		"```\ny\n```\n    x":           "f c f f c f",
		"a | b\n---|---\n    x":        "T T f c f",
		"Foo\n===\n    x":              "t h f c f",
		"    a\n    b":                 "f c c f",
		"    a\n\n    b":               "f c c c f",
		"    a\n\nb":                   "f c f t t",
		"    a\nb":                     "f c f t",
		"text\n    x":                  "t t",
		"> q\n    x":                   "q t",
		"- a\n    x":                   "l t",
		"- a\n\n    x":                 "l t t",
		"    ```\n    y\n    ```":      "f c c c f",
		"    | a | b |\n    |---|---|": "f c c f",
		"- a\n    ```\n    y\n    ```": "l f c f",
		"\n\t- item":                   "t f c f",
		"- a\n\t- b":                   "l l",
		"    ***":                      "f c f",
		"   ***":                       "r",
		"    # x\n    > y\n    <b>":    "f c c c f",
	} {
		mapped(t, text, func(_ ansi.Method, doc Document) {
			var got []string
			for _, dl := range doc.Lines {
				got = append(got, kind(dl))
			}
			if strings.Join(got, " ") != want {
				t.Fatalf("%q: %v, want %s", text, got, want)
			}
		})
	}
	// The card shows each line without its first 4 columns, copies that
	// code, and a whole-line Quote keeps the indentation.
	options := documentOptions()
	options.MapSource = true
	text := "\n    x = 1\n\t\ty"
	doc := Parse(text, options)
	if doc.Lines[2].Text != "x = 1" || doc.Lines[3].Text != "    y" || doc.Lines[1].Block != "x = 1\n    y" {
		t.Fatalf("code lines %q %q block %q", doc.Lines[2].Text, doc.Lines[3].Text, doc.Lines[1].Block)
	}
	if RenderText(doc) != "\nx = 1\n    y" {
		t.Fatalf("copy %q", RenderText(doc))
	}
	if _, wide, ok := LineStart(doc.Sources[2], 0); !ok || text[wide:doc.Sources[2].End] != "    x = 1" {
		t.Fatalf("a whole code line quotes %q", text[wide:doc.Sources[2].End])
	}
	if len(doc.Lines) != len(doc.Sources) {
		t.Fatalf("%d lines, %d sources", len(doc.Lines), len(doc.Sources))
	}
}

// C1 comment blocks: hidden lines; the text after "-->" on the last line is
// literal; an unclosed comment is shown as it is; an open comment owns
// fences, tables, rules and setext pairs; a fence or code block owns "<!--".
func TestCommentBlocks(t *testing.T) {
	shown := func(doc Document) string {
		var parts []string
		for _, dl := range doc.Lines {
			switch {
			case dl.Hidden:
				parts = append(parts, "~")
			case dl.Fence != FenceNone:
				parts = append(parts, "|")
			default:
				parts = append(parts, dl.Text)
			}
		}
		return strings.Join(parts, " / ")
	}
	for text, want := range map[string]string{
		"<!-- foo -->*bar*":            "*bar*",
		"<!-- only -->":                "~",
		"   <!-- x -->":                "~",
		"a\n<!-- n -->\nb":             "a / ~ / b",
		"<!-- a\nb\nc --> after **x**": "~ / ~ / after **x**",
		"<!-- open\n# not hidden":      "<!-- open / not hidden",
		"<!--\n```\ncode\n```\n| a | b |\n|---|---|\n---\nFoo\n===\n-->": "~ / ~ / ~ / ~ / ~ / ~ / ~ / ~ / ~ / ~",
		"```\n<!-- x -->\n```":          "| / <!-- x --> / |",
		"\n    <!-- x -->":              " / | / <!-- x --> / |",
		"\n    code\n<!-- c -->\nafter": " / | / code / | / ~ / after",
		"    <!-- x -->\n":              "| / <!-- x --> / | / ",
		"Foo\n<!-- c -->\n---":          "Foo / ~ / ",
	} {
		mapped(t, text, func(_ ansi.Method, doc Document) {
			if got := shown(doc); got != want {
				t.Fatalf("%q shows %q, want %q", text, got, want)
			}
			if len(doc.Lines) != len(doc.Sources) {
				t.Fatalf("%q: %d lines, %d sources", text, len(doc.Lines), len(doc.Sources))
			}
		})
	}
	// Copy and Quote (plan section 6).
	options := documentOptions()
	options.MapSource = true
	for _, tc := range []struct {
		text           string
		la, oa, lb, ob int
		copy, quote    string
	}{
		{"a\n<!-- n -->\nb", 0, 0, 2, 1, "a\nb", "a\n<!-- n -->\nb"},
		{"<!-- n -->\nb", 1, 0, 1, 1, "b", "b"},
	} {
		doc := Parse(tc.text, options)
		if RenderText(doc) != tc.copy {
			t.Fatalf("%q copies %q", tc.text, RenderText(doc))
		}
		selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: tc.text, Render: func(s string) string {
			return RenderText(Parse(s, options))
		}}
		if got, ok := selection.Exact(tc.la, tc.oa, tc.lb, tc.ob, true, true); !ok || got != tc.quote {
			t.Fatalf("%q quotes %q (%v)", tc.text, got, ok)
		}
	}
}

// C1 inline tags: styles, hidden tags, literal unmatched or unknown ones, and
// the places a tag is not a tag.
func TestInlineTags(t *testing.T) {
	type styled struct {
		text         string
		bold, italic bool
	}
	pieces := func(dl DisplayLine) []styled {
		var out []styled
		for _, p := range dl.Pieces {
			if p.End > p.Start {
				out = append(out, styled{dl.Text[p.Start:p.End], p.Style.Bold, p.Style.Italic})
			}
		}
		return out
	}
	theme := documentOptions().Theme
	for _, tc := range []struct {
		input, text string
		bold        string // the bold text, "" for none
		italic      string
	}{
		{"<b>x <i>y</i> z</b>", "x y z", "x y z", "y"},
		{"<B>x</B> <EM>y</EM>", "x y", "x", "y"},
		{"<strong>s</strong>", "s", "s", ""},
		{"<b>x <i>y</b> z</i>", "<b>x y</b> z", "", "y</b> z"},
		{"<b>x", "<b>x", "", ""},
		{"x</b>", "x</b>", "", ""},
		{"<b class=\"a\">x</b>", "<b class=\"a\">x</b>", "", ""},
		{"<span>x</span> &amp;", "<span>x</span> &amp;", "", ""},
		{"\\<b>x\\</b>", "<b>x</b>", "", ""},
		{"`<b>x</b>`", "<b>x</b>", "", ""},
		{"<details><summary>More</summary>body</details>", "Morebody", "", ""},
		{"a <!-- x --> *b*", "a  b", "", "b"},
		{"<https://example.org/b> <b>x</b>", "https://example.org/b x", "x", ""},
	} {
		mapped(t, tc.input, func(_ ansi.Method, doc Document) {
			dl := doc.Lines[0]
			if dl.Text != tc.text {
				t.Fatalf("%q shows %q, want %q", tc.input, dl.Text, tc.text)
			}
			var bold, italic strings.Builder
			for _, p := range pieces(dl) {
				if p.bold {
					bold.WriteString(p.text)
				}
				if p.italic {
					italic.WriteString(p.text)
				}
			}
			if bold.String() != tc.bold || italic.String() != tc.italic {
				t.Fatalf("%q: bold %q italic %q, want %q %q", tc.input, bold.String(), italic.String(), tc.bold, tc.italic)
			}
		})
	}
	// Code style for <kbd> and <code>; a tag in a heading, quote and list item.
	mapped(t, "<kbd>Ctrl</kbd>+<code>C</code>", func(_ ansi.Method, doc Document) {
		var code strings.Builder
		for _, p := range doc.Lines[0].Pieces {
			if p.Style.FG == theme.Brand {
				code.WriteString(doc.Lines[0].Text[p.Start:p.End])
			}
		}
		if doc.Lines[0].Text != "Ctrl+C" || code.String() != "CtrlC" {
			t.Fatalf("kbd/code %q code %q", doc.Lines[0].Text, code.String())
		}
	})
	mapped(t, "# <b>H</b>\n> <i>q</i>\n- <b>item</b>", func(_ ansi.Method, doc Document) {
		if doc.Lines[0].Text != "H" || doc.Lines[1].Text != "q" || doc.Lines[2].Text != "• item" {
			t.Fatalf("tags in blocks %q %q %q", doc.Lines[0].Text, doc.Lines[1].Text, doc.Lines[2].Text)
		}
	})
	// A line left empty is hidden; a tag-only line is not a setext paragraph.
	mapped(t, "<details>\nFoo\n---", func(_ ansi.Method, doc Document) {
		if !doc.Lines[0].Hidden || doc.Lines[1].Text != "Foo" || !doc.Lines[2].Hidden {
			t.Fatalf("details/setext %+v", doc.Lines)
		}
	})
}

// <br> splits a line into parts, each wrapped by itself, copied as its own
// line, and quoted (whole) as the source with its tag.
func TestLineBreakTag(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "a<br>b"
	doc := Parse(text, options)
	if len(doc.Lines) != 2 || doc.Lines[0].Text != "a" || doc.Lines[1].Text != "b" || len(doc.Sources) != 2 {
		t.Fatalf("parts %+v", doc.Lines)
	}
	if RenderText(doc) != "a\nb" {
		t.Fatalf("copy %q", RenderText(doc))
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	if got, ok := selection.Exact(0, 0, 1, 1, true, true); !ok || got != text {
		t.Fatalf("quote %q %v", got, ok)
	}
	for _, br := range []string{"x<br/>y", "x<br />y", "x<BR>y"} {
		if d := Parse(br, options); len(d.Lines) != 2 || d.Lines[1].Text != "y" {
			t.Fatalf("%q parts %+v", br, d.Lines)
		}
	}
	long := Parse("- "+strings.Repeat("word ", 8)+"<br>"+strings.Repeat("more ", 8), options)
	for _, width := range []int{20, 80} {
		rows, _ := painted(t, long, options, width)
		checkCoverage(t, long, rows)
	}
}

// Copy and Quote of inline tags (plan section 6): a selection inside the
// tagged text is bare; a whole-line one takes the tags.
func TestInlineTagQuote(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "a <b>c</b> d"
	doc := Parse(text, options)
	if RenderText(doc) != "a c d" {
		t.Fatalf("copy %q", RenderText(doc))
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	if got, ok := selection.Exact(0, 2, 0, 3, true, true); !ok || got != "c" {
		t.Fatalf("quote of c: %q %v", got, ok)
	}
	if got, ok := selection.Exact(0, 0, 0, len(doc.Lines[0].Text), true, true); !ok || got != text {
		t.Fatalf("whole-line quote %q %v", got, ok)
	}
}

// <br> in a table cell (8a): one space in the cell's text with a forced
// break; the table always goes through the fit, so a row is as high as its
// tallest cell's segments.
func TestTableCellLineBreak(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "| k | v |\n|---|---|\n| a<br>b | c |"
	doc := Parse(text, options)
	row := doc.Lines[2]
	if row.Table == nil || !row.Table.Breaks || len(row.Breaks) != 1 || row.Text[row.Breaks[0]] != ' ' {
		t.Fatalf("row %q breaks %v", row.Text, row.Breaks)
	}
	if got := row.Text[row.Cells[0][0]:row.Cells[0][1]]; got != "a b" {
		t.Fatalf("cell text %q", got)
	}
	_, shown := painted(t, doc, options, 40)
	joined := strings.Join(shown, "\n")
	if !strings.Contains(joined, "a ") || !strings.Contains(joined, "b") || len(shown) < 4 {
		t.Fatalf("rows %q", shown)
	}
	var aRow, bRow = -1, -1
	for i, line := range shown {
		if strings.HasPrefix(strings.TrimSpace(line), "a") {
			aRow = i
		}
		if strings.HasPrefix(strings.TrimSpace(line), "b") {
			bRow = i
		}
	}
	if aRow < 0 || bRow != aRow+1 {
		t.Fatalf("a and b are not on consecutive rows: %q", shown)
	}
	for _, width := range []int{8, 12, 20, 80} {
		rows, _ := painted(t, doc, options, width)
		checkCoverage(t, doc, rows)
	}
	// Two breaks in a cell, breaks in two cells and in the header.
	two := Parse("| h<br>i | x |\n|---|---|\n| a<br>b<br>c | d<br>e |", options)
	if len(two.Lines[0].Breaks) != 1 || len(two.Lines[2].Breaks) != 3 {
		t.Fatalf("breaks %v %v", two.Lines[0].Breaks, two.Lines[2].Breaks)
	}
	if two.Lines[0].Table.Natural[0] != 2 {
		t.Fatalf("natural width is the widest segment, or the first line and its …: %v", two.Lines[0].Table.Natural)
	}
	// Collapsed: the row clips at its first line with the … mark.
	// (A long unbreakable word in the other column makes the table collapse.)
	collapsing := Parse("| k | v |\n|---|---|\n| a<br>b | "+strings.Repeat("x", 40)+" |", options)
	compact := Layout(collapsing, 24, LayoutOptions{Method: options.Method, Theme: options.Theme, CompactTables: true}).Rows
	found := false
	for _, r := range compact {
		var b strings.Builder
		for _, run := range r.Runs {
			b.WriteString(run.Text)
		}
		if strings.Contains(b.String(), "a…") {
			found = true
		}
	}
	if !found {
		t.Fatal("collapsed row shows no first line with …")
	}
	// Quote and Copy of the cell read its text, "a b", as today's contract.
	if got := TableText(doc.Lines, 2, 2, row.Cells[0][0], row.Cells[0][1]); len(got) != 1 || got[0].Text != "a b" {
		t.Fatalf("cell quote %+v", got)
	}
	if got := TableText(doc.Lines, 2, 2, 0, len(row.Text)); len(got) != 1 || got[0].Text != "a b\tc" {
		t.Fatalf("row quote %+v", got)
	}
	if got := TableText(doc.Lines, 2, 2, row.Breaks[0]+1, row.Cells[0][1]); len(got) != 1 || got[0].Text != "b" {
		t.Fatalf("part quote %+v", got)
	}
}

// Labelled rows: each forced break's segment starts its own line under the
// label.
func TestTableCellLineBreakLabelled(t *testing.T) {
	options := documentOptions()
	head := "| " + strings.Repeat("col | ", 7) + "\n|" + strings.Repeat("---|", 7) + "\n"
	doc := Parse(head+"| first<br>second | b | c | d | e | f | g |", options)
	rows := Layout(doc, 14, LayoutOptions{Method: options.Method, Theme: options.Theme}).Rows
	var shown []string
	for _, r := range rows {
		var b strings.Builder
		for _, run := range r.Runs {
			b.WriteString(run.Text)
		}
		shown = append(shown, b.String())
	}
	at := -1
	for i, line := range shown {
		if line == "col: first" {
			at = i
		}
	}
	if at < 0 || at+1 >= len(shown) || strings.TrimSpace(shown[at+1]) != "second" {
		t.Fatalf("segments %q", shown)
	}
}
