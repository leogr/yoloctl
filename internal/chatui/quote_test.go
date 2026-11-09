package chatui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// highlight sets a drag highlight over message id from display line la,
// byte oa to line lb, byte ob, as a finished drag leaves it.
func highlight(m *Model, id string, la, oa, lb, ob int) {
	key := "server:" + id
	m.sel = &selection{anchor: selPoint{key: key, line: la, lo: oa, hi: oa}, focus: selPoint{key: key, line: lb, lo: ob, hi: ob}}
}

// highlightText highlights the first occurrence of part on display line
// line of message id.
func highlightText(t *testing.T, m *Model, id string, line int, part string) {
	t.Helper()
	i := m.indexOf("server:" + id)
	text := m.body(&m.messages[i], 80).lines[line].Text
	at := strings.Index(text, part)
	if at < 0 {
		t.Fatalf("line %d of %s is %q, without %q", line, id, text, part)
	}
	highlight(m, id, line, at, line, at+len(part))
}

func quoteModel(t *testing.T, text string) *Model {
	t.Helper()
	m, _, _ := filterModel(t)
	arrive(m, refTarget, "mo", "opus", "a loaded target") // previewed where referenced
	arrive(m, "q", "mq", "qa", text)
	m.View()
	return m
}

// Quote puts the original Markdown of the highlighted part in the draft as
// one quoted block with a blank line after it, replies to that message,
// clears the highlight and focuses the composer, without the clipboard.
func TestQuoteInsertsTheSourceAndReplies(t *testing.T) {
	m := quoteModel(t, "intro **bold** and [docs](https://x.y) end")
	m.opts.Clipboard, m.copyOnSelect = "off", false
	highlightText(t, m, "q", 0, "bold")
	m.focus = focusMessages
	press(m, 'q', tea.ModAlt)
	if m.Draft() != "> **bold**\n\n" || m.replyTo != "q" || m.sel != nil || m.focus != focusComposer {
		t.Fatalf("draft %q reply %q sel %v focus %v", m.Draft(), m.replyTo, m.sel, m.focus)
	}
	if !strings.Contains(m.notice, "Quoted the source of 1 line of qa's message from") || !strings.Contains(m.notice, "replying to it") {
		t.Fatalf("notice: %q", m.notice)
	}
	for _, tc := range []struct{ part, want string }{
		{"docs (https://x.y)", "[docs](https://x.y)"},
		{"docs", "docs"},
		{"intro bold", "intro **bold**"},
		{"bo", "bo"},
	} {
		_ = m.SetDraft("")
		highlightText(t, m, "q", 0, tc.part)
		m.quoteSelection()
		if m.Draft() != "> "+tc.want+"\n\n" {
			t.Fatalf("%q quotes %q", tc.part, m.Draft())
		}
	}
}

// A highlight whose source would render differently, or that cuts text the
// view writes in place of the source, is refused and nothing changes.
func TestQuoteRefusesWhatItCannotQuoteExactly(t *testing.T) {
	m := quoteModel(t, "intro **bold** and [docs](https://x.y) end")
	m.replyTo = "older"
	_ = m.SetDraft("keep")
	for _, part := range []string{"intro bo", "docs (https", "(https://x.y) end"} {
		highlightText(t, m, "q", 0, part)
		m.quoteSelection()
		if m.Draft() != "keep" || m.replyTo != "older" || m.sel == nil || m.notice != quoteCut {
			t.Fatalf("%q: draft %q reply %q notice %q", part, m.Draft(), m.replyTo, m.notice)
		}
	}
	m.source = true
	m.View()
	highlightText(t, m, "q", 0, "intro **bo")
	m.quoteSelection()
	if m.Draft() != "keep\n> intro **bo\n\n" {
		t.Fatalf("source view quotes any part exactly: %q", m.Draft())
	}
}

