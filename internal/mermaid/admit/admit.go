// Package admit decides whether a Mermaid fence may be drawn. It validates
// only: the renderer always receives the original source, never a rewrite.
// Every line must be recognized; anything else rejects the whole fence with a
// short reason that the view shows next to the source.
//
// Admitted (v1, leogr D2): flowcharts (graph|flowchart TD|TB|LR) with plain,
// quoted, rhombus and round nodes, the four edge styles, labels, chains, &
// groups, subgraphs and comments; sequence diagrams with participants and
// actors, the four message arrows, notes, blocks, activations and autonumber.
package admit

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is an admitted diagram type.
type Kind int

const (
	Unknown Kind = iota
	Flowchart
	Sequence
)

// Decision is the admission result: Reason is empty when admitted.
type Decision struct {
	Kind   Kind
	Reason string
}

// Admitted reports whether the fence may be sent to the renderer.
func (d Decision) Admitted() bool { return d.Reason == "" }

func reject(k Kind, format string, args ...any) Decision {
	return Decision{Kind: k, Reason: fmt.Sprintf(format, args...)}
}

// Check validates one fence's source.
func Check(source string) Decision {
	if !utf8.ValidString(source) {
		return reject(Unknown, "not valid UTF-8")
	}
	for _, r := range source {
		if r != '\n' && unicode.IsControl(r) { // as the renderer requires
			return reject(Unknown, "contains control characters")
		}
	}
	lines := strings.Split(source, "\n")
	first := -1
	var header []string
	for i, line := range lines {
		t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
		if t != "" && !strings.HasPrefix(t, "%%") {
			first, header = i, strings.Fields(t)
			break
		}
	}
	if first < 0 || len(header) == 0 {
		return reject(Unknown, "empty diagram")
	}
	switch {
	case header[0] == "sequenceDiagram" && len(header) == 1:
		return checkSequence(lines, first+1)
	case header[0] == "graph" || header[0] == "flowchart":
		if len(header) != 2 {
			return reject(Flowchart, "flowchart needs one direction (TD, TB or LR)")
		}
		switch header[1] {
		case "TD", "TB", "LR":
			return checkFlowchart(lines, first+1)
		default:
			return reject(Flowchart, "direction %s is not supported", header[1])
		}
	default:
		return reject(Unknown, "only flowchart and sequence diagrams are drawn")
	}
}

func isIDRune(r rune) bool {
	return r == '_' || r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
}

// ident reads [A-Za-z0-9_]+ at the start of s.
func ident(s string) (string, string) {
	i := 0
	for i < len(s) && isIDRune(rune(s[i])) {
		i++
	}
	return s[:i], s[i:]
}

// ---- flowchart ----------------------------------------------------------

type flow struct{ depth int }

func checkFlowchart(lines []string, from int) Decision {
	f := &flow{}
	for n := from; n < len(lines); n++ {
		line := strings.TrimSuffix(strings.TrimSpace(lines[n]), ";")
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if reason := f.line(line); reason != "" {
			return reject(Flowchart, "line %d: %s", n+1, reason)
		}
	}
	if f.depth != 0 {
		return reject(Flowchart, "subgraph without end")
	}
	return Decision{Kind: Flowchart}
}

// Mermaid flowchart keywords outside v1: named in the reason.
var unsupportedFlowWords = map[string]bool{
	"classDef": true, "class": true, "style": true, "linkStyle": true,
	"click": true, "direction": true, "accTitle": true, "accDescr": true,
}

func (f *flow) line(line string) string {
	if line == "end" {
		if f.depth == 0 {
			return "end without subgraph"
		}
		f.depth--
		return ""
	}
	if word, _, _ := strings.Cut(line, " "); unsupportedFlowWords[word] {
		return word + " is not supported"
	}
	if rest, ok := strings.CutPrefix(line, "subgraph"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		f.depth++
		return subgraphTitle(strings.TrimSpace(rest))
	}
	return f.statement(line)
}

