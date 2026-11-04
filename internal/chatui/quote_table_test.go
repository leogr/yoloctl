package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// quoteTableExample has three columns; "gate" shows bold, without markers.
const quoteTableExample = "| Box | Stage | Next |\n|---|---|---|\n| opus2 | Review | merge after **gate** |\n| sol | Build | refresh fix |"

// highlightSpan highlights message id from the first occurrence of from on
// display line la through the end of the first occurrence of to on line lb.
func highlightSpan(t *testing.T, m *Model, id string, la int, from string, lb int, to string) {
	t.Helper()
	lines := m.body(&m.messages[m.indexOf("server:"+id)], max(m.lastTextWidth, 1)).lines
	a, b := strings.Index(lines[la].text, from), strings.Index(lines[lb].text, to)
	if a < 0 || b < 0 {
		t.Fatalf("%q on line %d (%q) or %q on line %d (%q) is missing", from, la, lines[la].text, to, lb, lines[lb].text)
	}
	highlight(m, id, la, a, lb, b+len(to))
}

// quoted quotes the current highlight into an empty draft.
func quoted(m *Model) string {
	_ = m.SetDraft("")
	m.quoteSelection()
	return m.Draft()
}

// The confirmed examples: the selected cell text as shown, cells joined by
// one real TAB, one line per row, no table Markdown, header only when
// selected.
func TestQuoteTableExamples(t *testing.T) {
	m := quoteModel(t, quoteTableExample)
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if len(lines) != 4 || !lines[1].decor || lines[2].cells == nil {
		t.Fatalf("fixture: %+v", lines)
	}
	for _, tc := range []struct {
		la       int
		from     string
		lb       int
		to, want string
	}{
		{2, "Revi", 2, "Revi", "> Revi\n\n"},
		{2, "pus2", 2, "Revi", "> pus2\tRevi\n\n"},
		{2, "merge after ga", 2, "merge after ga", "> merge after ga\n\n"},
		{2, "merge after gate", 2, "merge after gate", "> merge after gate\n\n"},
		{2, "Review", 3, "sol", "> Review\tmerge after gate\n> sol\n\n"},
		{0, "Stage", 0, "Stage", "> Stage\n\n"},
		{0, "Box", 3, "refresh fix", "> Box\tStage\tNext\n> opus2\tReview\tmerge after gate\n> sol\tBuild\trefresh fix\n\n"},
	} {
		highlightSpan(t, m, "q", tc.la, tc.from, tc.lb, tc.to)
		if got := quoted(m); got != tc.want {
			t.Fatalf("%q..%q quotes %q, want %q (%q)", tc.from, tc.to, got, tc.want, m.notice)
		}
		if !strings.Contains(m.notice, "Quoted selected table text of qa's message from") || !strings.Contains(m.notice, "replying to it") || m.replyTo != "q" {
			t.Fatalf("notice %q reply %q", m.notice, m.replyTo)
		}
	}
	// The whole message converts its table too; the source blank lines
	// around it stay, the prose quotes as its source.
	w := quoteModel(t, "intro **x**\n\n"+quoteTableExample+"\n\nafter `c`")
	wl := w.body(&w.messages[w.indexOf("server:q")], 80).lines
	highlight(w, "q", 0, 0, len(wl)-1, len(wl[len(wl)-1].text))
	if got := quoted(w); got != "> intro **x**\n>\n> Box\tStage\tNext\n> opus2\tReview\tmerge after gate\n> sol\tBuild\trefresh fix\n>\n> after `c`\n\n" {
		t.Fatalf("whole message: %q (%q)", got, w.notice)
	}
}