// Line markers stay when the highlight starts at the line's start; several
// lines quote as several > lines, inserted on a line of their own.
func TestQuoteKeepsLineMarkersAndPlacesTheBlock(t *testing.T) {
	m := quoteModel(t, "- item one\n> quoted\n## Title\n\tindented")
	_ = m.SetDraft("hello")
	highlight(m, "q", 0, 0, 3, len(m.body(&m.messages[m.indexOf("server:q")], 80).lines[3].Text))
	m.quoteSelection()
	want := "hello\n> - item one\n> > quoted\n> ## Title\n> \tindented\n\n"
	if m.Draft() != want {
		t.Fatalf("draft %q, want %q", m.Draft(), want)
	}
	if !strings.Contains(m.notice, "4 lines") {
		t.Fatalf("notice: %q", m.notice)
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != "hello" || m.replyTo != "q" {
		t.Fatalf("one Ctrl+Z removes the whole quote, the reply stays: %q %q", m.Draft(), m.replyTo)
	}
	highlightText(t, m, "q", 0, "item")
	m.quoteSelection()
	if m.Draft() != "hello\n> item\n\n" {
		t.Fatalf("mid-line: %q", m.Draft())
	}
}

// A highlight inside a fenced block quotes a fenced block; one crossing a
// fence edge gets the missing opener or closer; frame rows include theirs.
func TestQuoteKeepsFencesBalanced(t *testing.T) {
	m := quoteModel(t, "see:\n```go\nfmt.Println(1)\nx := 2\n```\nafter")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if !lines[1].Decor || !lines[4].Decor {
		t.Fatalf("fixture frames: %+v", lines)
	}
	for _, tc := range []struct {
		la, oa, lb, ob int
		want           string
	}{
		{2, 4, 3, 1, "```go\nPrintln(1)\nx\n```"},
		{0, 0, 2, 3, "see:\n```go\nfmt\n```"},
		{3, 0, 5, 5, "```go\nx := 2\n```\nafter"},
		{1, 0, 4, 0, "```go\nfmt.Println(1)\nx := 2\n```"},
	} {
		_ = m.SetDraft("")
		highlight(m, "q", tc.la, tc.oa, tc.lb, tc.ob)
		m.quoteSelection()
		quoted := strings.ReplaceAll(strings.TrimSuffix(m.Draft(), "\n\n"), "> ", "")
		if quoted != tc.want {
			t.Fatalf("%+v quotes %q (notice %q)", tc, m.Draft(), m.notice)
		}
	}
	unclosed := quoteModel(t, "```sh\nmake check")
	highlightText(t, unclosed, "q", 1, "check")
	unclosed.quoteSelection()
	if unclosed.Draft() != "> ```sh\n> check\n> ```\n\n" {
		t.Fatalf("an unclosed fence closes with its marker: %q", unclosed.Draft())
	}
}

// A table quotes as its selected cell text, whole or in part (see
// quote_table_test.go); a rejected table's lines that would render as a
// table on their own are refused.
func TestQuoteTables(t *testing.T) {
	m := quoteModel(t, "| a | b |\n|---|--:|\n| 1 | 22 |\nafter")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	highlight(m, "q", 0, 0, 2, len(lines[2].Text))
	m.quoteSelection()
	if m.Draft() != "> a\tb\n> 1\t22\n\n" {
		t.Fatalf("whole table: %q (%q)", m.Draft(), m.notice)
	}
	_ = m.SetDraft("")
	highlight(m, "q", 0, 0, 2, 3)
	m.quoteSelection()
	if m.Draft() != "> a\tb\n> 1\n\n" {
		t.Fatalf("part of a table: %q %q", m.Draft(), m.notice)
	}
	rows := strings.Repeat("| x | y |\n", maxTableRows+1)
	rejected := quoteModel(t, "| a | b |\n|---|---|\n"+rows+"end")
	highlight(rejected, "q", 0, 0, 1, len("|---|---|"))
	rejected.quoteSelection()
	if rejected.Draft() != "" || rejected.notice != quoteCut {
		t.Fatalf("two lines of a rejected table render as a table alone: %q %q", rejected.Draft(), rejected.notice)
	}
}

// Every other refusal has its own notice and changes nothing.
func TestQuoteRefusals(t *testing.T) {
	m, _, sends := filterModel(t)
	arrive(m, "a", "mq", "qa", "first")
	arrive(m, "b", "mo", "opus", "second")
	arrive(m, "f", "mo", "opus", "a file")
	m.messages[m.indexOf("server:f")].Artifact = &ArtifactRef{ID: "0f8fad5b-d9cb-469f-a165-70867728950e", Name: "x.png", Bytes: 3}
	m.View()
	m.replyTo = "a"
	_ = m.SetDraft("keep")
	check := func(what, notice string) {
		t.Helper()
		m.quoteSelection()
		if m.Draft() != "keep" || m.replyTo != "a" || m.notice != notice {
			t.Fatalf("%s: draft %q reply %q notice %q", what, m.Draft(), m.replyTo, m.notice)
		}
	}
	m.sel = nil
	check("no highlight", quoteNoHighlight)
	m.sel = &selection{anchor: selPoint{key: "server:a"}, focus: selPoint{key: "server:b", line: 0, lo: 3, hi: 3}}
	check("several messages", quoteSeveral)
	highlight(m, "f", 0, 0, 0, 2)
	check("a file chip", quoteChips)
	m.editor.limit = 8
	highlight(m, "b", 0, 0, 0, 6)
	check("over the limit", "The quote does not fit: draft exceeds the message byte limit; nothing changed")
	m.editor.limit = 1 << 16
	_ = m.SetDraft("")
	paste(m, "pending")
	m.focus = focusComposer
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatal("the fixture send")
	}
	key := messageKey(m.messages[len(m.messages)-1])
	m.sel = &selection{anchor: selPoint{key: key}, focus: selPoint{key: key, lo: 3, hi: 3}}
	_ = m.SetDraft("keep")
	check("unconfirmed", quoteUnconfirmed)
	highlight(m, "b", 0, 0, 0, 6)
	m.quoteSelection()
	if m.replyTo != "b" || !strings.Contains(m.notice, "instead of qa's message from") {
		t.Fatalf("a quote replaces another reply and says so: %q %q", m.replyTo, m.notice)
	}
}

