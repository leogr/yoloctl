package markdownview

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// painted lays doc out at width and returns each row's visible text,
// trailing fill included, so gutters and padding are observable.
func painted(t *testing.T, doc Document, options ParseOptions, width int) ([]BodyRow, []string) {
	t.Helper()
	rows := Layout(doc, width, LayoutOptions{Method: options.Method, Theme: options.Theme}).Rows
	var out []string
	for _, row := range rows {
		line := ansi.Strip(Paint(options.Method, row.Runs, width, options.Theme.Surface, nil, spaceview.Style{}))
		if w := options.Method.StringWidth(line); w != width {
			t.Fatalf("row %q is %d cells, not %d", line, w, width)
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return rows, out
}

// checkCoverage asserts that the copyable runs of each display line's rows
// show its text exactly once, in order: no dropped, duplicated or reordered
// bytes and no copied padding.
func checkCoverage(t *testing.T, doc Document, rows []BodyRow) {
	t.Helper()
	got := make([]strings.Builder, len(doc.Lines))
	next := make([]int, len(doc.Lines))
	for _, row := range rows {
		if row.Decor || row.Frags != nil {
			continue
		}
		if row.Start != next[row.Line] {
			t.Fatalf("line %d row starts at %d after %d", row.Line, row.Start, next[row.Line])
		}
		next[row.Line] = row.End
		for _, r := range row.Runs {
			if r.Copy {
				if doc.Lines[row.Line].Text[r.Start:r.Start+len(r.Text)] != r.Text {
					t.Fatalf("run %q does not show its bytes", r.Text)
				}
				got[row.Line].WriteString(r.Text)
			}
		}
	}
	for n, dl := range doc.Lines {
		if dl.Decor || dl.Table != nil {
			continue
		}
		if next[n] != len(dl.Text) || strings.TrimRight(got[n].String(), " ") != strings.TrimRight(dl.Text, " ") {
			t.Fatalf("line %d shows %q of %q (to %d)", n, got[n].String(), dl.Text, next[n])
		}
	}
}

func TestOrderedNumbersRightAlignPerRun(t *testing.T) {
	options := documentOptions()
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"8. eight\n9. nine\n10. ten\n11) eleven", []string{" 8. eight", " 9. nine", "10. ten", "11) eleven"}},
		// A blank or other line ends the run.
		{"9. a\n\n10. b\ntext\n100. c", []string{"9. a", "", "10. b", "text", "100. c"}},
		// Deeper items continue the outer run; a nested run aligns alone.
		{"9. a\n   - b\n     8. x\n     10. y\n10. c", []string{" 9. a", "   • b", "      8. x", "     10. y", "10. c"}},
		// An unordered item at the same indentation ends the run.
		{"9. a\n- b\n10. c", []string{"9. a", "• b", "10. c"}},
		// Written numbers and delimiters are kept, never renumbered.
		{"3) c\n1. a\n0010. z", []string{"   3) c", "   1. a", "0010. z"}},
	} {
		doc := Parse(tc.text, options)
		rows, got := painted(t, doc, options, 40)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%q:\n got %q\nwant %q", tc.text, got, tc.want)
		}
		if RenderText(doc) != strings.ReplaceAll(tc.text, "- ", "• ") {
			t.Fatalf("display copy changed: %q", RenderText(doc))
		}
		checkCoverage(t, doc, rows)
	}
}

func TestListItemsWrapUnderTheirText(t *testing.T) {
	options := documentOptions()
	for _, tc := range []struct {
		text  string
		width int
		want  []string
	}{
		{"9. alpha beta gamma\n10. delta epsilon zeta eta", 16, []string{" 9. alpha beta", "    gamma", "10. delta", "    epsilon zeta", "    eta"}},
		{"- alpha beta gamma delta", 14, []string{"• alpha beta", "  gamma delta"}},
		{"  - alpha beta gamma delta", 14, []string{"  • alpha beta", "    gamma", "    delta"}},
		{"1. top item text\n   * nested words wrap here", 18, []string{"1. top item text", "   • nested words", "     wrap here"}},
		// Too narrow for a gutter: the item wraps from the row start, as before.
		{"    - alpha beta gamma", 12, []string{"    • alpha", "beta gamma"}},
	} {
		doc := Parse(tc.text, options)
		rows, got := painted(t, doc, options, tc.width)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%q at %d:\n got %q\nwant %q", tc.text, tc.width, got, tc.want)
		}
		checkCoverage(t, doc, rows)
	}
}