// subgraphTitle accepts `id`, `id[title]` or plain words. Quoted titles
// (`"title"`, `id["title"]`) are refused: the renderer draws their quotes.
func subgraphTitle(s string) string {
	if s == "" {
		return "subgraph needs a name"
	}
	if strings.Contains(s, `"`) {
		return "quoted subgraph titles are not supported"
	}
	id, rest := ident(s)
	if id != "" && strings.HasPrefix(rest, "[") {
		_, tail, ok := bracketText(rest, '[', ']')
		if !ok || tail != "" {
			return "unsupported subgraph title"
		}
		return ""
	}
	if strings.ContainsAny(s, `[]{}()|<>`) {
		return "unsupported subgraph title"
	}
	return ""
}

// statement is group (edge group)*, group is node (& node)*.
func (f *flow) statement(s string) string {
	for {
		for {
			rest, reason := node(s)
			if reason != "" {
				return reason
			}
			s = strings.TrimLeft(rest, " \t")
			if !strings.HasPrefix(s, "&") {
				break
			}
			s = strings.TrimLeft(s[1:], " \t")
		}
		if s == "" {
			return ""
		}
		rest, reason := edge(s)
		if reason != "" {
			return reason
		}
		s = strings.TrimLeft(rest, " \t")
	}
}

// node reads an id and an optional admitted shape: [text], ["text"],
// {text}, (text).
func node(s string) (string, string) {
	id, rest := ident(s)
	if id == "" {
		return "", "unsupported statement"
	}
	if rest == "" {
		return rest, ""
	}
	open := rest[0]
	var closing byte
	switch open {
	case '[':
		closing = ']'
	case '{':
		closing = '}'
	case '(':
		closing = ')'
	case ':', '>':
		return "", "unsupported node style or shape"
	default:
		return rest, ""
	}
	if len(rest) > 1 && strings.ContainsRune("[({/\\>", rune(rest[1])) {
		return "", "unsupported node shape"
	}
	_, tail, ok := bracketText(rest, open, closing)
	if !ok {
		return "", "unsupported node text"
	}
	return tail, ""
}

// bracketText reads open text close, where text is either "quoted" (any
// characters but a quote) or plain (no brackets or quotes).
func bracketText(s string, open, closing byte) (string, string, bool) {
	if len(s) < 2 || s[0] != open {
		return "", "", false
	}
	body := s[1:]
	if strings.HasPrefix(body, `"`) {
		end := strings.IndexByte(body[1:], '"')
		if end < 0 || len(body) < end+3 || body[end+2] != closing {
			return "", "", false
		}
		return body[1 : end+1], body[end+3:], true
	}
	end := strings.IndexByte(body, closing)
	if end < 0 {
		return "", "", false
	}
	text := body[:end]
	if strings.ContainsAny(text, "[]{}()\"|") {
		return "", "", false
	}
	return text, body[end+1:], true
}

var edgeOps = []string{"-.->", "-->", "---", "==>"}

// edge reads an admitted edge: an operator with an optional |label|, or
// `-- label -->`.
func edge(s string) (string, string) {
	for _, op := range edgeOps {
		if rest, ok := strings.CutPrefix(s, op); ok {
			if rest != "" && strings.ContainsRune("->.=ox", rune(rest[0])) {
				return "", "unsupported edge"
			}
			return pipeLabel(rest)
		}
	}
	if rest, ok := strings.CutPrefix(s, "-- "); ok {
		text, tail, found := strings.Cut(rest, "-->")
		text = strings.TrimSpace(text)
		if !found || text == "" || strings.ContainsAny(text, "|\"[]{}()") {
			return "", "unsupported edge label"
		}
		if tail != "" && strings.ContainsRune("->.=ox", rune(tail[0])) {
			return "", "unsupported edge"
		}
		return tail, ""
	}
	return "", "unsupported edge"
}

