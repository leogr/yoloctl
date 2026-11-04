package chatui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// span styles plain[start:end] of one rendered line. Code spans are
// literal; link spans name the URL that both their parts open.
type span struct {
	start, end int
	style      spaceview.Style
	code       bool
	link       string
	strike     bool
}

// inlineMarkdown renders a deliberately small inline subset of one line:
// `code` (accent text, no background or padding), **strong**/__strong__,
// *emphasis*/_emphasis_ (italic), ***both***/___both___, ~~strikethrough~~
// and [text](url). The result is display text with its style spans; markers
// are removed only when they pair up, and a link always keeps its URL visible
// after the text. Code spans are literal. The original message text is never
// changed.
func inlineMarkdown(line string, base spaceview.Style, theme spaceview.Theme) (string, []span) {
	return inlineMarkdownMap(line, base, theme, nil)
}

// inlineMarkdownMap is inlineMarkdown that, given src, also records where
// each display byte comes from in line (see srcMap).
func inlineMarkdownMap(line string, base spaceview.Style, theme spaceview.Theme, src *srcMap) (string, []span) {
	var out strings.Builder
	var spans []span
	// emitSpan writes text, shown in place of line[from:to].
	emitSpan := func(text string, sp span, from, to int) {
		if text == "" {
			return
		}
		src.add(text, line, from, to)
		sp.start = out.Len()
		out.WriteString(text)
		sp.end = out.Len()
		spans = append(spans, sp)
	}
	emit := func(from, to int, st spaceview.Style) { emitSpan(line[from:to], span{style: st}, from, to) }
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
			emitSpan(line[i+1:i+2], span{style: base}, i, i+2)
			i += 2
			plain = i
			continue
		case c == '`':
			run := countRun(line[i:], '`')
			if end := strings.Index(line[i+run:], strings.Repeat("`", run)); end >= 0 && countRun(line[i+run+end:], '`') == run {
				flush(i)
				emitSpan(line[i+run:i+run+end], span{style: code, code: true}, i+run, i+run+end)
				i += run + end + run
				plain = i
				continue
			}
			i += run
			continue
		case c == '~' && countRun(line[i:], '~') == 2 && opens(line, i, 2):
			if end := closing(line, i+2, "~~"); end >= 0 {
				flush(i)
				emitSpan(line[i+2:end], span{style: base, strike: true}, i+2, end)
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
		case c == '[':
			if close := strings.Index(line[i:], "]("); close > 1 {
				textEnd := i + close
				if urlEnd := strings.IndexByte(line[textEnd+2:], ')'); urlEnd > 0 {
					url := line[textEnd+2 : textEnd+2+urlEnd]
					if !strings.ContainsAny(url, " \t") {
						flush(i)
						emitSpan(line[i+1:textEnd], span{style: link, link: url}, i+1, textEnd)
						emitSpan(" ("+url+")", span{style: dim, link: url}, textEnd, textEnd+2+urlEnd+1)
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

// listItem replaces an unordered list marker with a bullet, keeping indent;
// given src, it records the bullet as shown in place of its marker.
func listItem(line string, src *srcMap) string {
	trim := strings.TrimLeft(line, " ")
	if len(trim) >= 2 && strings.IndexByte("-*+", trim[0]) >= 0 && trim[1] == ' ' {
		lead := len(line) - len(trim)
		src.add(line[:lead], line, 0, lead)
		src.add("• ", line, lead, lead+2)
		src.add(trim[2:], line, lead+2, len(line))
		return line[:lead] + "• " + trim[2:]
	}
	return line
}