// Gutters are decorations: a press over one selects from the row's first
// byte, and nothing in them is copyable.
func TestListGutterHitsTheRowStart(t *testing.T) {
	options := documentOptions()
	doc := Parse("8. one\n10. alpha beta gamma", options)
	rows, _ := painted(t, doc, options, 14)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	if lo, hi := HitRun(options.Method, rows[0].Runs, 0, rows[0].End); lo != 0 || hi != 0 {
		t.Fatal("pad hit", lo, hi)
	}
	cont := rows[2]
	if lo, _ := HitRun(options.Method, cont.Runs, 1, cont.End); lo != cont.Start || cont.Runs[0].Copy {
		t.Fatal("continuation gutter hit", lo, cont.Start)
	}
}

func TestListWrapKeepsWideGraphemesAndTabs(t *testing.T) {
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		options := documentOptions()
		options.Method = wm
		text := "1. 👩‍💻👩‍💻👩‍💻👩‍💻👩‍💻 漢字漢字漢字 🇫🇷🇫🇷 é́ end\n10. a\tb\tc d e f g h i j k l m n o p\n  - x\ty z w v u t s r q p o n m"
		doc := Parse(text, options)
		if RenderText(doc) != RenderText(Parse(text, ParseOptions{Method: wm, Theme: options.Theme})) {
			t.Fatal("parse is not deterministic")
		}
		for _, width := range []int{12, 13, 20, 80} {
			rows, _ := painted(t, doc, options, width)
			checkCoverage(t, doc, rows)
		}
	}
}

func TestHorizontalRule(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "a\n  ---  \nb"
	doc := Parse(text, options)
	if !doc.Lines[1].Rule || !doc.Lines[1].Decor || doc.Lines[1].Text != "" || doc.Sources[1].Src != nil {
		t.Fatalf("rule line %+v", doc.Lines[1])
	}
	_, got := painted(t, doc, options, 12)
	if !slices.Equal(got, []string{"a", strings.Repeat("─", 12), "b"}) {
		t.Fatalf("rule rows %q", got)
	}
	if RenderText(doc) != "a\nb" {
		t.Fatalf("a rule is never copied: %q", RenderText(doc))
	}
	// Quote over the rule takes its original source; only the rule holds
	// no selectable bytes.
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	if got, ok := selection.Exact(0, 0, 2, 1, true, true); !ok || got != text {
		t.Fatal("quote across a rule", got, ok)
	}
	for _, other := range []string{"----", "- - -", "***", "___", "--", "--- x"} {
		if d := Parse(other, options); d.Lines[0].Rule || d.Lines[0].Decor {
			t.Fatalf("%q became a rule", other)
		}
	}
	if d := Parse("---", ParseOptions{Method: options.Method, Theme: options.Theme, Source: true}); d.Lines[0].Rule || d.Lines[0].Text != "---" {
		t.Fatal("source mode changed")
	}
	// A delimiter row is still a table's.
	d := Parse("a | b\n---|---\nc | d", options)
	if d.Lines[0].Table == nil || d.Lines[1].Rule {
		t.Fatalf("table precedence %+v", d.Lines)
	}
	if d := Parse("```\n---\n```", options); d.Lines[1].Rule || d.Lines[1].Text != "---" {
		t.Fatal("a fenced --- stays code")
	}
}