func pipeLabel(s string) (string, string) {
	t := strings.TrimLeft(s, " \t")
	if !strings.HasPrefix(t, "|") {
		return s, ""
	}
	text, rest, ok := strings.Cut(t[1:], "|")
	if !ok || strings.ContainsAny(text, `"`) {
		return "", "unsupported edge label"
	}
	return rest, ""
}

// ---- sequence -----------------------------------------------------------

type sequence struct {
	depth int
	// stmts are the statements in order, for the note placement checks;
	// order is the participants by first appearance (their columns).
	stmts []seqStmt
	order map[string]int
	line0 int // the line number being checked
}

// Statement kinds that note placement depends on.
const (
	stmtOther = iota
	stmtMessage
	stmtNote
	stmtOpen  // alt, opt, loop, par, critical, break
	stmtInner // else, and
	stmtEnd
)

type seqStmt struct {
	kind  int
	line  int
	ids   []string // a message's two participants, a note's
	where string   // a note's "left of ", "right of " or "over "
}

func (q *sequence) add(kind int, where string, ids ...string) {
	q.stmts = append(q.stmts, seqStmt{kind: kind, line: q.line0, ids: ids, where: where})
	q.participant(ids...)
}

func (q *sequence) participant(ids ...string) {
	if q.order == nil {
		q.order = map[string]int{}
	}
	for _, id := range ids {
		if _, ok := q.order[id]; !ok {
			q.order[id] = len(q.order)
		}
	}
}

var blockWords = map[string]bool{"alt": true, "opt": true, "loop": true, "par": true, "critical": true, "break": true}
var innerWords = map[string]bool{"else": true, "and": true}

// arrows, longest first.
var arrows = []string{"-->>", "->>", "-->", "->"}

func checkSequence(lines []string, from int) Decision {
	q := &sequence{}
	for n := from; n < len(lines); n++ {
		line := strings.TrimSuffix(strings.TrimSpace(lines[n]), ";")
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		q.line0 = n + 1
		if reason := q.line(line); reason != "" {
			return reject(Sequence, "line %d: %s", n+1, reason)
		}
	}
	if q.depth != 0 {
		return reject(Sequence, "block without end")
	}
	if n, reason := q.notePlacement(); reason != "" {
		return reject(Sequence, "line %d: %s", n, reason)
	}
	return Decision{Kind: Sequence}
}

// notePlacement refuses the notes the renderer draws in the wrong place
// (deep QA D1/D2): it draws a note beside the message before it, so a note
// right after a block's start, else/and or end lands in the wrong block; and
// a block's frame spans only its messages' participants, so inside a block a
// note left of its first or right of its last participant, or one beside or
// over a participant outside that span, crosses or leaves the frame.
func (q *sequence) notePlacement() (int, string) {
	type block struct{ lo, hi int } // column span of its messages (lo > hi: none)
	spans := make([]block, len(q.stmts))
	// One pass: a message widens the innermost block; a block's span joins
	// its parent's when it ends, so each statement is visited once.
	var open []int
	for i, st := range q.stmts {
		switch st.kind {
		case stmtOpen:
			spans[i] = block{lo: len(q.order), hi: -1}
			open = append(open, i)
		case stmtEnd:
			child := spans[open[len(open)-1]]
			open = open[:len(open)-1]
			if len(open) > 0 {
				p := &spans[open[len(open)-1]]
				p.lo, p.hi = min(p.lo, child.lo), max(p.hi, child.hi)
			}
		case stmtMessage:
			if len(open) > 0 {
				b := &spans[open[len(open)-1]]
				for _, id := range st.ids {
					b.lo, b.hi = min(b.lo, q.order[id]), max(b.hi, q.order[id])
				}
			}
		}
	}
	open = open[:0]
	boundary := false // the statement before (past other notes) starts, splits or ends a block
	for i, st := range q.stmts {
		switch st.kind {
		case stmtOpen:
			open = append(open, i)
			boundary = true
		case stmtInner:
			boundary = true
		case stmtEnd:
			open = open[:len(open)-1]
			boundary = true
		case stmtMessage:
			boundary = false
		case stmtNote:
			if boundary {
				return st.line, "a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"
			}
			if len(open) == 0 {
				continue
			}
			// Inside the frame: over a participant within the span, left of one
			// right of its left edge, right of one left of its right edge.
			span := spans[open[len(open)-1]]
			for _, id := range st.ids {
				c := q.order[id]
				switch {
				case st.where == "left of " && c <= span.lo:
					return st.line, "a note left of a block's first participant crosses the block's frame"
				case st.where == "right of " && c >= span.hi:
					return st.line, "a note right of a block's last participant crosses the block's frame"
				case c < span.lo || c > span.hi:
					return st.line, "a note inside a block over a participant without messages in that block is drawn outside it"
				}
			}
		}
	}
	return 0, ""
}

