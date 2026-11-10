package markdownview

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// span styles plain[start:end] of one rendered line. Code spans are
// literal; link spans name the URL that both their parts open.
type Span struct {
	Start, End int
	Style      spaceview.Style
	Code       bool
	Link       string
	Strike     bool
	Auto       bool // an angle autolink: shown and linked as a whole
}

// inlineMarkdown renders a deliberately small inline subset of one line:
// `code` (accent text, no background or padding), **strong**/__strong__,
// *emphasis*/_emphasis_ (italic), ***both***/___both___, ~~strikethrough~~,
// [text](url) and <http(s)://url>, shown without its brackets. The result
// is display text with its style spans; markers are removed only when they
// pair up, and a link always keeps its URL visible after the text. Code spans are literal. The original message text is never
// changed.
func InlineMarkdown(line string, base spaceview.Style, theme spaceview.Theme) (string, []Span) {
	return InlineMarkdownMap(line, base, theme, nil)
}

// inlineMarkdownMap is inlineMarkdown that, given src, also records where
// each display byte comes from in line (see srcMap).
func InlineMarkdownMap(line string, base spaceview.Style, theme spaceview.Theme, src *SourceMap) (string, []Span) {
	var out strings.Builder
	var spans []Span
	// emitSpan writes text, shown in place of line[from:to].
	emitSpan := func(text string, sp Span, from, to int) {
		if text == "" {
			return
		}
		src.Add(text, line, from, to)
		sp.Start = out.Len()
		out.WriteString(text)
		sp.End = out.Len()
		spans = append(spans, sp)
	}
	emit := func(from, to int, st spaceview.Style) { emitSpan(line[from:to], Span{Style: st}, from, to) }
	strong, emph, both := base, base, base
	strong.Bold = true
	emph.Italic = true
	both.Bold, both.Italic = true, true
	code := spaceview.Style{FG: theme.Brand}
	link := spaceview.Style{FG: theme.Label, Underline: true}
	dim := spaceview.Style{FG: theme.Dim}
	plain := 0 // start of pending literal text
	flush := func(i int) {
		emit(plain, i, base)
	}
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line) && strings.IndexByte("\\`*_[]()~|", line[i+1]) >= 0:
			flush(i)
			emitSpan(line[i+1:i+2], Span{Style: base}, i, i+2)
			i += 2
			plain = i
			continue
		case c == '`':
			run := countRun(line[i:], '`')
			if end := strings.Index(line[i+run:], strings.Repeat("`", run)); end >= 0 && countRun(line[i+run+end:], '`') == run {
				flush(i)
				emitSpan(line[i+run:i+run+end], Span{Style: code, Code: true}, i+run, i+run+end)
				i += run + end + run
				plain = i
				continue
			}
			i += run
			continue
		case c == '~' && countRun(line[i:], '~') == 2 && opens(line, i, 2):
			if end := closing(line, i+2, "~~"); end >= 0 {
				flush(i)
				emitSpan(line[i+2:end], Span{Style: base, Strike: true}, i+2, end)
				i = end + 2
				plain = i
				continue
			}
			i += 2
			continue
		case (c == '*' || c == '_') && countRun(line[i:], c) == 3 && opens(line, i, 3) && closing(line, i+3, strings.Repeat(string(c), 3)) >= 0:
			end := closing(line, i+3, strings.Repeat(string(c), 3))
			flush(i)
			emit(i+3, end, both)
			i = end + 3
			plain = i
			continue
		case c == '*' || c == '_':
			run := min(countRun(line[i:], c), 2)
			marker := strings.Repeat(string(c), run)
			if opens(line, i, run) {
				if end := closing(line, i+run, marker); end >= 0 {
					flush(i)
					if run == 2 {
						emit(i+run, end, strong)
					} else {
						emit(i+run, end, emph)
					}
					i = end + run
					plain = i
					continue
				}
			}
			i += run
			continue
		case c == '<':
			if end, ok := autolinkEnd(line, i); ok {
				flush(i)
				url := line[i+1 : end]
				emitSpan(url, Span{Style: link, Link: url, Auto: true}, i+1, end)
				i = end + 1
				plain = i
				continue
			}
		case c == '[':
			if close := strings.Index(line[i:], "]("); close > 1 {
				textEnd := i + close
				if urlEnd := strings.IndexByte(line[textEnd+2:], ')'); urlEnd > 0 {
					url := line[textEnd+2 : textEnd+2+urlEnd]
					if !strings.ContainsAny(url, " \t") {
						flush(i)
						emitSpan(line[i+1:textEnd], Span{Style: link, Link: url}, i+1, textEnd)
						emitSpan(" ("+url+")", Span{Style: dim, Link: url}, textEnd, textEnd+2+urlEnd+1)
						i = textEnd + 2 + urlEnd + 1
						plain = i
						continue
					}
				}
			}
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		i += max(size, 1)
	}
	flush(len(line))
	return out.String(), spans
}