// Cell text quotes as displayed, also cut inside formatting that source
// Quote refuses: literal Markdown-looking bytes stay, nothing is escaped or
// restored. Prose keeps its exactness check.
func TestQuoteTableKeepsDisplayedText(t *testing.T) {
	m := quoteModel(t, "| a | b |\n|---|---|\n| **bold** [docs](https://x.y) | `co*de*` \\| x |")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if !strings.Contains(lines[2].text, "bold docs (https://x.y)") || !strings.Contains(lines[2].text, "co*de* | x") {
		t.Fatalf("fixture: %q", lines[2].text)
	}
	for _, tc := range []struct{ part, want string }{
		{"ld docs (htt", "ld docs (htt"},
		{"s://x.y)", "s://x.y)"},
		{"e* | x", "e* | x"},
		{"co*de* | x", "co*de* | x"},
	} {
		highlightText(t, m, "q", 2, tc.part)
		if got := quoted(m); got != "> "+tc.want+"\n\n" {
			t.Fatalf("%q quotes %q (%q)", tc.part, got, m.notice)
		}
	}
	p := quoteModel(t, "intro **bold**")
	highlightText(t, p, "q", 0, "intro bo")
	if got := quoted(p); got != "" || p.notice != quoteCut {
		t.Fatalf("prose cut: %q %q", got, p.notice)
	}
}

// emptyCells has empty cells inside, first and last, and an empty row.
const emptyCells = "| a | b | c |\n|---|---|---|\n| one |  | three |\n|  |  |  |\n| x | y | z |\n|  | mid |  |"

// Empty cells between selected fields stay empty fields; edge cells keep
// only their selected part; an empty row's TABs survive the quote block; a
// whole row keeps its empty edge fields; padding and separators at either
// end add nothing and alone quote nothing; a selection ending at a row's
// offset zero leaves that row out.
func TestQuoteTableCellEdges(t *testing.T) {
	m := quoteModel(t, emptyCells)
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	for _, tc := range []struct {
		la, oa, lb, ob int
		want           string
	}{
		{2, 0, 2, len(lines[2].text), "> one\t\tthree\n\n"},
		{2, 1, 2, strings.Index(lines[2].text, "three") + 3, "> ne\t\tthr\n\n"},
		{2, strings.Index(lines[2].text, "three"), 4, 1, "> three\n> \t\t\n> x\n\n"},
		{3, 0, 3, len(lines[3].text), "> \t\t\n\n"},
		{5, 0, 5, len(lines[5].text), "> \tmid\t\n\n"},
		{4, 2, 4, strings.Index(lines[4].text, "y") + 1, "> y\n\n"},
		{4, 0, 4, strings.Index(lines[4].text, "y"), "> x\n\n"},
		{2, strings.Index(lines[2].text, "three"), 3, 0, "> three\n\n"},
		{2, strings.Index(lines[2].text, "three"), 4, 0, "> three\n> \t\t\n\n"},
	} {
		for _, reverse := range []bool{false, true} {
			highlight(m, "q", tc.la, tc.oa, tc.lb, tc.ob)
			if reverse {
				m.sel.anchor, m.sel.focus = m.sel.focus, m.sel.anchor
				m.sel.anchor.lo, m.sel.anchor.hi, m.sel.focus.lo, m.sel.focus.hi = tc.ob, tc.ob, tc.oa, tc.oa
			}
			if got := quoted(m); got != tc.want || strings.Contains(got, "│") {
				t.Fatalf("%+v reverse=%v quotes %q, want %q (%q)", tc, reverse, got, tc.want, m.notice)
			}
		}
	}
	// Only padding and a separator: no quote, nothing changes.
	m.replyTo = "older"
	_ = m.SetDraft("keep")
	sep := strings.Index(lines[4].text, "│")
	highlight(m, "q", 4, 1, 4, sep+len("│ "))
	m.quoteSelection()
	if m.Draft() != "keep" || m.replyTo != "older" || m.sel == nil || m.notice != quoteNoHighlight {
		t.Fatalf("separator only: draft %q reply %q notice %q", m.Draft(), m.replyTo, m.notice)
	}
	// A component of only the rule contributes nothing.
	if rows := tableText(lines, 1, 1, 0, len(lines[1].text)); rows != nil {
		t.Fatalf("the rule gave %+v", rows)
	}
}

