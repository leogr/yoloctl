package chatui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// quoteKey runs Quote; the footer chip names it.
const quoteKey, quoteLabel = "alt+q", "❝ Quote"

// srcMap maps each byte of a derived string to the string it came from:
// a verbatim copy maps byte for byte; text shown in place of other bytes (a
// bullet, a tab's spaces, an escaped character, a link's address) is a unit
// that maps only as a whole. Input bytes no output byte maps to are markup
// the view drops. A nil *srcMap records nothing.
type srcMap struct {
	at     []int    // per byte: input offset, or -1-u inside unit u
	units  [][2]int // input ranges shown as other text
	lo, hi int      // the input's bounds
}

// add records text, shown in place of in[from:to].
func (m *srcMap) add(text, in string, from, to int) {
	if m == nil {
		return
	}
	if in[from:to] == text {
		for k := range len(text) {
			m.at = append(m.at, from+k)
		}
		return
	}
	m.units = append(m.units, [2]int{from, to})
	for range len(text) {
		m.at = append(m.at, -len(m.units))
	}
}

// startOf and endOf bound the input that output byte k shows.
func (m *srcMap) startOf(k int) int {
	if a := m.at[k]; a >= 0 {
		return a
	}
	return m.units[-1-m.at[k]][0]
}

func (m *srcMap) endOf(k int) int {
	if a := m.at[k]; a >= 0 {
		return a + 1
	}
	return m.units[-1-m.at[k]][1]
}

// before and after map an output boundary to the input before or after any
// dropped markup there.
func (m *srcMap) before(k int) int {
	if k == 0 {
		return m.lo
	}
	return m.endOf(k - 1)
}

func (m *srcMap) after(k int) int {
	if k == len(m.at) {
		return m.hi
	}
	return m.startOf(k)
}

// then composes m (output → middle) with inner (middle → input).
func (m *srcMap) then(inner *srcMap) *srcMap {
	out := &srcMap{lo: inner.lo, hi: inner.hi}
	mine, theirs := map[int]int{}, map[int]int{}
	unit := func(seen map[int]int, key int, r [2]int) int {
		u, ok := seen[key]
		if !ok {
			out.units = append(out.units, r)
			u = len(out.units) - 1
			seen[key] = u
		}
		return -1 - u
	}
	for _, a := range m.at {
		switch {
		case a >= 0 && inner.at[a] >= 0:
			out.at = append(out.at, inner.at[a])
		case a >= 0:
			out.at = append(out.at, unit(theirs, inner.at[a], inner.units[-1-inner.at[a]]))
		default:
			r := m.units[-1-a]
			out.at = append(out.at, unit(mine, a, [2]int{inner.after(r[0]), inner.before(r[1])}))
		}
	}
	return out
}

// shift moves the input offsets by base.
func (m *srcMap) shift(base int) *srcMap {
	out := &srcMap{at: make([]int, len(m.at)), units: make([][2]int, len(m.units)), lo: m.lo + base, hi: m.hi + base}
	for k, a := range m.at {
		out.at[k] = a
		if a >= 0 {
			out.at[k] = a + base
		}
	}
	for u, r := range m.units {
		out.units[u] = [2]int{r[0] + base, r[1] + base}
	}
	return out
}

// lineSrc is where one display line comes from in plain(text): its source
// line, how its bytes map (nil for a line that maps only whole: a fence
// frame or a rebuilt table line, which Quote reads as cells instead), and the
// fence it belongs to (1-based).
type lineSrc struct {
	start, end int
	src        *srcMap
	fence      int
}

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