// The Quote chip leads the footer while a highlight can be quoted, also
// beside the copy notice of a release, and a click quotes; a picked
// message offers it among its actions.
func TestQuoteChip(t *testing.T) {
	m := quoteModel(t, "tests pass\nsmoke pending")
	highlightText(t, m, "q", 1, "smoke pending")
	m.say(levelOK, "Copied 13 characters of displayed text · Esc clears the highlight")
	m.View()
	h, ok := hitWith(m, hitFooter, "quote")
	if !ok || !strings.Contains(view(m), "❝ Quote") {
		t.Fatalf("no chip:\n%s", view(m))
	}
	clickAt(m, h.x0, h.y)
	if m.Draft() != "> smoke pending\n\n" {
		t.Fatalf("chip click: %q", m.Draft())
	}
	m.View()
	if _, ok := hitWith(m, hitFooter, "quote"); ok {
		t.Fatal("no chip without a highlight")
	}
	m.notice = ""
	highlightText(t, m, "q", 0, "pass")
	if out := view(m); !strings.Contains(out, "Alt+Q quote") {
		t.Fatalf("hints:\n%s", out)
	}
	m.pick("server:q")
	var labels []string
	for _, a := range m.messageActions() {
		labels = append(labels, a.label)
	}
	if !strings.Contains(strings.Join(labels, ","), quoteLabel) {
		t.Fatalf("actions: %v", labels)
	}
	help := ""
	for _, section := range helpSections {
		for _, row := range section.rows {
			help += row[0] + " " + row[1] + "\n"
		}
	}
	if !strings.Contains(help, "Alt+Q  ❝ Quote Quote the highlighted part of one message") {
		t.Fatalf("help:\n%s", help)
	}
}

// quoteFixtures cover every construct the view renders, kept short so the
// all-pairs check stays fast.
var quoteFixtures = []string{
	"a **b** _c_ `d` ~~e~~ [f](g) \\* h\t!",
	"- one\n* two\n> three **x**\n## four\nfive",
	"x\n```go\nif a {\n\tb()\n}\n```\ny",
	"```\nopen",
	"| a | b |\n|:-|-:|\n| *1* | 22 |\nz",
	"p\n| **h** | i |\n|:-:|--:|\n| jk | `l` |",
	"ü😀 **é** é\r\nend\x07",
	"see " + sharedTarget + " **x**\n- msgid:" + refTarget,
}

// The mapping is an optional output of the line builder: the lines are the
// same, one source entry per line.
func TestQuoteMappingKeepsTheDisplay(t *testing.T) {
	m, _, _ := filterModel(t)
	for _, text := range quoteFixtures {
		for _, source := range []bool{false, true} {
			var srcs []lineSrc
			got := displayLinesMap(ansi.WcWidth, text, source, m.theme, "leogr", &srcs)
			if want := displayLines(ansi.WcWidth, text, source, m.theme, "leogr"); !reflect.DeepEqual(got, want) || len(srcs) != len(got) {
				t.Fatalf("%q source=%v: lines differ or %d sources for %d lines", text, source, len(srcs), len(got))
			}
		}
	}
}

