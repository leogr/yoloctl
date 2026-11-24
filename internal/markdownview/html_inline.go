package markdownview

import (
	"strings"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// A small HTML subset inside inline text (feat-markdown-tolerant-tables
// C1). Recognized only bare, case-insensitive: <br> (also <br/>, <br />), the
// pairs <kbd>/<code> (code style), <b>/<strong> (bold), <i>/<em> (italic),
// the hidden <details>/<summary> tags and a comment <!-- … --> closed on its
// line. Everything else, a tag with an attribute and entities included,
// stays literal. A tag is not a tag inside a code span, after a backslash or
// inside an autolink. Pairs match within one line with a stack: a closer
// that matches the innermost open tag closes it, any other closer stays
// literal, and openers still open at the end stay literal.

// tagStrip is a line without its recognized tags.
type tagStrip struct {
	text   string
	src    *SourceMap // text → the line; the tags' bytes are dropped
	styles []tagStyle // in text
	breaks []int      // offsets in text where a <br> was
	tags   bool       // whether anything was recognized
}

type tagStyle struct {
	start, end int
	kind       byte // 'b' bold, 'i' italic, 'c' code
}

var pairTags = map[string]byte{"kbd": 'c', "code": 'c', "b": 'b', "strong": 'b', "i": 'i', "em": 'i'}

type tagToken struct {
	start, end int
	name       string // "br", "details", "summary", "comment" or a pair tag
	closing    bool
	keep       bool // recognized and dropped from the text
	pair       int  // the matching token of a kept pair, else -1
}

// stripTags removes the recognized tags of line.
func stripTags(line string) tagStrip {
	if !strings.Contains(line, "<") {
		return tagStrip{text: line}
	}
	tokens := scanTags(line)
	if len(tokens) == 0 {
		return tagStrip{text: line}
	}
	var stack []int
	for k := range tokens {
		tk := &tokens[k]
		tk.pair = -1
		if _, pair := pairTags[tk.name]; !pair {
			tk.keep = true
			continue
		}
		if !tk.closing {
			stack = append(stack, k)
			continue
		}
		if n := len(stack); n > 0 && tokens[stack[n-1]].name == tk.name {
			open := stack[n-1]
			stack = stack[:n-1]
			tk.keep, tk.pair = true, open
			tokens[open].keep, tokens[open].pair = true, k
		}
	}
	out := tagStrip{src: &SourceMap{Hi: len(line)}}
	var text strings.Builder
	opened := map[int]int{} // kept opener token → its text offset
	// The text kept between dropped tags, in order. A dropped tag's bytes
	// join a neighbouring character as one unit, so a selection inside the
	// tagged text reaches neither tag while a whole line takes both: an
	// opening tag (and <br>, a comment, <details>) the character before it,
	// a closing tag the character after it.
	type keptToken struct {
		tagToken
		k int
	}
	var kept []keptToken
	for k, tk := range tokens {
		if tk.keep {
			kept = append(kept, keptToken{tk, k})
		}
	}
	if len(kept) == 0 {
		return tagStrip{text: line}
	}
	out.tags = true
	plain, extendNext := 0, -1 // extendNext: a closing tag's start, joined to the next character
	emit := func(from, to int) {
		if from >= to {
			return
		}
		if extendNext >= 0 {
			_, size := utf8.DecodeRuneInString(line[from:])
			out.src.Add(line[from:from+size], line, extendNext, from+size)
			text.WriteString(line[from : from+size])
			from += size
			extendNext = -1
		}
		out.src.Add(line[from:to], line, from, to)
		text.WriteString(line[from:to])
	}
	for _, tk := range kept {
		joinPrev := !tk.closing && tk.start > plain
		switch {
		case joinPrev:
			_, size := utf8.DecodeLastRuneInString(line[plain:tk.start])
			emit(plain, tk.start-size)
			if extendNext >= 0 {
				// A closing tag right before: it joins this same character.
				out.src.Add(line[tk.start-size:tk.start], line, extendNext, tk.end)
				extendNext = -1
			} else {
				out.src.Add(line[tk.start-size:tk.start], line, tk.start-size, tk.end)
			}
			text.WriteString(line[tk.start-size : tk.start])
		default:
			emit(plain, tk.start)
			if tk.closing && extendNext < 0 {
				extendNext = tk.start
			}
		}
		plain = tk.end
		switch {
		case tk.name == "br":
			out.breaks = append(out.breaks, text.Len())
		case tk.pair >= 0 && !tk.closing:
			opened[tk.k] = text.Len()
		case tk.pair >= 0:
			out.styles = append(out.styles, tagStyle{start: opened[tk.pair], end: text.Len(), kind: pairTags[tk.name]})
		}
	}
	emit(plain, len(line))
	out.text = text.String()
	return out
}

// scanTags finds the tag tokens of line outside code spans, escapes and
// autolinks, as InlineMarkdownMap reads those.
func scanTags(line string) []tagToken {
	var tokens []tagToken
	noCommentEnd := false
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i += 2
			continue
		case c == '`':
			run := countRun(line[i:], '`')
			if end := strings.Index(line[i+run:], strings.Repeat("`", run)); end >= 0 && countRun(line[i+run+end:], '`') == run {
				i += run + end + run
			} else {
				i += run
			}
			continue
		case c == '<':
			if end, ok := autolinkEnd(line, i); ok {
				i = end + 1
				continue
			}
			if tk, ok := tagAt(line, i, &noCommentEnd); ok {
				tokens = append(tokens, tk)
				i = tk.end
				continue
			}
		}
		i++
	}
	return tokens
}