// quoteLine is one line Quote inserts: source, or the selected text of a
// table row, which is kept as is even when it holds only TABs.
type quoteLine struct {
	text  string
	table bool
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
		if lines[n].subs != nil && srcs[n].src != nil {
			// A preview maps only whole, to its token.
			srcs[n].src = subMap(lines[n]).then(srcs[n].src)
		}
	}
	q := quoteSpans{lines: lines, srcs: srcs, text: plain(msg.Text), m: m}
	ia, la, oa, ib, lb, ob, _ := m.ordered(*m.sel)
	if ia != i {
		la, oa = 0, 0
	}
	if ib != i {
		lb = len(lines) - 1
		ob = len(lines[lb].text)
	}
	la, lb = min(max(la, 0), len(lines)-1), min(max(lb, 0), len(lines)-1)
	oa, ob = min(max(oa, 0), len(lines[la].text)), min(max(ob, 0), len(lines[lb].text))
	tables := false
	for n := la; n <= lb; n++ {
		tables = tables || lines[n].table != nil
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
		for end < lb && lines[end+1].table == lines[n].table {
			end++
		}
		lo, hi := 0, len(lines[end].text)
		if n == la {
			lo = oa
		}
		if end == lb {
			hi = ob
		}
		if lines[n].table != nil {
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
		src, why := q.exact(n, lo, end, hi, n == la && lo > 0, end == lb && hi < len(lines[end].text))
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
		out = append(out, quoteLine{text: line})
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
	for n := from; n <= to; n++ {
		if q.lines[n].text != "" || q.lines[n].decor || strings.TrimSpace(q.text[q.srcs[n].start:q.srcs[n].end]) != "" {
			return false
		}
	}
	return true
}

// exact is the source of display lines la (from byte oa) through lb (to
// byte ob), none of them a table's, that renders to exactly their
// highlighted text, or the refusal; it is empty when nothing but line
// breaks is highlighted. startEdge and endEdge say whether the highlight
// starts or ends inside line la or lb; any other side takes whole source
// lines, line breaks and markers included.
func (q quoteSpans) exact(la, oa, lb, ob int, startEdge, endEdge bool) (string, string) {
	var parts []string
	for n := la; n <= lb; n++ {
		if q.lines[n].decor {
			continue
		}
		lo, hi := 0, len(q.lines[n].text)
		if n == la {
			lo = min(oa, hi)
		}
		if n == lb {
			hi = min(ob, hi)
		}
		parts = append(parts, q.lines[n].copyText(min(lo, hi), hi))
	}
	// Line breaks at the highlight's own ends are the edges of lines
	// highlighted from their end or to their start: they quote nothing.
	want := strings.Join(parts, "\n")
	sb, se, okA := lineStart(q.srcs[la], oa)
	eb, ee, okB := lineEnd(q.srcs[lb], ob)
	if startEdge {
		want = strings.TrimLeft(want, "\n")
	} else {
		sb, se, okA = q.srcs[la].start, q.srcs[la].start, true
	}
	if endEdge {
		want = strings.TrimRight(want, "\n")
	} else {
		eb, ee, okB = q.srcs[lb].end, q.srcs[lb].end, true
	}
	if !okA || !okB {
		return "", quoteCut
	}
	// Markup touching the ends first, so whole spans keep their markers.
	for _, c := range [][2]int{{se, ee}, {sb, eb}} {
		s, e := c[0], c[1]
		for startEdge && s < e && q.text[s] == '\n' {
			s++
		}
		for endEdge && e > s && q.text[e-1] == '\n' {
			e--
		}
		if s >= e {
			continue
		}
		src := fenced(q.text, q.srcs, la, lb, s, e)
		if rendered(src, q.m.source, q.m) == want {
			return src, ""
		}
	}
	if want == "" {
		return "", ""
	}
	return "", quoteCut
}

// tableText is the selected text of a rendered table's display lines from
// through to, the selection starting at byte lo of line from and ending at
// byte hi of line to: one line per row, holding its reached cells' selected
// text joined by TAB, an empty cell between them an empty field. A row the
// selection covers from its start or to its end reaches its first or last
// cell; otherwise its first and last reached cells are those with selected
// text, so padding and separators at either end add nothing. The rule and a
// row reached only in padding or separators contribute nothing.
func tableText(lines []displayLine, from, to, lo, hi int) []quoteLine {
	var out []quoteLine
	for n := from; n <= to; n++ {
		dl := lines[n]
		if dl.decor {
			continue
		}
		a, b := 0, len(dl.text)
		if n == from {
			a = min(lo, b)
		}
		if n == to {
			b = min(hi, b)
		}
		first, last := -1, -1
		for c, cr := range dl.cells {
			if max(cr[0], a) < min(cr[1], b) {
				if first < 0 {
					first = c
				}
				last = c
			}
		}
		if a == 0 && b == len(dl.text) {
			first, last = 0, len(dl.cells)-1
		} else if first >= 0 {
			if a == 0 {
				first = 0
			}
			if b == len(dl.text) {
				last = len(dl.cells) - 1
			}
		}
		if first < 0 {
			continue
		}
		fields := make([]string, 0, last-first+1)
		for _, cr := range dl.cells[first : last+1] {
			s, e := max(cr[0], a), min(cr[1], b)
			fields = append(fields, dl.text[s:max(s, e)])
		}
		out = append(out, quoteLine{text: strings.Join(fields, "\t"), table: true})
	}
	return out
}

// lineStart maps a highlight starting at display offset o of a line to the
// source offset of that point and the one before any markup touching it.
func lineStart(ls lineSrc, o int) (bare, wide int, ok bool) {
	if ls.src == nil {
		return ls.start, ls.start, true // a fence frame, from its start
	}
	m := ls.src
	if o == len(m.at) {
		return m.hi, m.hi, true
	}
	if m.at[o] < 0 && o > 0 && m.at[o-1] == m.at[o] {
		return 0, 0, false // inside a unit
	}
	return m.startOf(o), m.before(o), true
}

// lineEnd maps a highlight ending at display offset o of a line like
// lineStart, with markup after it.
func lineEnd(ls lineSrc, o int) (bare, wide int, ok bool) {
	if ls.src == nil {
		return ls.end, ls.end, true
	}
	m := ls.src
	if o == 0 {
		return m.lo, m.lo, true
	}
	if m.at[o-1] < 0 && o < len(m.at) && m.at[o] == m.at[o-1] {
		return 0, 0, false
	}
	return m.endOf(o - 1), m.after(o), true
}

// fenced is text[s:e] with the opener of the fence it starts inside and the
// closer of the one it ends inside, so a quoted code block stays one.
func fenced(text string, srcs []lineSrc, la, lb, s, e int) string {
	q := text[s:e]
	if f := srcs[la].fence; f > 0 {
		if open := fenceLine(srcs, f, true); open >= 0 && s > srcs[open].start {
			q = text[srcs[open].start:srcs[open].end] + "\n" + q
		}
	}
	if f := srcs[lb].fence; f > 0 {
		open, end := fenceLine(srcs, f, true), fenceLine(srcs, f, false)
		switch {
		case end >= 0 && end != open && e < srcs[end].end:
			q += "\n" + text[srcs[end].start:srcs[end].end]
		case end == open && open >= 0:
			// The source never closes it: close with its own marker.
			q += "\n" + fenceMarker(strings.TrimLeft(text[srcs[open].start:srcs[open].end], " "))
		}
	}
	return q
}

// fenceLine finds fence f's opener, or its closer (the opener again when the
// source never closes it).
func fenceLine(srcs []lineSrc, f int, open bool) int {
	first, last := -1, -1
	for k, ls := range srcs {
		if ls.fence == f {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if open || last == first {
		return first
	}
	if srcs[last].src == nil {
		return last // the closing frame
	}
	return first // unclosed: its last line is code
}

// rendered is the copyable text text shows as in the current view.
func rendered(text string, source bool, m *Model) string {
	var out []string
	for _, dl := range displayLines(m.widthMethod, text, source, m.theme, m.self) {
		if !dl.decor {
			out = append(out, dl.text)
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
		if line.text == "" || !line.table && strings.TrimSpace(line.text) == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + line.text + "\n")
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
