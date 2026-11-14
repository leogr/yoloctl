package markdownview

import (
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Piece styles display text [start, end) of one line.
type Piece struct {
	Start, End int
	Style      spaceview.Style
	Code       bool // inline code: never re-styled as a mention or URL
	Strike     bool
	Auto       bool // an angle autolink: likewise never re-styled
	Act        Action
}

// DisplayLine is one logical line as shown: Markdown markers removed,
// bullets and tabs expanded. Its text is exactly what a drag selection
// copies; decorations (quote bars, fence frames) live outside it.
type DisplayLine struct {
	Text   string
	Pieces []Piece
	Quote  bool // quote bars precede every wrapped row
	// QuoteDepth is a quote line's number of levels, one bar each (0
	// outside a quote).
	QuoteDepth int
	Decor      bool // a fence frame or table rule: shown, never copied
	Code       bool // fenced code: hard-wrapped inside its block's card
	Fence      FenceFrame
	// block is a fence opener's code, its source lines as its copy button
	// copies them ("" for an empty block, which shows no button).
	Block string
	// table is set on the rows and the rule of a rendered table; cells are
	// the byte ranges of a row's cell texts in text (nil on the rule).
	Table *TableInfo
	Cells [][2]int
	// Subs record caller-supplied display replacements.
	Subs []TextSub
	// List is set on a list item's line (see ListItem).
	List ListItem
	// Rule is a horizontal rule (a Decor line without text): bodyRows draws
	// a thin line across the text width.
	Rule       bool
	TableLimit bool // rejected structural table: explain before literal text
	// Hidden is a line the view does not show (a setext underline, a
	// comment): it keeps its place and LineSource so indexes stay aligned,
	// is Decor so it is never copied, and gets no row, so it adds no blank
	// row and cannot be selected.
	Hidden bool
	// Diagram is the fence (1-based) whose drawing this Code line is a row
	// of, 0 otherwise.
	Diagram int
}

// drawDiagram replaces the code lines of the fence opened at out[opener]
// with its drawing rows. Each row maps to the fence's whole content as one
// unit: a Quote that covers the drawing from its first row's start to its
// last row's end quotes the exact fence; anything partial is inexact.
func drawDiagram(out []DisplayLine, opener, fence int, rows []string, theme spaceview.Theme, srcs *[]LineSource, openerSrcs, closerAt int) []DisplayLine {
	out = out[:opener+1]
	start, end := closerAt, closerAt
	if srcs != nil {
		*srcs = (*srcs)[:openerSrcs]
		start = (*srcs)[openerSrcs-1].End + 1
		end = max(start, closerAt-1)
	}
	style := spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}
	for k, row := range rows {
		out = append(out, DisplayLine{Text: row, Code: true, Diagram: fence, Pieces: []Piece{{End: len(row), Style: style}}})
		if srcs != nil {
			m := &SourceMap{Units: [][2]int{{start, end}}, Lo: start, Hi: end, At: make([]int, len(row))}
			for i := range m.At {
				m.At[i] = -1
			}
			*srcs = append(*srcs, LineSource{Start: start, End: end, Src: m, Fence: fence, DiagramFirst: k == 0, DiagramLast: k == len(rows)-1, Diagram: true})
		}
	}
	return out
}

// TableInfo is shared by the display lines of one rendered table: what
// bodyRows needs to fit its columns to the view.
type TableInfo struct {
	Align        []byte
	Natural      []int // widest cell per column: the aligned layout
	Floor        []int // narrowest a wrapped column may get
	WordFloor    []int // longest table-only unbreakable run
	CompactFloor []int // widest grapheme plus an ellipsis, capped by natural
	Source       int   // source bytes, for the wrapped layout's budget
	Unpadded     bool  // aligned representation exceeded the same byte budget
	// spaceBefore and spaceAfter ask bodyRows for an empty row between the
	// table and a neighbouring source line that is not blank. They are
	// display rows only: no logical line, source map or copied byte.
	SpaceBefore, SpaceAfter bool
}

// FenceFrame marks the decor line of a fence: the opener shows the info
// string (the language) as the card's label row, the closer its bottom
// padding row.
type FenceFrame int

const (
	FenceNone FenceFrame = iota
	FenceOpen
	FenceClose
)

// copyText returns the copyable bytes of text[lo:hi]: exactly what is
// shown, except that a preview it covers whole is its reference token.
func (dl DisplayLine) CopyText(lo, hi int) string {
	var b strings.Builder
	at := lo
	for _, s := range dl.Subs {
		if s.Start >= lo && s.End <= hi {
			b.WriteString(dl.Text[at:s.Start])
			b.WriteString(s.Copy)
			at = s.End
		}
	}
	b.WriteString(dl.Text[at:hi])
	return b.String()
}

// BodyRow is one wrapped row of a display line. A row of a wrapped table
// shows cell fragments that are not contiguous in the line: frags lists
// them, and [start, end) only bounds them.
type BodyRow struct {
	Line, Start, End int
	Runs             []Run
	Decor            bool
	Closed           bool // compact row: full cells remain available to logical copy
	Frags            [][2]int
}

// codeCard is a fenced block's background: a light raised tint over the
// block's own width rather than a full-width slab.
func CodeCard(t spaceview.Theme) spaceview.Color { return t.Popup }

// maxBodyRows bounds the rows one message can occupy; the original text
// stays available to explicit copy.
const MaxBodyRows = 16384