// Every pair of grapheme endpoints, frame and rule rows included, in both
// views either quotes a slice of the source (plus a fence's own opener or
// closer) that renders to exactly the highlighted text, or refuses. One
// touching a rendered table instead quotes what tableOracle derives from the
// highlighted text, or nothing when that is empty.
func TestQuoteEveryHighlightIsExactOrRefused(t *testing.T) {
	for _, text := range quoteFixtures {
		for _, source := range []bool{false, true} {
			m := quoteModel(t, text)
			m.source = source
			m.View()
			i := m.indexOf("server:q")
			lines := m.body(&m.messages[i], 80).lines
			if strings.Contains(text, sharedTarget) && !source && (lines[0].Subs == nil || lines[1].Subs == nil) {
				t.Fatalf("%q: the fixture should show previews", text)
			}
			type point struct{ line, at int }
			var points []point
			for n, dl := range lines {
				for at := 0; ; {
					points = append(points, point{n, at})
					if at == len(dl.Text) {
						break
					}
					g, _ := ansi.FirstGraphemeCluster(dl.Text[at:], ansi.WcWidth)
					at += len(g)
				}
			}
			src := plain(text)
			sliced, tables := 0, 0
			for a, p := range points {
				for _, q := range points[a+1:] {
					highlight(m, "q", p.line, p.at, q.line, q.at)
					_, want, why := m.quoteTarget()
					if why != "" {
						continue
					}
					got, _, why := m.quoteSource(i)
					if oracle, ok := tableOracle(lines, p.line, p.at, q.line, q.at); ok {
						tables++
						if oracle == "" && why != quoteNoHighlight || oracle != "" && (why != "" || joinQuote(got) != oracle) {
							t.Fatalf("%q %v-%v: quotes %q (%q), want %q", text, p, q, joinQuote(got), why, oracle)
						}
						continue
					}
					if why != "" {
						continue
					}
					sliced++
					if r := rendered(joinQuote(got), source, m); r != strings.Trim(want, "\n") {
						t.Fatalf("%q %v %v-%v: %q renders %q, highlighted %q", text, source, p, q, joinQuote(got), r, want)
					}
					if !strings.Contains(src, joinQuote(got)) && !strings.Contains(src, trimFence(joinQuote(got))) {
						t.Fatalf("%q %v %v-%v: %q is not the source", text, source, p, q, joinQuote(got))
					}
				}
			}
			if sliced == 0 || source && tables > 0 {
				t.Fatalf("%q %v: %d quoted, %d over tables", text, source, sliced, tables)
			}
			// The whole message always quotes: as its source (an unclosed
			// fence closed), or with its tables as cell text.
			last := len(lines) - 1
			highlight(m, "q", 0, 0, last, len(lines[last].Text))
			got, _, why := m.quoteSource(i)
			if oracle, ok := tableOracle(lines, 0, 0, last, len(lines[last].Text)); ok {
				if why != "" || joinQuote(got) != oracle {
					t.Fatalf("%q: whole message quotes %q (%q), want %q", text, joinQuote(got), why, oracle)
				}
				continue
			}
			if why != "" || joinQuote(got) != strings.Trim(src, "\n") && !strings.HasPrefix(joinQuote(got), strings.Trim(src, "\n")+"\n```") {
				t.Fatalf("%q %v: whole message quotes %q (%q)", text, source, joinQuote(got), why)
			}
		}
	}
}

// joinQuote is the text of quoted lines.
func joinQuote(lines []quoteLine) string {
	texts := make([]string, len(lines))
	for k, l := range lines {
		texts[k] = l.Text
	}
	return strings.Join(texts, "\n")
}

// tableOracle derives a table quote from the highlighted text alone, for
// fixtures whose cells hold no spaces and whose prose shows its source: a
// row's highlighted text split at its separators, padding trimmed, with
// fields that were only padding or a separator dropped at either end, joined
// by TAB; other lines as highlighted; edge line breaks dropped. ok is false
// when the highlight touches no rendered table.
func tableOracle(lines []displayLine, la, oa, lb, ob int) (string, bool) {
	var out []string
	ok := false
	for n := la; n <= lb; n++ {
		dl := lines[n]
		ok = ok || dl.Table != nil
		if dl.Decor {
			continue
		}
		lo, hi := 0, len(dl.Text)
		if n == la {
			lo = oa
		}
		if n == lb {
			hi = ob
		}
		part := dl.Text[lo:hi]
		if dl.Table == nil {
			out = append(out, part)
			continue
		}
		fields := strings.Split(part, "│")
		for k := range fields {
			fields[k] = strings.TrimSpace(fields[k])
		}
		for len(fields) > 0 && fields[0] == "" {
			fields = fields[1:]
		}
		for len(fields) > 0 && fields[len(fields)-1] == "" {
			fields = fields[:len(fields)-1]
		}
		if len(fields) > 0 {
			out = append(out, strings.Join(fields, "\t"))
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n"), ok
}

// trimFence drops a fence line quoting added at either end.
func trimFence(q string) string {
	lines := strings.Split(q, "\n")
	if len(lines) > 0 && fenceMarker(strings.TrimLeft(lines[0], " ")) != "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && fenceMarker(strings.TrimLeft(lines[len(lines)-1], " ")) != "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