func (q *sequence) line(line string) string {
	word, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case line == "autonumber":
		return ""
	case line == "end":
		if q.depth == 0 {
			return "end without block"
		}
		q.depth--
		q.add(stmtEnd, "")
		return ""
	case blockWords[word]:
		q.depth++
		q.add(stmtOpen, "")
		return ""
	case innerWords[word]:
		if q.depth == 0 {
			return word + " outside a block"
		}
		q.add(stmtInner, "")
		return ""
	case word == "participant" || word == "actor":
		id, tail := ident(rest)
		tail = strings.TrimSpace(tail)
		if id == "" {
			return "participant needs a name"
		}
		if tail != "" {
			alias, ok := strings.CutPrefix(tail, "as ")
			if !ok || strings.TrimSpace(alias) == "" {
				return "unsupported participant"
			}
		}
		q.participant(id)
		return ""
	case word == "activate" || word == "deactivate":
		id, tail := ident(rest)
		if id == "" || strings.TrimSpace(tail) != "" {
			return "unsupported " + word
		}
		q.participant(id)
		return ""
	case word == "Note" || word == "note":
		return q.note(rest)
	}
	return q.message(line)
}

func (q *sequence) note(s string) string {
	var where string
	for _, p := range []string{"left of ", "right of ", "over "} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			where, s = p, rest
			break
		}
	}
	if where == "" {
		return "unsupported note"
	}
	who, _, ok := strings.Cut(s, ":")
	if !ok {
		return "note needs a text"
	}
	ids := strings.Split(who, ",")
	if len(ids) > 2 || len(ids) == 2 && where != "over " {
		return "unsupported note"
	}
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if n, tail := ident(id); n == "" || tail != "" {
			return "unsupported note"
		}
		ids[i] = id
	}
	q.add(stmtNote, where, ids...)
	return ""
}

func (q *sequence) message(s string) string {
	from, rest := ident(s)
	if from == "" {
		return "unsupported statement"
	}
	rest = strings.TrimLeft(rest, " \t")
	arrow := ""
	for _, a := range arrows {
		if strings.HasPrefix(rest, a) {
			arrow = a
			break
		}
	}
	if arrow == "" {
		if strings.HasPrefix(rest, "-") {
			return "unsupported arrow"
		}
		return "unsupported statement"
	}
	rest = rest[len(arrow):]
	if rest != "" && strings.ContainsRune("x)>", rune(rest[0])) {
		return "unsupported arrow" // -x, -), and longer arrows than the four
	}
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "+"), "-")
	rest = strings.TrimLeft(rest, " \t")
	to, rest := ident(rest)
	if to == "" {
		return "message needs a target"
	}
	rest = strings.TrimLeft(rest, " \t")
	if _, ok := strings.CutPrefix(rest, ":"); !ok {
		return "message needs a text"
	}
	q.add(stmtMessage, "", from, to)
	return ""
}