// parseLines optionally records where each display line comes from in
// Plain(text), one LineSource per display line. Display is identical either way.
func parseLines(wm ansi.Method, text string, source bool, theme spaceview.Theme, decorate Decorator, srcs *[]LineSource, diagrams func(string) (Diagram, bool)) []DisplayLine {
	var out []DisplayLine
	fence, fences := "", 0
	openerSrcs := 0 // len(*srcs) right after the open fence's opener
	lines := strings.Split(Plain(text), "\n")
	opener := -1 // the open fence's display line
	var block strings.Builder
	var lang *syntax // the open fence's highlighter, nil for plain
	var hl hlState
	at := 0      // the source offset of lines[idx]
	literal := 0 // lines of a rejected table: shown as text, never re-detected
	var lists listContext
	// Comment blocks (C1): closer[k] is the first line at or after k that
	// holds "-->", from one backward pass, so no opener searches the rest of
	// the message; commentEnd is the open block's closing line.
	closer := make([]int, len(lines)+1)
	closer[len(lines)] = -1
	for k := len(lines) - 1; k >= 0; k-- {
		closer[k] = closer[k+1]
		if strings.Contains(lines[k], "-->") {
			closer[k] = k
		}
	}
	commentEnd := -1
	// setext[k] is the heading level of paragraph line k when its run ends
	// in an underline, -1 for line k being that underline, 0 when decided
	// otherwise; a missing key is not decided yet.
	setext := map[int]int{}
	noTable := 0 // lines before it are known not to start a table
	paragraphLine := func(k int) bool {
		l := lines[k]
		if strings.TrimSpace(l) == "" || FenceMarker(strings.TrimLeft(l, " ")) != "" || thematicBreak(l) || atxHeading(l) || commentBlockEnd(lines, closer, k) >= 0 ||
			quoteDepth(l) > 0 || setextUnderline(l) > 0 {
			return false
		}
		if _, _, item := listMarker(l); item {
			return false
		}
		if strip := stripTags(l); strip.tags && len(strip.breaks) == 0 && strings.TrimSpace(strip.text) == "" {
			return false // a tag-only line is hidden
		}
		if k < noTable {
			return true
		}
		// A rejected table's lines are text: scan them once, as the main
		// loop does.
		table, n := TableLines(wm, lines[k:], theme, decorate)
		if table == nil && n > 0 {
			noTable = k + n
		}
		return table == nil
	}
	// decideSetext looks ahead from paragraph line k over its run: when the
	// run ends in an underline, all its lines are that heading.
	decideSetext := func(k int) {
		j := k + 1
		for j < len(lines) && paragraphLine(j) {
			j++
		}
		level := 0
		if j < len(lines) {
			level = setextUnderline(lines[j])
		}
		for r := k; r < j; r++ {
			setext[r] = level
		}
		if level > 0 {
			setext[j] = -1
		}
	}
	// An open indented code block (C2): its opener display line and the
	// code it copies; prevText is whether the previous line was paragraph or
	// quote text, which a code block cannot start under.
	codeOpener, prevText := -1, false
	var code strings.Builder
	closeCode := func(end int) {
		out[codeOpener].Block = strings.TrimSuffix(code.String(), "\n")
		closer := DisplayLine{Decor: true, Fence: FenceClose, Pieces: []Piece{{Style: spaceview.Style{FG: theme.Dim, BG: CodeCard(theme)}}}}
		out = append(out, closer)
		if srcs != nil {
			*srcs = append(*srcs, LineSource{Start: end, End: end})
		}
		codeOpener = -1
	}
	// codeContinues reports whether the blank line k is inside the open code
	// block: a later line indented by 4 or more comes before any line
	// indented by less.
	codeContinues := func(k int) bool {
		for ; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) != "" {
				col, _ := indentCols(lines[k])
				return col >= 4
			}
		}
		return false
	}
	for idx := 0; idx < len(lines); at, idx = at+len(lines[idx])+1, idx+1 {
		line := lines[idx]
		textAbove := prevText
		prevText = false
		col, _ := indentCols(line)
		blank := strings.TrimSpace(line) == ""
		if !source && fence == "" && idx > commentEnd {
			if _, _, item := listMarker(line); !item {
				lists.other(line)
			}
			if owned := col >= 4 || blank && codeContinues(idx); codeOpener >= 0 && !owned {
				closeCode(at - 1)
			}
			if codeOpener < 0 {
				if end := commentBlockEnd(lines, closer, idx); end >= 0 {
					commentEnd = end
				}
			}
		}
		if !source && idx <= commentEnd {
			// An open comment owns its lines: hidden, and the text after
			// "-->" on its last line is shown literally.
			out = append(out, commentLine(wm, lines[idx], idx == commentEnd, at, theme, srcs))
			continue
		}
		if !source && fence == "" {
			if codeOpener < 0 && !blank && col >= 4 && !lists.active() && !textAbove {
				// A code block starts: a card like a fence without a language.
				codeOpener = len(out)
				code.Reset()
				out = append(out, DisplayLine{Decor: true, Fence: FenceOpen, Pieces: []Piece{{Style: spaceview.Style{FG: theme.Dim, BG: CodeCard(theme)}}}})
				if srcs != nil {
					*srcs = append(*srcs, LineSource{Start: at, End: at})
				}
			}
			if codeOpener >= 0 {
				out = append(out, indentedCodeLine(wm, line, at, theme, srcs))
				code.WriteString(out[len(out)-1].Text + "\n")
				continue
			}
		}
		tableLimit := false
		// A fence opener always wins over a table, even with pipes in it.
		if !source && fence == "" && idx >= literal && FenceMarker(strings.TrimLeft(line, " ")) == "" {
			table, n := TableLines(wm, lines[idx:], theme, decorate)
			if table != nil {
				// A blank or whitespace-only neighbour already separates the
				// table; the message's own edges need nothing.
				table[0].Table.SpaceBefore = idx > 0 && strings.TrimSpace(lines[idx-1]) != ""
				table[0].Table.SpaceAfter = idx+n < len(lines) && strings.TrimSpace(lines[idx+n]) != ""
				out = append(out, table...)
				if srcs != nil {
					// A table's display lines are its source lines, rebuilt:
					// header, rule (the delimiter row), then each row.
					for k := range n {
						*srcs = append(*srcs, LineSource{Start: at, End: at + len(lines[idx+k])})
						if k < n-1 {
							at += len(lines[idx+k]) + 1
						}
					}
				}
				idx += n - 1
				continue
			}
			literal = idx + n
			tableLimit = n > 0
		}
		st := spaceview.Style{FG: theme.Text}
		trim := strings.TrimLeft(line, " ")
		dl := DisplayLine{TableLimit: tableLimit}
		ls := LineSource{Start: at, End: at + len(line)}
		var lead *SourceMap // line → lines[idx], when mapping
		if srcs != nil {
			lead = &SourceMap{Hi: len(line)}
		}
		prose := false
		if !source {
			if marker := FenceMarker(trim); marker != "" && (fence != "" && ClosesFence(trim, fence) || fence == "" && fenceIndentOK(line, lists.active())) {
				var note string
				if fence == "" {
					fence, fences = marker, fences+1
					dl.Text, dl.Fence = strings.TrimSpace(strings.TrimPrefix(trim, marker)), FenceOpen
					opener = len(out)
					block.Reset()
					lang, hl = syntaxFor(dl.Text), hlState{}
				} else {
					dl.Text, dl.Fence, fence = "", FenceClose, ""
					out[opener].Block = block.String()
					if diagrams != nil && IsMermaid(out[opener].Text) {
						if d, ok := diagrams(out[opener].Block); ok {
							note = d.Note
							if len(d.Lines) > 0 {
								out = drawDiagram(out, opener, fences, d.Lines, theme, srcs, openerSrcs, at)
							}
						}
					}
				}
				dl.Decor = true
				dl.Pieces = []Piece{{End: len(dl.Text), Style: spaceview.Style{FG: theme.Dim, BG: CodeCard(theme)}}}
				out = append(out, dl)
				if srcs != nil {
					ls.Fence = fences
					*srcs = append(*srcs, ls)
					if dl.Fence == FenceOpen {
						openerSrcs = len(*srcs)
					}
				}
				if note != "" {
					// One reason row after the card: shown, never copied.
					out = append(out, DisplayLine{Text: note, Decor: true, Pieces: []Piece{{End: len(note), Style: spaceview.Style{FG: theme.Dim, Italic: true}}}})
					if srcs != nil {
						*srcs = append(*srcs, LineSource{Start: ls.End, End: ls.End})
					}
				}
				continue
			}
			switch {
			case fence != "":
				dl.Code = true
				block.WriteString(line)
				if idx < len(lines)-1 {
					block.WriteByte('\n')
				}
				st = spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}
				ls.Fence = fences
			case setext[idx] == -1:
				// A setext underline: hidden; its heading's last line maps over it.
				dl.Decor, dl.Hidden = true, true
				dl.Pieces = []Piece{{Style: st}}
				out = append(out, dl)
				if srcs != nil {
					*srcs = append(*srcs, ls)
				}
				continue
			case thematicBreak(line):
				dl.Decor, dl.Rule = true, true
				dl.Pieces = []Piece{{Style: spaceview.Style{FG: theme.Frame}}}
				out = append(out, dl)
				if srcs != nil {
					*srcs = append(*srcs, ls)
				}
				continue
			case strings.HasPrefix(trim, "#"):
				prose = true
				if n := len(trim) - len(strings.TrimLeft(trim, "#")); n <= 6 && strings.HasPrefix(trim[n:], " ") {
					content := strings.TrimLeft(trim[n:], " ")
					from := len(lines[idx]) - len(content)
					line, st.FG, st.Bold = withoutClosingHashes(content), HeadingColor(theme, n), true
					lead.Add(line, lines[idx], from, from+len(line))
				}
			case quoteDepth(line) > 0:
				dl.QuoteDepth, line = quoteMarkers(line)
				st.FG, prose, dl.Quote = theme.Dim, true, true
				prevText = true
			default:
				line, dl.List, lead = tolerantListItem(line, lead, lists.active())
				prose = true
				if dl.List.Content == 0 && !lists.active() && strings.TrimSpace(lines[idx]) != "" {
					if _, known := setext[idx]; !known {
						decideSetext(idx)
					}
					prevText = setext[idx] == 0
					if level := setext[idx]; level > 0 {
						content := strings.TrimLeft(lines[idx], " \t")
						from := len(lines[idx]) - len(content)
						line, st.FG, st.Bold = strings.TrimRight(content, " \t"), HeadingColor(theme, level), true
						lead = nil
						if srcs != nil {
							lead = &SourceMap{Hi: len(lines[idx])}
							if setext[idx+1] == -1 {
								// A whole-line end of the heading's last line takes
								// its underline too.
								lead.Hi = len(lines[idx]) + 1 + len(lines[idx+1])
							}
							lead.Add(line, lines[idx], from, from+len(line))
						}
					}
				}
			}
			if dl.List.Content > 0 {
				marker, content, _ := listMarker(lines[idx])
				lists.item(marker, content)
			}
		}
		if lead != nil && len(lead.At) == 0 {
			// line is what remains of lines[idx] after a marker the view drops.
			lead.Add(line, lines[idx], len(lines[idx])-len(line), len(lines[idx]))
		}
		var tabs, inline *SourceMap
		if srcs != nil {
			tabs = &SourceMap{Hi: len(line)}
		}
		dl.Text = ExpandTabsMap(wm, line, tabs)
		var strip tagStrip
		if prose {
			var spans []Span
			strip = stripTags(dl.Text)
			if strip.tags {
				dl.Text = strip.text
			}
			if srcs != nil || strip.tags {
				inline = &SourceMap{Hi: len(dl.Text)}
			}
			dl.Text, spans = InlineMarkdownMap(dl.Text, st, theme, inline)
			for _, sp := range spans {
				p := Piece{Start: sp.Start, End: sp.End, Style: sp.Style, Code: sp.Code, Strike: sp.Strike, Auto: sp.Auto}
				if sp.Link != "" {
					p.Act = Action{Kind: IntentURL, Value: sp.Link}
				}
				dl.Pieces = append(dl.Pieces, p)
			}
			if strip.tags {
				dl.Pieces = styleTags(dl.Pieces, inline, strip.styles, theme)
			}
		} else if dl.Code {
			dl.Pieces, hl = highlightLine(lang, dl.Text, hl, st, theme)
		} else {
			dl.Pieces = []Piece{{End: len(dl.Text), Style: st}}
		}
		if !dl.Code {
			dl.Pieces = decorateTokens(dl.Text, dl.Pieces, theme, decorate, !source)
		}
		var breaks []int
		if strip.tags {
			for _, b := range strip.breaks {
				breaks = append(breaks, displayAt(inline, b))
			}
			if len(breaks) == 0 && strings.TrimSpace(dl.Text) == "" {
				// Nothing left to show after hiding its tags or comment.
				dl.Decor, dl.Hidden = true, true
			}
		}
		if srcs != nil {
			m := tabs.Then(lead)
			if strip.tags {
				m = strip.src.Then(m)
			}
			if inline != nil {
				m = inline.Then(m)
			}
			ls.Src = m.Shift(at)
		}
		parts, partSrcs := splitAtBreaks(dl, ls, breaks, srcs != nil)
		out = append(out, parts...)
		if srcs != nil {
			*srcs = append(*srcs, partSrcs...)
		}
	}
	if fence != "" {
		out[opener].Block = block.String() // an unclosed fence runs to the end
	}
	if codeOpener >= 0 {
		closeCode(len(Plain(text)))
	}
	alignOrdered(out)
	return out
}

