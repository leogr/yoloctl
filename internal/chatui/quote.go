package chatui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// quoteKey runs Quote; the footer chip names it.
const quoteKey, quoteLabel = "alt+q", "❝ Quote"

// Quote refusals, one per reason; nothing changes when one is given.
const (
	quoteNoHighlight = "Drag over message text to quote it"
	quoteSeveral     = "Select text inside one message to quote it"
	quoteUnconfirmed = "Not confirmed yet; you can quote it once the room echoes it"
	quoteCut         = "The highlight starts or ends inside formatting the view adds; widen it to whole words or use source view (Ctrl+S)"
	quoteChips       = "Files and images show as chips; use source view (Ctrl+S) to quote this message"
)

// quoteTarget is the message the highlight lies in and its highlighted
// text, or the reason it cannot be quoted.
func (m *Model) quoteTarget() (int, string, string) {
	if m.sel == nil {
		return -1, "", quoteNoHighlight
	}
	parts := m.selectedParts(*m.sel)
	switch {
	case len(parts) == 0 || len(parts) == 1 && parts[0].text == "":
		return -1, "", quoteNoHighlight
	case len(parts) > 1:
		return -1, "", quoteSeveral
	case m.messages[parts[0].index].ID == "":
		return -1, "", quoteUnconfirmed
	}
	return parts[0].index, parts[0].text, ""
}

// quotable reports whether Quote would act now, so the chip is offered.
func (m *Model) quotable() bool {
	_, _, why := m.quoteTarget()
	return why == ""
}