// autolinkEnd returns the index of the '>' that closes an angle autolink at
// line[i]: an http or https URL with something after the scheme and no
// space, control or '<' before the '>'. Other angle-bracket text, HTML
// included, stays literal.
func autolinkEnd(line string, i int) (int, bool) {
	rest := line[i+1:]
	scheme := 0
	switch {
	case strings.HasPrefix(rest, "https://"):
		scheme = len("https://")
	case strings.HasPrefix(rest, "http://"):
		scheme = len("http://")
	default:
		return 0, false
	}
	for j := scheme; j < len(rest); j++ {
		switch c := rest[j]; {
		case c == '>':
			return i + 1 + j, j > scheme
		case c == '<' || c <= ' ' || c == 0x7f:
			return 0, false
		}
	}
	return 0, false
}

func countRun(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

func runeBefore(s string, i int) rune {
	if i == 0 {
		return ' '
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

func runeAt(s string, i int) rune {
	if i >= len(s) {
		return ' '
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r
}

// A delimiter opens after a boundary and before non-space, so snake_case and
// a * b stay literal.
func opens(s string, i, run int) bool {
	before := runeBefore(s, i)
	return (unicode.IsSpace(before) || unicode.IsPunct(before)) && !unicode.IsSpace(runeAt(s, i+run))
}

// closing finds a matching delimiter after non-space and before a boundary.
func closing(s string, from int, marker string) int {
	for j := from; j < len(s); {
		k := strings.Index(s[j:], marker)
		if k < 0 {
			return -1
		}
		at := j + k
		if at > from && !unicode.IsSpace(runeBefore(s, at)) {
			after := runeAt(s, at+len(marker))
			if unicode.IsSpace(after) || unicode.IsPunct(after) {
				return at
			}
		}
		j = at + len(marker)
	}
	return -1
}

// ListItem marks the display line of a list item: Text[:Marker] is its
// indentation and Text[Marker:Content] its marker with the space after it
// (Content is 0 on other lines). Its wrapped rows align under
// Text[Content:]. Pad decorative cells precede an ordered item so that the
// written numbers of its run right-align; they are never copied.
type ListItem struct {
	Marker, Content, Pad int
	Ordered              bool
}

// listItem replaces an unordered list marker with a bullet, keeping indent;
// given src, it records the bullet as shown in place of its marker. An
// ordered item keeps its written number and delimiter.
func listItem(line string, src *SourceMap) (string, ListItem) {
	trim := strings.TrimLeft(line, " ")
	lead := len(line) - len(trim)
	if len(trim) >= 2 && strings.IndexByte("-*+", trim[0]) >= 0 && trim[1] == ' ' {
		src.Add(line[:lead], line, 0, lead)
		src.Add("• ", line, lead, lead+2)
		src.Add(trim[2:], line, lead+2, len(line))
		return line[:lead] + "• " + trim[2:], ListItem{Marker: lead, Content: lead + len("• ")}
	}
	if OrderedItem(trim) {
		digits := len(trim) - len(strings.TrimLeft(trim, "0123456789"))
		return line, ListItem{Marker: lead, Content: lead + digits + len(". "), Ordered: true}
	}
	return line, ListItem{}
}

// alignOrdered right-aligns the written numbers of each run of ordered
// items at one indentation. A run continues across deeper list items; any
// other line, a blank one included, an unordered item at its indentation or
// a shallower item ends it.
func alignOrdered(lines []DisplayLine) {
	type run struct {
		indent, widest int
		lines          []int
	}
	var open []run
	end := func(r run) {
		for _, n := range r.lines {
			li := &lines[n].List
			li.Pad = r.widest - (li.Content - li.Marker)
		}
	}
	for n := range lines {
		li := lines[n].List
		if li.Content == 0 {
			for _, r := range open {
				end(r)
			}
			open = open[:0]
			continue
		}
		for len(open) > 0 && open[len(open)-1].indent > li.Marker {
			end(open[len(open)-1])
			open = open[:len(open)-1]
		}
		if top := len(open) - 1; top >= 0 && open[top].indent == li.Marker {
			if li.Ordered {
				open[top].lines = append(open[top].lines, n)
				open[top].widest = max(open[top].widest, li.Content-li.Marker)
				continue
			}
			end(open[top])
			open = open[:top]
		}
		if li.Ordered {
			open = append(open, run{indent: li.Marker, widest: li.Content - li.Marker, lines: []int{n}})
		}
	}
	for _, r := range open {
		end(r)
	}
}