// tolerantListItem is listItem for a line under the list context: a marker
// line whose indentation holds a TAB is an item only inside a list (B1), its
// indentation then shown as the spaces it expands to; outside one it stays
// text. lead maps the returned line to the source line.
func tolerantListItem(line string, lead *SourceMap, inList bool) (string, ListItem, *SourceMap) {
	col, at := indentCols(line)
	if !inList || !strings.Contains(line[:at], "\t") {
		text, item := listItem(line, lead)
		return text, item, lead
	}
	if _, _, ok := listMarker(line); !ok {
		text, item := listItem(line, lead)
		return text, item, lead
	}
	spaces := strings.Repeat(" ", col)
	norm := spaces + line[at:]
	var normMap, itemMap *SourceMap
	if lead != nil {
		normMap = &SourceMap{Hi: len(line)}
		normMap.Add(spaces, line, 0, at)
		normMap.Add(line[at:], line, at, len(line))
		itemMap = &SourceMap{Hi: len(norm)}
	}
	text, item := listItem(norm, itemMap)
	if lead == nil {
		return text, item, nil
	}
	if len(itemMap.At) == 0 {
		itemMap.Add(text, norm, 0, len(norm))
	}
	return text, item, itemMap.Then(normMap)
}

// quoteMarkers strips a quote line's markers: up to 3 columns of
// indentation, then each '>' with one optional space after it, a further
// level's '>' allowed after up to 3 more spaces. It returns the number of
// levels (0 when line is no quote) and the text after them.
func quoteMarkers(line string) (int, string) {
	col, i := 0, 0
	for ; i < len(line) && (line[i] == ' ' || line[i] == '\t'); i++ {
		if line[i] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
	}
	if col > 3 || i == len(line) || line[i] != '>' {
		return 0, line
	}
	depth := 0
	for i < len(line) && line[i] == '>' {
		depth++
		i++
		if i < len(line) && line[i] == ' ' {
			i++
		}
		next := i
		for next < len(line) && next-i < 3 && line[next] == ' ' {
			next++
		}
		if next < len(line) && line[next] == '>' {
			i = next
		}
	}
	return depth, line[i:]
}

func quoteDepth(line string) int {
	depth, _ := quoteMarkers(line)
	return depth
}

// splitAtBreaks splits a display line at the display offsets of its <br>
// tags. Each part keeps the line's quote and heading style; only the first
// is the list item. Each part has its own LineSource over its part of the
// source, the tag's bytes a hidden unit at the end of the part before it.
func splitAtBreaks(dl DisplayLine, ls LineSource, breaks []int, mapped bool) ([]DisplayLine, []LineSource) {
	if len(breaks) == 0 {
		if !mapped {
			return []DisplayLine{dl}, nil
		}
		return []DisplayLine{dl}, []LineSource{ls}
	}
	var parts []DisplayLine
	var srcs []LineSource
	bounds := append(append([]int{0}, breaks...), len(dl.Text))
	for k := 0; k+1 < len(bounds); k++ {
		a, b := bounds[k], bounds[k+1]
		part := dl
		part.Text = dl.Text[a:b]
		part.Pieces = nil
		for _, p := range dl.Pieces {
			empty := p.Start == p.End && p.Start >= a && p.Start <= b
			if (p.End <= a || p.Start >= b) && !empty {
				continue
			}
			p.Start, p.End = max(p.Start, a)-a, min(p.End, b)-a
			part.Pieces = append(part.Pieces, p)
		}
		if len(part.Pieces) == 0 && len(dl.Pieces) > 0 {
			part.Pieces = []Piece{{Style: dl.Pieces[0].Style}}
		}
		part.Subs = nil
		if k > 0 {
			part.List = ListItem{}
		}
		parts = append(parts, part)
		if !mapped {
			continue
		}
		m := ls.Src
		sub := &SourceMap{At: append([]int(nil), m.At[a:b]...), Units: m.Units, Lo: m.Lo, Hi: m.Hi}
		if k > 0 {
			sub.Lo = m.After(a)
		}
		if k+2 < len(bounds) {
			sub.Hi = m.After(b)
		}
		srcs = append(srcs, LineSource{Start: sub.Lo, End: sub.Hi, Src: sub, Fence: ls.Fence})
	}
	return parts, srcs
}

