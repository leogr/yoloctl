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

type sequence struct{ depth int }

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
		if reason := q.line(line); reason != "" {
			return reject(Sequence, "line %d: %s", n+1, reason)
		}
	}
	if q.depth != 0 {
		return reject(Sequence, "block without end")
	}
	return Decision{Kind: Sequence}
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
		return ""
	case blockWords[word]:
		q.depth++
		return ""
	case innerWords[word]:
		if q.depth == 0 {
			return word + " outside a block"
		}
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
		return ""
	case word == "activate" || word == "deactivate":
		id, tail := ident(rest)
		if id == "" || strings.TrimSpace(tail) != "" {
			return "unsupported " + word
		}
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
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if n, tail := ident(id); n == "" || tail != "" {
			return "unsupported note"
		}
	}
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
	return ""
}