// Prose and tables quote in order with no added separators: prose as its
// source, a fence balanced, tables as cell text; an endpoint on the rule
// keeps the header it passed; an unmappable prose endpoint refuses the whole
// quote.
func TestQuoteTableMixed(t *testing.T) {
	for _, tc := range []struct {
		text           string
		la             int
		from           string
		lb             int
		to, want, note string
	}{
		{"intro **b**\n" + quoteTableExample, 0, "intro", 3, "pus2", "> intro **b**\n> Box\tStage\tNext\n> opus2\n\n", "prose into a table"},
		{quoteTableExample + "\nmid\n" + quoteTableExample, 3, "sol", 5, "Box", "> sol\tBuild\trefresh fix\n> mid\n> Box\n\n", "two tables"},
		{"```go\nx := 1\n```\n" + quoteTableExample, 1, "1", 5, "Revi", "> ```go\n> 1\n> ```\n> Box\tStage\tNext\n> opus2\tRevi\n\n", "fenced prose"},
		{quoteTableExample + "\nafter **it**", 3, "Build", 4, "after **it**"[:5], "> Build\trefresh fix\n> after\n\n", "table into prose"},
	} {
		m := quoteModel(t, tc.text)
		lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
		la := strings.Index(lines[tc.la].text, tc.from)
		lb := strings.Index(lines[tc.lb].text, tc.to)
		if la < 0 || lb < 0 {
			t.Fatalf("%s: fixture lines %+v", tc.note, lines)
		}
		highlight(m, "q", tc.la, la, tc.lb, lb+len(tc.to))
		if got := quoted(m); got != tc.want {
			t.Fatalf("%s: %q, want %q (%q)", tc.note, got, tc.want, m.notice)
		}
	}
	// The rule as either endpoint: what was passed of the header stays.
	m := quoteModel(t, "intro\n"+quoteTableExample)
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if !lines[2].decor {
		t.Fatalf("fixture: %+v", lines)
	}
	highlight(m, "q", 0, 0, 2, 3)
	if got := quoted(m); got != "> intro\n> Box\tStage\tNext\n\n" {
		t.Fatalf("prose to the rule: %q (%q)", got, m.notice)
	}
	highlight(m, "q", 2, 0, 3, strings.Index(lines[3].text, "Revi")+4)
	if got := quoted(m); got != "> opus2\tRevi\n\n" {
		t.Fatalf("rule to a row: %q (%q)", got, m.notice)
	}
	// An endpoint on the header's offset zero, as a press on the spacer
	// before the table gives, quotes only the prose before it.
	highlight(m, "q", 0, 0, 1, 0)
	if got := quoted(m); got != "> intro\n\n" || !strings.Contains(m.notice, "Quoted the source of 1 line") {
		t.Fatalf("prose to the spacer: %q (%q)", got, m.notice)
	}
	// One prose endpoint inside a link's address refuses everything.
	r := quoteModel(t, "see [docs](https://x.y)\n"+quoteTableExample)
	rl := r.body(&r.messages[r.indexOf("server:q")], 80).lines
	r.replyTo = "older"
	_ = r.SetDraft("keep")
	highlight(r, "q", 0, strings.Index(rl[0].text, "https"), 3, 3)
	r.quoteSelection()
	if r.Draft() != "keep" || r.replyTo != "older" || r.sel == nil || r.notice != quoteCut {
		t.Fatalf("unmappable prose: draft %q reply %q notice %q", r.Draft(), r.replyTo, r.notice)
	}
}