// commentBlockEnd is the closing line of a comment block opened at line k:
// its first text, after up to 3 columns of indentation, is "<!--", and a
// "-->" follows on that line or a later one. -1 when line k opens none: an
// unclosed comment is shown as it is.
func commentBlockEnd(lines []string, closer []int, k int) int {
	col, at := indentCols(lines[k])
	if col > 3 || !strings.HasPrefix(lines[k][at:], "<!--") {
		return -1
	}
	if strings.Contains(lines[k][at+4:], "-->") {
		return k
	}
	return closer[k+1]
}

// commentLine is a line of a comment block: hidden, unless it is the last
// line and text follows its "-->", which is shown literally (no inline
// Markdown, no tags). The hidden comment is a unit before that text.
func commentLine(wm ansi.Method, line string, last bool, at int, theme spaceview.Theme, srcs *[]LineSource) DisplayLine {
	suffix, from := "", len(line)
	if last {
		start := 0
		if open := strings.Index(line, "<!--"); open >= 0 {
			start = open + 4
		}
		from = start + strings.Index(line[start:], "-->") + 3
		for from < len(line) && (line[from] == ' ' || line[from] == '\t') {
			from++ // whitespace after the comment is not shown
		}
		suffix = line[from:]
	}
	if strings.TrimSpace(suffix) == "" {
		if srcs != nil {
			*srcs = append(*srcs, LineSource{Start: at, End: at + len(line)})
		}
		return DisplayLine{Decor: true, Hidden: true, Pieces: []Piece{{Style: spaceview.Style{FG: theme.Text}}}}
	}
	var tabs, lead *SourceMap
	if srcs != nil {
		tabs = &SourceMap{Hi: len(suffix)}
		lead = &SourceMap{Hi: len(line)}
		lead.Add(suffix, line, from, len(line))
	}
	dl := DisplayLine{Text: ExpandTabsMap(wm, suffix, tabs)}
	dl.Pieces = []Piece{{End: len(dl.Text), Style: spaceview.Style{FG: theme.Text}}}
	if srcs != nil {
		*srcs = append(*srcs, LineSource{Start: at, End: at + len(line), Src: tabs.Then(lead).Shift(at)})
	}
	return dl
}

// fenceIndentOK reports whether a fence opener's indentation lets it open
// one: up to 3 columns, or any inside a list, where items are not containers
// in this parser (today's leniency).
func fenceIndentOK(line string, inList bool) bool {
	col, _ := indentCols(line)
	return col <= 3 || inList
}

// indentedCodeLine is one line of an indented code block: its text without
// the first 4 columns of indentation (TABs expanded), unwrapped and not
// highlighted. A whole-line Quote takes its source line, indentation
// included, so the quotation stays code.
func indentedCodeLine(wm ansi.Method, line string, at int, theme spaceview.Theme, srcs *[]LineSource) DisplayLine {
	cut, col := 0, 0
	for cut < len(line) && col < 4 && (line[cut] == ' ' || line[cut] == '\t') {
		if line[cut] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
		cut++
	}
	var tabs, lead *SourceMap
	if srcs != nil {
		tabs = &SourceMap{Hi: len(line) - cut}
		lead = &SourceMap{Hi: len(line)}
		lead.Add(line[cut:], line, cut, len(line))
	}
	dl := DisplayLine{Code: true}
	dl.Text = ExpandTabsMap(wm, line[cut:], tabs)
	dl.Pieces = []Piece{{End: len(dl.Text), Style: spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}}}
	if srcs != nil {
		*srcs = append(*srcs, LineSource{Start: at, End: at + len(line), Src: tabs.Then(lead).Shift(at)})
	}
	return dl
}

// setextUnderline is the level of a setext underline line: up to 3 columns
// of indentation, then one or more '=' (1) or '-' (2), then only spaces or
// TABs; 0 for any other line.
func setextUnderline(line string) int {
	col, at := indentCols(line)
	if col > 3 || at == len(line) || line[at] != '=' && line[at] != '-' {
		return 0
	}
	mark := line[at]
	end := at
	for end < len(line) && line[end] == mark {
		end++
	}
	if strings.Trim(line[end:], " \t") != "" {
		return 0
	}
	if mark == '=' {
		return 1
	}
	return 2
}

// atxHeading reports a '#' heading line as parseLines reads one.
func atxHeading(line string) bool {
	trim := strings.TrimLeft(line, " ")
	n := len(trim) - len(strings.TrimLeft(trim, "#"))
	return n > 0 && n <= 6 && strings.HasPrefix(trim[n:], " ")
}

// thematicBreak reports a rule line: up to 3 columns of indentation (TABs to
// the next multiple of 4), then 3 or more of one of '-', '*' or '_',
// optionally separated by spaces or TABs, and nothing else.
func thematicBreak(line string) bool {
	col, i := 0, 0
	for ; i < len(line) && (line[i] == ' ' || line[i] == '\t'); i++ {
		if line[i] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
	}
	if col > 3 {
		return false
	}
	var mark byte
	n := 0
	for ; i < len(line); i++ {
		switch c := line[i]; {
		case c == ' ' || c == '\t':
		case (c == '-' || c == '*' || c == '_') && (mark == 0 || c == mark):
			mark, n = c, n+1
		default:
			return false
		}
	}
	return n >= 3
}

// withoutClosingHashes drops an ATX heading's closing sequence: the final run
// of unescaped '#', preceded by a space or TAB and followed only by spaces or
// TABs, with the whitespace before it. A heading of only such a run is empty.
func withoutClosingHashes(s string) string {
	t := strings.TrimRight(s, " \t")
	j := len(t)
	for j > 0 && t[j-1] == '#' {
		j--
	}
	switch {
	case j == len(t):
		return s
	case j == 0:
		return ""
	case t[j-1] != ' ' && t[j-1] != '\t':
		return s // "T#", or an escaped first '#'
	}
	return strings.TrimRight(t[:j], " \t")
}

// HeadingColor is the text colour of a heading of level n (1–6), shown bold
// at the terminal's cell size: the accent for level 1, body text for level 2,
// the softer label colour for level 3; levels 4–6 keep the accent.
func HeadingColor(theme spaceview.Theme, n int) spaceview.Color {
	switch n {
	case 2:
		return theme.Text
	case 3:
		return theme.Label
	}
	return theme.Brand
}

// linked reports whether a Markdown link already targets part of
// [start, end): a reference never replaces its destination.
func Linked(pieces []Piece, start, end int) bool {
	for _, p := range pieces {
		if p.Act.Kind == IntentURL && p.Start < end && p.End > start {
			return true
		}
	}
	return false
}