// quoteSource returns what Quote inserts for the highlighted part of message
// i, line by line, and whether a rendered table contributed to it. Outside a
// rendered table it is the original Markdown: one contiguous slice of the
// message's plain text, with markup touching either end included when it
// still renders the same, and the opener or closer of a fence the slice
// starts or ends inside. The slice must render to exactly the highlighted
// text; otherwise it explains the refusal and nothing is approximated. A
// rendered table contributes its selected cell text instead (see
// tableText), and the text around it quotes as its own slices.
func (m *Model) quoteSource(i int) ([]quoteLine, bool, string) {
	msg := &m.messages[i]
	if !m.source && (msg.Artifact != nil || len(msg.Attachments) > 0 && attachmentText(msg) != msg.Text) {
		return nil, false, quoteChips
	}
	var srcs []lineSrc
	displayLinesMap(m.widthMethod, msg.Text, m.source, m.theme, m.self, &srcs)
	lines := m.body(msg, max(m.lastTextWidth, 1)).lines // with previews
	if len(lines) != len(srcs) {
		return nil, false, quoteCut
	}
	for n := range srcs {
		if lines[n].Subs != nil && srcs[n].Src != nil {
			// A preview maps only whole, to its token.
			srcs[n].Src = subMap(lines[n]).Then(srcs[n].Src)
		}
	}
	q := quoteSpans{lines: lines, srcs: srcs, text: plain(msg.Text), m: m}
	ia, la, oa, ib, lb, ob, _ := m.ordered(*m.sel)
	if ia != i {
		la, oa = 0, 0
	}
	if ib != i {
		lb = len(lines) - 1
		ob = len(lines[lb].Text)
	}
	la, lb = min(max(la, 0), len(lines)-1), min(max(lb, 0), len(lines)-1)
	oa, ob = min(max(oa, 0), len(lines[la].Text)), min(max(ob, 0), len(lines[lb].Text))
	tables := false
	for n := la; n <= lb; n++ {
		tables = tables || lines[n].Table != nil
	}
	if !tables {
		src, why := q.exact(la, oa, lb, ob, true, true)
		if why == "" && src == "" {
			why = quoteCut
		}
		if why != "" {
			return nil, false, why
		}
		return sourceLines(src), false, ""
	}
	// Each run of one table's lines, or of other lines, quotes on its own.
	// A run quotes whole source lines on a side next to a table or covered
	// to the line's edge, so blank lines and line markers stay; blank lines
	// at either end of the highlight go.
	var out []quoteLine
	table := false
	for n := la; n <= lb; {
		end := n
		for end < lb && lines[end+1].Table == lines[n].Table {
			end++
		}
		lo, hi := 0, len(lines[end].Text)
		if n == la {
			lo = oa
		}
		if end == lb {
			hi = ob
		}
		if lines[n].Table != nil {
			rows := tableText(lines, n, end, lo, hi)
			table = table || len(rows) > 0
			out = append(out, rows...)
			n = end + 1
			continue
		}
		if q.blank(n, end) {
			out = append(out, make([]quoteLine, end-n+1)...)
			n = end + 1
			continue
		}
		// A side reached from a line's start or to its end is whole too.
		src, why := q.exact(n, lo, end, hi, n == la && lo > 0, end == lb && hi < len(lines[end].Text))
		if why != "" {
			return nil, false, why
		}
		if src != "" {
			out = append(out, sourceLines(src)...)
		}
		n = end + 1
	}
	for len(out) > 0 && out[0] == (quoteLine{}) {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == (quoteLine{}) {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil, false, quoteNoHighlight
	}
	return out, table, ""
}

// sourceLines splits quoted source into lines.
func sourceLines(src string) []quoteLine {
	var out []quoteLine
	for _, line := range strings.Split(src, "\n") {
		out = append(out, quoteLine{Text: line})
	}
	return out
}

// quoteSpans is one message's display lines and where they come from.
type quoteSpans struct {
	lines []displayLine
	srcs  []lineSrc
	text  string // plain(message text)
	m     *Model
}

// blank reports whether display lines from through to are all blank source
// lines, which quote as bare > lines.
func (q quoteSpans) blank(from, to int) bool {
	return (markdownview.SourceSelection{Lines: q.lines, Sources: q.srcs, Text: q.text}).Blank(from, to)
}

// exact is the source of display lines la (from byte oa) through lb (to
// byte ob), none of them a table's, that renders to exactly their
// highlighted text, or the refusal; it is empty when nothing but line
// breaks is highlighted. startEdge and endEdge say whether the highlight
// starts or ends inside line la or lb; any other side takes whole source
// lines, line breaks and markers included.
func (q quoteSpans) exact(la, oa, lb, ob int, startEdge, endEdge bool) (string, string) {
	text, ok := (markdownview.SourceSelection{Lines: q.lines, Sources: q.srcs, Text: q.text, Render: func(s string) string { return rendered(s, q.m.source, q.m) }}).Exact(la, oa, lb, ob, startEdge, endEdge)
	if !ok {
		return "", quoteCut
	}
	return text, ""
}

// rendered is the copyable text text shows as in the current view.
func rendered(text string, source bool, m *Model) string {
	var out []string
	for _, dl := range displayLines(m.widthMethod, text, source, m.theme, m.self) {
		if !dl.Decor {
			out = append(out, dl.Text)
		}
	}
	return strings.Join(out, "\n")
}

// quoteBlock is quoted lines inserted at the cursor of draft as their own
// block: on a line of its own, then a blank line so what is typed next is
// not part of the quote. A blank source line quotes as a bare >; a table
// row's TAB fields stay.
func quoteBlock(lines []quoteLine, draft string, cursor int) string {
	var b strings.Builder
	if cursor > 0 && draft[cursor-1] != '\n' {
		b.WriteString("\n")
	}
	for _, line := range lines {
		if line.Text == "" || !line.Table && strings.TrimSpace(line.Text) == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + line.Text + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// quoteSelection is Alt+Q and the Quote chip: the original Markdown of the
// highlighted part of one message, or a rendered table's selected cell text,
// goes into the draft at the cursor as one quoted block (one undo step), then
// that message becomes the reply target. The host clipboard is never used.
// Any refusal changes nothing.
func (m *Model) quoteSelection() tea.Cmd {
	i, _, why := m.quoteTarget()
	if why == "" {
		var lines []quoteLine
		var table bool
		lines, table, why = m.quoteSource(i)
		if why == "" {
			why = m.insertQuote(i, lines, table)
		}
	}
	if why != "" {
		m.say(levelWarn, why)
	}
	return nil
}

func (m *Model) insertQuote(i int, lines []quoteLine, table bool) string {
	e := &m.editor
	if err := e.insert(quoteBlock(lines, e.text, e.cursor)); err != nil {
		return "The quote does not fit: " + err.Error() + "; nothing changed"
	}
	m.refreshSuggest()
	msg := &m.messages[i]
	target := m.messageName(msg)
	note := " · replying to it"
	if m.replyTo != "" && m.replyTo != msg.ID {
		note = " · replying to it instead of " + m.replyName(m.replyTo)
	}
	if m.replyTo != msg.ID {
		m.replyTo = msg.ID
		m.replyRevision++
	}
	m.sel = nil
	m.focus = focusComposer
	if table {
		m.say(levelOK, "Quoted selected table text of "+target+note)
		return ""
	}
	what := "1 line"
	if len(lines) > 1 {
		what = fmt.Sprintf("%d lines", len(lines))
	}
	m.say(levelOK, "Quoted the source of "+what+" of "+target+note)
	return ""
}

// replyName names the message a reply ID targets, if it is loaded.
func (m *Model) replyName(id string) string {
	for i := range m.messages {
		if m.messages[i].ID == id {
			return m.messageName(&m.messages[i])
		}
	}
	return "the earlier message"
}