// The same logical selection quotes the same text in the aligned, fitted
// and fallback layouts and after a resize; a real drag over a wrapped table
// quotes its cells while Copy still copies the displayed bytes.
func TestQuoteTableLayouts(t *testing.T) {
	key := "server:t"
	for _, width := range []int{200, 24, 6} {
		m := tableModel(width, quoteTableExample)
		lines := m.body(&m.messages[m.indexOf(key)], max(m.lastTextWidth, 1)).lines
		for _, tc := range []struct {
			la       int
			from     string
			lb       int
			to, want string
		}{
			{2, "pus2", 2, "Revi", "> pus2\tRevi\n\n"},
			{2, "Review", 3, "sol", "> Review\tmerge after gate\n> sol\n\n"},
		} {
			a, b := strings.Index(lines[tc.la].text, tc.from), strings.Index(lines[tc.lb].text, tc.to)
			highlight(m, "t", tc.la, a, tc.lb, b+len(tc.to))
			if got := quoted(m); got != tc.want {
				t.Fatalf("width %d: %q, want %q (%q)", width, got, tc.want, m.notice)
			}
			highlight(m, "t", tc.la, a, tc.lb, b+len(tc.to))
			m.Update(tea.WindowSizeMsg{Width: 200 + 24 - width, Height: 16})
			m.View()
			if got := quoted(m); got != tc.want {
				t.Fatalf("width %d resized: %q, want %q", width, got, tc.want)
			}
			m.Update(tea.WindowSizeMsg{Width: width, Height: 16})
			m.View()
		}
	}
	m, copied := copyingModel(24, 30)
	m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: quoteTableExample}})
	m.View()
	lines := m.body(&m.messages[m.indexOf(key)], max(m.lastTextWidth, 1)).lines
	if wrapped := bodyRows(ansi.WcWidth, lines, m.lastTextWidth, m.theme); len(wrapped) <= len(lines) {
		t.Fatalf("the table should wrap at 24: %d rows", len(wrapped))
	}
	row := lines[2].text
	x0, y0 := cellOf(t, m, key, 2, strings.Index(row, "Review"))
	x1, y1 := cellOf(t, m, key, 2, strings.Index(row, "gate")+3)
	if y1 == y0 {
		t.Fatalf("the drag should end on a wrapped row")
	}
	drag(m, x0, y0, x1, y1)
	if got := (*copied)[len(*copied)-1]; got != row[strings.Index(row, "Review"):] {
		t.Fatalf("copy changed: %q", got)
	}
	if got := quoted(m); got != "> Review\tmerge after gate\n\n" {
		t.Fatalf("wrapped drag quotes %q (%q)", got, m.notice)
	}
}

// Pipes inside backticks split cells, as the parser has always done; an
// escaped pipe stays in its cell.
func TestQuoteTableCodeSpanPipes(t *testing.T) {
	m := quoteModel(t, "| A | B | C | D |\n|---|---|---|---|\n| prefix | `left|right` | suffix |\n| p | a \\| b | c | d |")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if len(lines[2].cells) != 4 {
		t.Fatalf("the parser should split the code span: %q %v", lines[2].text, lines[2].cells)
	}
	highlightSpan(t, m, "q", 2, "`left", 2, "right`")
	if got := quoted(m); got != "> `left\tright`\n\n" {
		t.Fatalf("code-span pipe: %q (%q)", got, m.notice)
	}
	highlightSpan(t, m, "q", 3, "a | b", 3, "c")
	if got := quoted(m); got != "> a | b\tc\n\n" {
		t.Fatalf("escaped pipe: %q (%q)", got, m.notice)
	}
}