// overlay applies f to the parts of pieces inside [start, end), splitting
// pieces at the boundaries. Ranges touching inline code or an angle
// autolink stay unchanged.
func Overlay(pieces []Piece, start, end int, f func(Piece) Piece) []Piece {
	for _, p := range pieces {
		if (p.Code || p.Auto) && p.Start < end && p.End > start {
			return pieces
		}
	}
	var out []Piece
	for _, p := range pieces {
		if p.End <= start || p.Start >= end {
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
		out = append(out, f(mid))
		if p.End > end {
			right := p
			right.Start = end
			out = append(out, right)
		}
	}
	return out
}

// bodyRows wraps display lines into rows at most width cells wide. A table
// that borders a non-blank line gets an empty decor row on that side, once
// per table whatever its layout. A spacer belongs to the logical line it
// borders on the far side (the table's first line before it, the line after
// the table after it), so a press on one selects from that line's start.
// Muted table dividers sit between logical body rows, after wrapped fragments.
// Every added row counts toward maxBodyRows: the row that reaches it is
// followed only by the limit notice.
func bodyRows(wm ansi.Method, lines []DisplayLine, width int, theme spaceview.Theme, limited *bool, options LayoutOptions, refused *[]TableRowID) []BodyRow {
	rowLimit := MaxBodyRows
	if options.UnlimitedRows {
		rowLimit = int(^uint(0) >> 1)
	}
	var rows []BodyRow
	cards, pad := CodeCards(wm, lines, width), CardPad(width)
	tableIndex := -1
	var fitted [][]BodyRow // the current table's rows, nil when it renders as text
	var rowRule []Run      // shared decorative divider between logical body rows
	first := 0
	limit := func(n, at int) []BodyRow {
		if limited != nil {
			*limited = true
		}
		return append(rows, BodyRow{Line: n, Start: at, End: at, Decor: true, Runs: []Run{Deco("[display row limit; Copy message retains the entire original]", spaceview.Style{FG: theme.Bad})}})
	}
	for n, dl := range lines {
		if dl.Hidden {
			continue
		}
		tbl := dl.Table
		if dl.TableLimit {
			rows = append(rows, tableNotice(wm, n, width, theme, "Table over the display limit; showing its text."))
			if len(rows) >= rowLimit {
				return limit(n, 0)
			}
		}
		if tbl != nil && (n == 0 || lines[n-1].Table != tbl) {
			tableIndex++
			if tbl.SpaceBefore {
				rows = append(rows, BodyRow{Line: n, Decor: true})
				if len(rows) >= rowLimit {
					return limit(n, 0)
				}
			}
			after := 0
			if tbl.SpaceAfter {
				after = 1 // the spacer after the table needs a row too
			}
			var reason TableFitReason
			var denied []TableRowID
			fitted, reason, denied = fitTableInView(wm, lines[n:], n, tableIndex, width, rowLimit-len(rows)-after, theme, options)
			*refused = append(*refused, denied...)

			first = n
			if reason != TableFits && reason != TableLaidOut {
				fitted = labelledTable(wm, lines[n:], n, width, rowLimit-len(rows)-after, theme, reason)
				if fitted == nil {
					rows = append(rows, tableNotice(wm, n, width, theme, "Table over the display limit; showing its text."))
					if len(rows) >= rowLimit {
						return limit(n, 0)
					}
				}
			}
			rowRule = nil
			if (reason == TableFits || reason == TableLaidOut) && n+1 < len(lines) && lines[n+1].Table == tbl && lines[n+1].Decor {
				rule := lines[n+1].Text
				if fitted != nil {
					rule = ""
					for _, run := range fitted[1][0].Runs {
						rule += run.Text
					}
				}
				rowRule = TableRowRule(wm, rule, width, theme)
			}
		}
		if tbl != nil && rowRule != nil && n > first+2 && dl.Cells != nil {
			rows = append(rows, BodyRow{Line: n, Decor: true, Runs: rowRule})
			if len(rows) >= rowLimit {
				return limit(n, 0)
			}
		}
		var prefix, suffix []Run
		textWidth := width
		if cards[n] > 0 {
			// Every row of a fenced block, frames included, fills its card.
			bg := spaceview.Style{BG: CodeCard(theme)}
			textWidth = max(cards[n]-2*pad, 1)
			prefix = []Run{Deco(strings.Repeat(" ", pad), bg)}
			suffix = []Run{Deco("", bg)}
		}
		switch {
		case tbl != nil && fitted != nil:
			rows = append(rows, fitted[n-first]...)
		case dl.Decor:
			text := dl.Text
			if dl.Fence != FenceNone {
				text = Truncate(wm, text, textWidth, "…")
			}
			if dl.Rule {
				text = strings.Repeat("─", textWidth)
			}
			runs := append(CloneRuns(prefix), Deco(text, dl.Pieces[0].Style))
			if button := CopyButtonRuns(wm, dl, textWidth, theme); button != nil {
				runs = append(CloneRuns(prefix), button...)
			}
			runs = append(runs, suffix...)
			rows = append(rows, BodyRow{Line: n, Runs: PadCard(wm, runs, cards[n]), Decor: true})
			if len(rows) >= rowLimit {
				return limit(n, 0)
			}
		default:
			if dl.Quote {
				// One bar per level, each two cells; at least one is drawn and
				// the text keeps a cell: deeper levels than fit are not drawn.
				bars := max(min(dl.QuoteDepth, (width-1)/2), 1)
				prefix = []Run{Deco(strings.Repeat("│ ", bars), spaceview.Style{FG: theme.Frame})}
				textWidth = max(width-2*bars, 1)
			}
			ranges, pad, hang := WrapRanges(wm, dl.Text, textWidth, !dl.Code), 0, 0
			if r, p, h, ok := listRanges(wm, dl, textWidth); ok {
				ranges, pad, hang = r, p, h
			}
			for k, r := range ranges {
				runs := CloneRuns(prefix)
				gutter := hang
				if k == 0 {
					gutter = pad
				}
				if gutter > 0 {
					runs = append(runs, Deco(strings.Repeat(" ", gutter), spaceview.Style{}))
				}
				for _, p := range dl.Pieces {
					s, e := max(p.Start, r[0]), min(p.End, r[1])
					if s < e {
						runs = append(runs, Run{Text: dl.Text[s:e], Style: p.Style, Copy: true, Start: s, Act: p.Act, Strike: p.Strike})
					}
				}
				runs = PadCard(wm, append(runs, suffix...), cards[n])
				rows = append(rows, BodyRow{Line: n, Start: r[0], End: r[1], Runs: runs})
				if len(rows) >= rowLimit {
					return limit(n, r[1])
				}
			}
		}
		if tbl != nil && tbl.SpaceAfter && (n+1 == len(lines) || lines[n+1].Table != tbl) {
			rows = append(rows, BodyRow{Line: n + 1, Decor: true})
			if len(rows) >= rowLimit {
				return limit(n+1, 0)
			}
		}
	}
	return rows
}

// listMinText is the narrowest text column a list item keeps beside its
// gutter; in a narrower view the item wraps from the row start as before.
const listMinText = 8

// listRanges wraps a list item so that its continuation rows align under
// its text: the item's text wraps to the width left beside the marker and
// its Pad, and the first row also holds the indentation and marker. pad is
// the decorative cells before the first row, hang those before the others.
// ok is false for other lines and when the text column would be narrower
// than listMinText.
func listRanges(wm ansi.Method, dl DisplayLine, textWidth int) (ranges [][2]int, pad, hang int, ok bool) {
	li := dl.List
	if li.Content == 0 || li.Content > len(dl.Text) || dl.Code || dl.Quote {
		return nil, 0, 0, false
	}
	hang = li.Pad + wm.StringWidth(dl.Text[:li.Content])
	if textWidth-hang < listMinText {
		return nil, 0, 0, false
	}
	ranges = WrapRanges(wm, dl.Text[li.Content:], textWidth-hang, true)
	for k := range ranges {
		ranges[k][0], ranges[k][1] = ranges[k][0]+li.Content, ranges[k][1]+li.Content
	}
	ranges[0][0] = 0
	return ranges, li.Pad, hang, true
}

// codeCards sizes each fenced block, from its opener through its closer, to
// its widest code line or label plus one cell of padding on each side,
// within width. Other lines get 0.
func CodeCards(wm ansi.Method, lines []DisplayLine, width int) []int {
	cards := make([]int, len(lines))
	pad := CardPad(width)
	for n := 0; n < len(lines); {
		if lines[n].Fence != FenceOpen {
			n++
			continue
		}
		end, inner := n+1, wm.StringWidth(lines[n].Text)
		if lines[n].Block != "" {
			inner += CopyGap(lines[n].Text) + wm.StringWidth(CopyLabel)
		}
		for end < len(lines) && lines[end].Code {
			inner = max(inner, wm.StringWidth(lines[end].Text))
			end++
		}
		if end < len(lines) && lines[end].Fence == FenceClose {
			end++
		}
		for k := n; k < end; k++ {
			cards[k] = min(inner+2*pad, width)
		}
		n = end
	}
	return cards
}

// copyLabel is a fenced block's copy button, right-aligned on its label row.
const CopyLabel = "copy"

// copyGap separates a block's language label from its copy button.
func CopyGap(label string) int {
	if label == "" {
		return 0
	}
	return 2
}

// copyButtonRuns renders a fence opener with code as its label, truncated
// first, and the copy button flush right within textWidth. It is nil when
// the block is empty or the card is too narrow for the button: the row then
// shows the label alone and Alt+C still copies the first block.
func CopyButtonRuns(wm ansi.Method, dl DisplayLine, textWidth int, theme spaceview.Theme) []Run {
	button := wm.StringWidth(CopyLabel)
	if dl.Fence != FenceOpen || dl.Block == "" || textWidth < button {
		return nil
	}
	label, room := "", textWidth-button-CopyGap(dl.Text)
	if room > 0 {
		label = Truncate(wm, dl.Text, room, "…")
	}
	bg := spaceview.Style{BG: CodeCard(theme)}
	return []Run{
		Deco(label, dl.Pieces[0].Style),
		Deco(strings.Repeat(" ", textWidth-button-wm.StringWidth(label)), bg),
		{Text: CopyLabel, Style: spaceview.Style{FG: theme.Label, BG: CodeCard(theme)}, Act: Action{Kind: IntentCopyCode, Value: dl.Block}},
	}
}

// cardPad is a card's padding on each side: one cell, none on views too
// narrow to spare it.
func CardPad(width int) int { return min(width/3, 1) }

// padCard widens a code row's last run, its card padding, so the row
// fills its card exactly; other rows are unchanged.
func PadCard(wm ansi.Method, runs []Run, card int) []Run {
	if gap := card - RunsWidth(wm, runs); card > 0 && gap > 0 {
		runs[len(runs)-1].Text += strings.Repeat(" ", gap)
	}
	return runs
}

// Table bounds. Padding columns to their widest cell can multiply a skewed
// table's size, so a table over these bounds, or whose aligned rendering
// would exceed twice its source plus tableSlack bytes, stays literal text.
// The same budget bounds a table wrapped to the view (see fitTable).
const (
	MaxTableColumns = 32
	MaxTableRows    = 512
	TableSlack      = 4 << 10
	TableFloor      = 4 // cells a wrapped column keeps, if it is that wide
)

// tableLines renders a Markdown table at the start of lines: a header row, a
// delimiter row with the same number of cells, then body rows until a line
// without a pipe. Columns align to their widest cell, as the delimiter row
// asks; bodyRows narrows them to a smaller view (see fitTable). The delimiter
// becomes a frame line that is never copied; cells copy as laid out here,
// separators and padding included, whatever the view's width.
// n is the number of source lines the table spans, 0 when lines does not
// start one. A table over its bounds returns no lines but still its span,
// which the caller then shows as text without detecting a table inside it,
// so every line is examined a bounded number of times.
func TableLines(wm ansi.Method, lines []string, theme spaceview.Theme, decorate Decorator) (out []DisplayLine, n int) {
	if len(lines) < 2 {
		return nil, 0
	}
	header, ok := TableCells(lines[0])
	align, delim := TableAlign(lines[1])
	if !ok || !delim || len(align) != len(header) {
		return nil, 0
	}
	n = 2
	for n < len(lines) && TableRow(lines[n]) {
		n++
	}
	if len(header) > MaxTableColumns || n-2 > MaxTableRows {
		return nil, n
	}
	rows := [][]string{header}
	source := len(lines[0]) + len(lines[1]) + 2
	for _, line := range lines[2:n] {
		cells, _ := TableCells(line)
		cells = append(cells, make([]string, max(len(header)-len(cells), 0))...)
		rows, source = append(rows, cells[:len(header)]), source+len(line)+1
	}
	type cell struct {
		text  string
		spans []Span
	}
	shown := make([][]cell, len(rows))
	widths := make([]int, len(header))
	for r, cells := range rows {
		st := spaceview.Style{FG: theme.Text, Bold: r == 0}
		for c, raw := range cells {
			text, spans := InlineMarkdown(ExpandTabs(wm, raw), st, theme)
			shown[r] = append(shown[r], cell{text, spans})
			widths[c] = max(widths[c], wm.StringWidth(text))
		}
	}
	// The exact size of the aligned rows and rule, checked before any
	// padding is allocated.
	sep := len(" │ ") * (len(widths) - 1)
	total := len("─┼─") * (len(widths) - 1)
	for _, w := range widths {
		total += w * len("─")
	}
	for _, cells := range shown {
		total += sep
		for c, cl := range cells {
			total += len(cl.text) + widths[c] - wm.StringWidth(cl.text)
		}
	}
	unpadded := total > 2*source+TableSlack
	if unpadded {
		logical := len("─┼─") * (len(widths) - 1)
		for _, cells := range shown {
			logical += sep
			for _, cl := range cells {
				logical += len(cl.text)
			}
		}
		if logical > 2*source+TableSlack {
			return nil, n
		}
	}
	tbl := &TableInfo{Align: align, Natural: widths, Floor: make([]int, len(widths)), WordFloor: make([]int, len(widths)), CompactFloor: make([]int, len(widths)), Source: source, Unpadded: unpadded}
	for c, w := range widths {
		for _, cells := range shown {
			tbl.WordFloor[c] = max(tbl.WordFloor[c], tableRunWidth(wm, cells[c].text))
			tbl.CompactFloor[c] = max(tbl.CompactFloor[c], WidestGrapheme(wm, cells[c].text)+wm.StringWidth("…"))
		}
		tbl.CompactFloor[c] = min(w, tbl.CompactFloor[c])
		// A wrapped column keeps a few cells, and always its widest
		// grapheme, which cannot break.
		tbl.Floor[c] = min(w, TableFloor)
		if w > TableFloor {
			for _, cells := range shown {
				tbl.Floor[c] = max(tbl.Floor[c], WidestGrapheme(wm, cells[c].text))
			}
		}
	}
	frame := spaceview.Style{FG: theme.Frame}
	for r, cells := range shown {
		dl := DisplayLine{Table: tbl, Cells: make([][2]int, len(cells))}
		var b strings.Builder
		add := func(text string, sp Span) {
			start := b.Len()
			b.WriteString(text)
			if text != "" {
				dl.Pieces = append(dl.Pieces, Piece{Start: start + sp.Start, End: start + sp.End, Style: sp.Style, Code: sp.Code, Strike: sp.Strike})
			}
		}
		for c, cl := range cells {
			if c > 0 {
				add(" │ ", Span{End: len(" │ "), Style: frame})
			}
			gap := widths[c] - wm.StringWidth(cl.text)
			if unpadded {
				gap = 0
			}
			left := 0
			switch align[c] {
			case 'r':
				left = gap
			case 'c':
				left = gap / 2
			}
			add(strings.Repeat(" ", left), Span{End: left, Style: spaceview.Style{FG: theme.Text}})
			start := b.Len()
			b.WriteString(cl.text)
			dl.Cells[c] = [2]int{start, b.Len()}
			for _, sp := range cl.spans {
				p := Piece{Start: start + sp.Start, End: start + sp.End, Style: sp.Style, Code: sp.Code, Strike: sp.Strike, Auto: sp.Auto}
				if sp.Link != "" {
					p.Act = Action{Kind: IntentURL, Value: sp.Link}
				}
				dl.Pieces = append(dl.Pieces, p)
			}
			add(strings.Repeat(" ", gap-left), Span{End: gap - left, Style: spaceview.Style{FG: theme.Text}})
		}
		dl.Text = strings.TrimRight(b.String(), " ")
		for c, cr := range dl.Cells {
			// Trimming can only shorten the last cell's own trailing spaces.
			end := min(cr[1], len(dl.Text))
			dl.Cells[c] = [2]int{min(cr[0], end), end}
		}
		var kept []Piece
		for _, p := range dl.Pieces {
			if p.Start < len(dl.Text) {
				p.End = min(p.End, len(dl.Text))
				kept = append(kept, p)
			}
		}
		dl.Pieces = decorateTokens(dl.Text, kept, theme, decorate, true)
		out = append(out, dl)
		if r == 0 {
			parts := make([]string, len(widths))
			for c, w := range widths {
				if !unpadded {
					parts[c] = strings.Repeat("─", w)
				}
			}
			rule := strings.Join(parts, "─┼─")
			out = append(out, DisplayLine{Text: rule, Decor: true, Pieces: []Piece{{End: len(rule), Style: frame}}, Table: tbl})
		}
	}
	return out, n
}

// FitTableLayout lays out the table whose display lines start lines when its
// aligned rows are wider than width: the columns share the view (waterFill)
// and each cell wraps inside its column, so separators stay in place and the
// rule matches. first is the index of lines[0] in the message. It returns
// the rows of each table line and a reason. Natural fit remains unchanged;
// squeezed columns below 16 cells request labelled output. Refusal also occurs when the
// layout would exceed the table's budget (twice its source plus tableSlack
// bytes) or the rows left. Everything is counted before a row is built. A
// row's fragments keep their exact bytes, hanging spaces included, so copy,
// selection and the reading position all see the same logical line.
func FitTableLayout(wm ansi.Method, lines []DisplayLine, first, width, rowsLeft int, theme spaceview.Theme) ([][]BodyRow, TableFitReason) {
	return fitTableColumns(wm, lines, first, width, rowsLeft, theme, lines[0].Table.Floor, true, false)
}

func fitTableColumns(wm ansi.Method, lines []DisplayLine, first, width, rowsLeft int, theme spaceview.Theme, floor []int, cutoff, clipHeader bool) ([][]BodyRow, TableFitReason) {
	tbl := lines[0].Table
	k := 1
	for k < len(lines) && lines[k].Table == tbl {
		k++
	}
	lines = lines[:k]
	cols := len(tbl.Natural)
	avail := width - 3*(cols-1) // a separator takes three cells
	natural := 0
	for _, w := range tbl.Natural {
		natural += w
	}
	if natural <= avail && !tbl.Unpadded {
		return nil, TableFits
	}
	alloc, ok := WaterFill(tbl.Natural, floor, avail)
	if !ok {
		return nil, TableTooNarrow
	}
	for c, w := range alloc {
		if cutoff && w < tbl.Natural[c] && w < 16 {
			return nil, TableTooNarrow
		}
	}
	budget := tableLayoutBudget(tbl, width)
	used := len("─┼─") * (cols - 1)
	for _, w := range alloc {
		used += w * len("─")
	}
	frags := make([][][][2]int, len(lines)) // per line, cell and row
	heights := make([]int, len(lines))
	clippedHeader := make([]bool, cols)
	visual := max(len(lines)-3, 0) // one divider between consecutive body rows
	for i, dl := range lines {
		heights[i] = 1
		if dl.Cells != nil {
			frags[i] = make([][][2]int, cols)
			for c, cr := range dl.Cells {
				if cr[0] == cr[1] {
					continue
				}
				text := dl.Text[cr[0]:cr[1]]
				var rs [][2]int
				if clipHeader && i == 0 && tableRunWidth(wm, text) > alloc[c] {
					end := len(Truncate(wm, text, max(alloc[c]-wm.StringWidth("…"), 0), ""))
					rs = [][2]int{{0, end}}
					clippedHeader[c] = true
				} else {
					rs = tableWrapRanges(wm, text, alloc[c])
				}
				for j := range rs {
					rs[j][0], rs[j][1] = rs[j][0]+cr[0], rs[j][1]+cr[0]
				}
				frags[i][c], heights[i] = rs, max(heights[i], len(rs))
			}
			for v := range heights[i] {
				used += len(" │ ") * (cols - 1)
				for c := range cols {
					if v >= len(frags[i][c]) {
						if c < cols-1 {
							used += alloc[c]
						}
						continue
					}
					f := frags[i][c][v]
					e := DrawnEnd(wm, dl.Text, f, alloc[c])
					shown := wm.StringWidth(dl.Text[f[0]:e])
					if i == 0 && clippedHeader[c] {
						shown += wm.StringWidth("…")
						used += len("…")
					}
					if shown > alloc[c] {
						return nil, TableTooNarrow // never left to paint's truncation
					}
					left, right := CellGaps(tbl.Align[c], alloc[c]-shown, c == cols-1)
					used += e - f[0] + left + right
				}
				if used > budget {
					return nil, TableOverBudget
				}
			}
		}
		if visual += heights[i]; visual >= rowsLeft {
			return nil, TableNoRows
		}
	}

	frame, plainStyle := spaceview.Style{FG: theme.Frame}, spaceview.Style{FG: theme.Text}
	out := make([][]BodyRow, len(lines))
	for i, dl := range lines {
		if dl.Cells == nil {
			parts := make([]string, cols)
			for c, w := range alloc {
				parts[c] = strings.Repeat("─", w)
			}
			out[i] = []BodyRow{{Line: first + i, Runs: []Run{Deco(strings.Join(parts, "─┼─"), frame)}, Decor: true}}
			continue
		}
		for v := range heights[i] {
			row := BodyRow{Line: first + i, Start: len(dl.Text)}
			for c := range cols {
				if c > 0 {
					row.Runs = append(row.Runs, Deco(" │ ", frame))
				}
				if v >= len(frags[i][c]) {
					if c < cols-1 {
						row.Runs = append(row.Runs, Deco(strings.Repeat(" ", alloc[c]), plainStyle))
					}
					continue
				}
				f := frags[i][c][v]
				e := DrawnEnd(wm, dl.Text, f, alloc[c])
				shown := wm.StringWidth(dl.Text[f[0]:e])
				if i == 0 && clippedHeader[c] {
					shown += wm.StringWidth("…")
				}
				left, right := CellGaps(tbl.Align[c], alloc[c]-shown, c == cols-1)
				if left > 0 {
					row.Runs = append(row.Runs, Deco(strings.Repeat(" ", left), plainStyle))
				}
				for _, p := range dl.Pieces {
					if s, pe := max(p.Start, f[0]), min(p.End, e); s < pe {
						row.Runs = append(row.Runs, Run{Text: dl.Text[s:pe], Style: p.Style, Copy: true, Start: s, Act: p.Act, Strike: p.Strike})
					}
				}
				if i == 0 && clippedHeader[c] {
					row.Runs = append(row.Runs, Run{Text: "…", Style: spaceview.Style{FG: theme.Dim}, Tail: dl.Cells[c][1]})
				}
				if right > 0 {
					row.Runs = append(row.Runs, Deco(strings.Repeat(" ", right), plainStyle))
				}
				row.Frags = append(row.Frags, f)
				row.Start, row.End = min(row.Start, f[0]), max(row.End, f[1])
			}
			if row.Frags == nil { // a row of empty cells
				row.Start, row.End = 0, len(dl.Text)
			}
			out[i] = append(out[i], row)
		}
	}
	return out, TableLaidOut
}

// waterFill divides avail cells among columns of the given natural widths
// and floors (floor <= natural): every column gets the highest common level
// that fits, clamped to [floor, natural], so a column under that level keeps
// its natural width; cells still left go one each, left to right, to the
// columns at the level. ok is false when the floors alone do not fit.
func WaterFill(natural, floor []int, avail int) (alloc []int, ok bool) {
	at := func(c, level int) int { return min(natural[c], max(floor[c], level)) }
	sum := func(level int) int {
		total := 0
		for c := range natural {
			total += at(c, level)
		}
		return total
	}
	if sum(0) > avail {
		return nil, false
	}
	lo, hi := 0, slices.Max(natural)
	for lo < hi { // the highest level whose columns fit
		if mid := (lo + hi + 1) / 2; sum(mid) <= avail {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	alloc, extra := make([]int, len(natural)), avail-sum(lo)
	for c := range natural {
		alloc[c] = at(c, lo)
		if extra > 0 && alloc[c] == lo && lo < natural[c] {
			alloc[c]++
			extra--
		}
	}
	return alloc, true
}

// cellGaps splits the cells a fragment leaves free in its column into
// padding before and after it, by the column's alignment; the last column
// needs none after.
func CellGaps(align byte, gap int, last bool) (before, after int) {
	switch align {
	case 'r':
		before = gap
	case 'c':
		before = gap / 2
	}
	if last {
		return before, 0
	}
	return before, gap - before
}

// drawnEnd is where fragment f of text stops being drawn: a space that
// wrapRanges left hanging at the column's edge stays in the fragment's
// bytes, which copy, but is not drawn.
func DrawnEnd(wm ansi.Method, text string, f [2]int, width int) int {
	e := f[1]
	for e > f[0] && text[e-1] == ' ' && wm.StringWidth(text[f[0]:e]) > width {
		e--
	}
	return e
}

// widestGrapheme returns the width of the widest grapheme cluster in s.
func WidestGrapheme(wm ansi.Method, s string) int {
	widest := 0
	for o := 0; o < len(s); {
		g, w := ansi.FirstGraphemeCluster(s[o:], wm)
		widest, o = max(widest, w), o+len(g)
	}
	return widest
}

// tableRow reports whether a line continues a table: it has a separator
// pipe (see tableSeparators) and does not start another block (a fence,
// heading, quote or list item), as in GFM. It allocates nothing unless the
// line has backticks.
func TableRow(line string) bool {
	trim := strings.TrimLeft(line, " ")
	bullet := len(trim) >= 2 && strings.IndexByte("-*+", trim[0]) >= 0 && trim[1] == ' '
	if FenceMarker(trim) != "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ">") || bullet || OrderedItem(trim) {
		return false
	}
	found := false
	tableSeparators(trim, func(int) bool {
		found = true
		return false
	})
	return found
}

// tableSeparators calls sep with the index of each pipe of t that separates
// table cells, in order, until sep returns false: a pipe that is neither
// escaped nor inside a code span on the line. A code span pairs a backtick
// run with a later run of the same length, as inlineMarkdown pairs them:
// the first later run at least as long must be exactly as long, otherwise
// the opening backticks are literal. Backslashes inside a span are literal.
// The runs are found in one pass and each run's closer by a binary search,
// so no suffix of t is scanned again.
func tableSeparators(t string, sep func(int) bool) {
	var runs [][2]int   // each maximal backtick run: start, length
	var closer [][2]int // per run, the closing run of an opener of its length (or one less, after an escaped backtick); -1 for none
	if strings.IndexByte(t, '`') >= 0 {
		for i := 0; i < len(t); {
			if t[i] != '`' {
				i++
				continue
			}
			n := countRun(t[i:], '`')
			runs, i = append(runs, [2]int{i, n}), i+n
		}
		closer = make([][2]int, len(runs))
		// stair holds the runs after k that are longer than every run
		// between k and them, nearest last: their lengths strictly decrease
		// along the slice. The first later run at least want long is on it.
		var stair []int
		for k := len(runs) - 1; k >= 0; k-- {
			for d := range 2 {
				closer[k][d] = -1
				want := runs[k][1] - d
				if want <= 0 {
					continue
				}
				if p := sort.Search(len(stair), func(x int) bool { return runs[stair[x]][1] < want }); p > 0 && runs[stair[p-1]][1] == want {
					closer[k][d] = stair[p-1]
				}
			}
			for len(stair) > 0 && runs[stair[len(stair)-1]][1] <= runs[k][1] {
				stair = stair[:len(stair)-1]
			}
			stair = append(stair, k)
		}
	}
	r := 0 // the run at or after i
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '\\':
			i++ // an escaped character, a backtick included, is literal
		case '|':
			if !sep(i) {
				return
			}
		case '`':
			for runs[r][0]+runs[r][1] <= i {
				r++
			}
			end := runs[r][0] + runs[r][1] // past the opening backticks
			if d := i - runs[r][0]; d < 2 && closer[r][d] >= 0 {
				c := runs[closer[r][d]]
				end = c[0] + c[1] // past the closing backticks
			}
			i = end - 1
		}
	}
}