func TestAngleAutolinks(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "see <https://example.org/a>, <http://x.y>foo <mailto:a@b> <div> <https://> <https://a b> `<https://code>` https://bare"
	doc := Parse(text, options)
	dl := doc.Lines[0]
	if want := "see https://example.org/a, http://x.yfoo <mailto:a@b> <div> <https://> <https://a b> <https://code> https://bare"; dl.Text != want {
		t.Fatalf("text %q", dl.Text)
	}
	links := map[string]string{}
	for _, p := range dl.Pieces {
		if p.Act.Kind == IntentURL {
			links[dl.Text[p.Start:p.End]] = p.Act.Value
		}
	}
	// Rejected autolinks stay literal text, where the existing bare-URL
	// rule still applies ("https://>" and "https://a"); "foo" after an
	// autolink is not part of it.
	want := map[string]string{"https://example.org/a": "https://example.org/a", "http://x.y": "http://x.y", "https://bare": "https://bare", "https://": "https://", "https://a": "https://a"}
	if !maps.Equal(links, want) {
		t.Fatalf("links %q", links)
	}
	auto := 0
	for _, p := range dl.Pieces {
		if p.Auto {
			auto++
		}
	}
	if auto != 2 {
		t.Fatal("autolink pieces", auto)
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	at := strings.Index(dl.Text, "https://example.org/a")
	if got, ok := selection.Exact(0, at, 0, at+len("https://example.org/a"), true, true); !ok || got != "<https://example.org/a>" {
		t.Fatal("autolink source", got, ok)
	}
	// In a table cell too.
	table := Parse("| A |\n| - |\n| <https://t.example> |", options)
	if table.Lines[2].Text != "https://t.example" || table.Lines[2].Pieces[0].Act != (Action{Kind: IntentURL, Value: "https://t.example"}) {
		t.Fatalf("cell %+v", table.Lines[2])
	}
}

func TestHeadingHierarchy(t *testing.T) {
	for _, dark := range []bool{false, true} {
		theme := spaceview.NewTheme(dark, nil, nil)
		doc := Parse("# one\n## two\n### three\n#### four\n##### five\n###### six\n####### seven", ParseOptions{Method: ansi.GraphemeWidth, Theme: theme})
		want := []spaceview.Color{theme.Brand, theme.Text, theme.Label, theme.Brand, theme.Brand, theme.Brand}
		for n, fg := range want {
			st := doc.Lines[n].Pieces[0].Style
			if st.FG != fg || !st.Bold {
				t.Fatalf("dark=%v level %d style %+v", dark, n+1, st)
			}
		}
		if st := doc.Lines[6].Pieces[0].Style; st.Bold || doc.Lines[6].Text != "####### seven" {
			t.Fatal("seven hashes are not a heading", st)
		}
	}
}

// naiveSeparators is the direct statement of tableSeparators: inline
// Markdown's code-span pairing, searching again from each opener.
func naiveSeparators(t string) []int {
	var out []int
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '\\':
			i++
		case '|':
			out = append(out, i)
		case '`':
			n := countRun(t[i:], '`')
			if end := strings.Index(t[i+n:], strings.Repeat("`", n)); end >= 0 && countRun(t[i+n+end:], '`') == n {
				i += n + end + n - 1
			} else {
				i += n - 1
			}
		}
	}
	return out
}

func TestTableSeparatorsMatchInlineCodeSpans(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := "``||\\ a"
	for range 20000 {
		b := make([]byte, rng.IntN(24))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		var got []int
		tableSeparators(string(b), func(i int) bool {
			got = append(got, i)
			return true
		})
		if want := naiveSeparators(string(b)); !slices.Equal(got, want) {
			t.Fatalf("%q: %v, want %v", b, got, want)
		}
	}
	// Each cell renders its code span as inline Markdown does.
	cells, ok := TableCells("| `a|b` | ``c`|d`` | `e | f |")
	if !ok || !slices.Equal(cells, []string{"`a|b`", "``c`|d``", "`e", "f"}) {
		t.Fatalf("cells %q", cells)
	}
	for _, c := range cells[:2] {
		if text, spans := InlineMarkdown(c, spaceview.Style{}, spaceview.Theme{}); len(spans) != 1 || !spans[0].Code || !strings.Contains(text, "|") {
			t.Fatalf("cell %q renders %q", c, text)
		}
	}
}

// Openers of decreasing length, each closed only far away, cost a rescan
// per opener when searched naively; one pass handles a long line.
func TestTableSeparatorsLongLine(t *testing.T) {
	var b strings.Builder
	for n := 300; n > 0; n-- {
		b.WriteString(strings.Repeat("`", n) + " | ")
	}
	b.WriteString(strings.Repeat("`", 301))
	line := strings.Repeat(b.String(), 20)
	count := 0
	tableSeparators(line, func(int) bool { count++; return true })
	if want := len(naiveSeparators(line)); count != want {
		t.Fatal(count, want)
	}
	if !TableRow("x | `a|b`") || TableRow("x `a|b`") {
		t.Fatal("a pipe only inside code does not continue a table")
	}
}