// A table quote is one editor insert at the cursor: one undo, the reply
// target, no clipboard and no send; over the byte or line limit nothing
// changes.
func TestQuoteTableEditor(t *testing.T) {
	m, _, sends := filterModel(t)
	arrive(m, "q", "mq", "qa", quoteTableExample)
	m.View()
	copies := 0
	m.opts.Copy = func(string) tea.Cmd { copies++; return nil }
	_ = m.SetDraft("hello world")
	m.editor.cursor = len("hello")
	highlightSpan(t, m, "q", 2, "Review", 3, "sol")
	m.quoteSelection()
	if want := "hello\n> Review\tmerge after gate\n> sol\n\n world"; m.Draft() != want || m.replyTo != "q" || m.sel != nil || m.focus != focusComposer {
		t.Fatalf("draft %q reply %q sel %v focus %v", m.Draft(), m.replyTo, m.sel, m.focus)
	}
	if copies != 0 || len(*sends) != 0 {
		t.Fatalf("copies %d sends %d", copies, len(*sends))
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != "hello world" {
		t.Fatalf("one Ctrl+Z removes the quote: %q", m.Draft())
	}
	for _, tc := range []struct {
		draft string
		limit int
		why   string
	}{
		{"keep", 12, "The quote does not fit: draft exceeds the message byte limit; nothing changed"},
		{strings.Repeat("\n", maxDraftLines-3), 1 << 20, "The quote does not fit: draft exceeds the 8192-line editor limit; nothing changed"},
	} {
		m.editor.limit = 1 << 20
		_ = m.SetDraft(tc.draft)
		m.editor.limit = tc.limit
		m.replyTo = "older"
		cursor := m.editor.cursor
		highlightSpan(t, m, "q", 2, "Review", 3, "sol")
		m.quoteSelection()
		if m.Draft() != tc.draft || m.editor.cursor != cursor || m.replyTo != "older" || m.sel == nil || m.notice != tc.why {
			t.Fatalf("limit %d: draft %d bytes cursor %d reply %q notice %q", tc.limit, len(m.Draft()), m.editor.cursor, m.replyTo, m.notice)
		}
	}
}

// Prose beside a table quotes whole source lines on that side: a line that
// renders empty keeps its marker, a fence keeps its blank code lines and an
// empty fenced block stays a block. At the highlight's own ends, line breaks
// still quote nothing.
func TestQuoteTableKeepsSourceBesideTables(t *testing.T) {
	rows := "> Box\tStage\tNext\n> opus2\tReview\tmerge after gate\n> sol\tBuild\trefresh fix\n"
	for _, tc := range []struct{ name, text, want string }{
		{"empty heading after", quoteTableExample + "\n# \nafter", rows + "> # \n> after\n\n"},
		{"empty heading before", "# \n" + quoteTableExample, "> # \n" + rows + "\n"},
		{"empty quote marker", quoteTableExample + "\n>\nafter", rows + "> >\n> after\n\n"},
		{"leading blank code line", quoteTableExample + "\n```\n\ncode\n```", rows + "> ```\n>\n> code\n> ```\n\n"},
		{"trailing blank code line", quoteTableExample + "\n```\ncode\n\n```", rows + "> ```\n> code\n>\n> ```\n\n"},
		{"empty fenced block", quoteTableExample + "\n```\n\n```\nafter", rows + "> ```\n>\n> ```\n> after\n\n"},
		{"blank between tables", quoteTableExample + "\n\n" + quoteTableExample, rows + ">\n" + rows + "\n"},
	} {
		m := quoteModel(t, tc.text)
		lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
		last := len(lines) - 1
		highlight(m, "q", 0, 0, last, len(lines[last].text))
		if got := quoted(m); got != tc.want {
			t.Fatalf("%s: %q, want %q (%q)", tc.name, got, tc.want, m.notice)
		}
	}
	// A highlight starting at a prose line's end, or ending at the start of
	// the line after the table, adds no line there.
	m := quoteModel(t, "intro\n"+quoteTableExample+"\nafter")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	highlight(m, "q", 0, len("intro"), 4, len(lines[4].text))
	if got := quoted(m); got != rows+"\n" {
		t.Fatalf("from the prose line's end: %q (%q)", got, m.notice)
	}
	highlight(m, "q", 1, 0, 5, 0)
	if got := quoted(m); got != rows+"\n" {
		t.Fatalf("to the next line's start: %q (%q)", got, m.notice)
	}
}