// tagAt reads a recognized bare tag or a one-line comment at line[i].
func tagAt(line string, i int, noCommentEnd *bool) (tagToken, bool) {
	rest := line[i:]
	if strings.HasPrefix(rest, "<!--") {
		if !*noCommentEnd {
			if end := strings.Index(rest[4:], "-->"); end >= 0 {
				return tagToken{start: i, end: i + 4 + end + 3, name: "comment"}, true
			}
			*noCommentEnd = true // every later suffix is also missing the delimiter
		}
		return tagToken{}, false
	}
	j := 1
	closing := j < len(rest) && rest[j] == '/'
	if closing {
		j++
	}
	k := j
	for k < len(rest) && (rest[k] >= 'a' && rest[k] <= 'z' || rest[k] >= 'A' && rest[k] <= 'Z') {
		k++
	}
	name := strings.ToLower(rest[j:k])
	switch {
	case name == "br" && !closing:
		for k < len(rest) && rest[k] == ' ' {
			k++
		}
		if k < len(rest) && rest[k] == '/' {
			k++
		}
	case name == "details" || name == "summary" || pairTags[name] != 0:
	default:
		return tagToken{}, false
	}
	if k >= len(rest) || rest[k] != '>' {
		return tagToken{}, false
	}
	return tagToken{start: i, end: i + k + 1, name: name, closing: closing}, true
}

// displayAt is the display offset of stripped-text offset b: the first
// display byte shown from b or later.
func displayAt(m *SourceMap, b int) int {
	for k := range m.At {
		if m.StartOf(k) >= b {
			return k
		}
	}
	return len(m.At)
}

// styleTags applies the tag styles to pieces; a code span or autolink inside
// keeps its own style.
func styleTags(pieces []Piece, m *SourceMap, styles []tagStyle, theme spaceview.Theme) []Piece {
	for _, ts := range styles {
		start, end := displayAt(m, ts.start), displayAt(m, ts.end)
		var out []Piece
		for _, p := range pieces {
			if p.End <= start || p.Start >= end || p.Code || p.Auto {
				out = append(out, p)
				continue
			}
			if p.Start < start {
				left := p
				left.End = start
				out = append(out, left)
			}
			mid := p
			mid.Start, mid.End = max(p.Start, start), min(p.End, end)
			switch ts.kind {
			case 'b':
				mid.Style.Bold = true
			case 'i':
				mid.Style.Italic = true
			case 'c':
				mid.Style.FG = theme.Brand
			}
			out = append(out, mid)
			if p.End > end {
				right := p
				right.Start = end
				out = append(out, right)
			}
		}
		pieces = out
	}
	return pieces
}