// orderedItem reports whether a line starts an ordered list item ("1. ").
func OrderedItem(trim string) bool {
	digits := len(trim) - len(strings.TrimLeft(trim, "0123456789"))
	return digits > 0 && digits < 10 && (strings.HasPrefix(trim[digits:], ". ") || strings.HasPrefix(trim[digits:], ") "))
}

// tableCells splits a table row on its separator pipes (see
// tableSeparators), dropping one leading and one trailing pipe. One scan
// decides every pipe, so an escaped backslash before a pipe (\\|) leaves that
// pipe a separator, and a pipe inside a code span stays in its cell. An
// escaped character stays in its cell; inline Markdown unescapes it. ok is
// false for a line without a separator pipe.
func TableCells(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	var cells []string
	start, last := 0, -1 // last is the index of the last separator pipe
	tableSeparators(t, func(i int) bool {
		cells = append(cells, t[start:i])
		start, last = i+1, i
		return len(cells) <= MaxTableColumns+2 // too many columns for a table either way
	})
	if len(cells) > MaxTableColumns+2 {
		return cells, true
	}
	if last < 0 {
		return nil, false
	}
	cells = append(cells, t[start:])
	if t[0] == '|' {
		cells = cells[1:]
	}
	if len(cells) > 1 && last == len(t)-1 {
		cells = cells[:len(cells)-1]
	}
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells, true
}

// tableAlign parses a delimiter row such as "| :--- | :---: | ---: |" into
// one alignment per column: 'l', 'c' or 'r'.
func TableAlign(line string) ([]byte, bool) {
	cells, ok := TableCells(line)
	if !ok {
		return nil, false
	}
	align := make([]byte, len(cells))
	for i, c := range cells {
		dashes := strings.TrimSuffix(strings.TrimPrefix(c, ":"), ":")
		if dashes == "" || strings.Trim(dashes, "-") != "" {
			return nil, false
		}
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			align[i] = 'c'
		case strings.HasSuffix(c, ":"):
			align[i] = 'r'
		default:
			align[i] = 'l'
		}
	}
	return align, true
}
