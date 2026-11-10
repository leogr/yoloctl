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
	Quote  bool // a quote bar precedes every wrapped row
	Decor  bool // a fence frame or table rule: shown, never copied
	Code   bool // fenced code: hard-wrapped inside its block's card
	Fence  FenceFrame
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
	Rule bool
}

// TableInfo is shared by the display lines of one rendered table: what
// bodyRows needs to fit its columns to the view.
type TableInfo struct {
	Align   []byte
	Natural []int // widest cell per column: the aligned layout
	Floor   []int // narrowest a wrapped column may get
	Source  int   // source bytes, for the wrapped layout's budget
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
func parseLines(wm ansi.Method, text string, source bool, theme spaceview.Theme, decorate Decorator, srcs *[]LineSource) []DisplayLine {
	var out []DisplayLine
	fence, fences := "", 0
	lines := strings.Split(Plain(text), "\n")
	opener := -1 // the open fence's display line
	var block strings.Builder
	var lang *syntax // the open fence's highlighter, nil for plain
	var hl hlState
	at := 0      // the source offset of lines[idx]
	literal := 0 // lines of a rejected table: shown as text, never re-detected
	for idx := 0; idx < len(lines); at, idx = at+len(lines[idx])+1, idx+1 {
		line := lines[idx]
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
		}
		st := spaceview.Style{FG: theme.Text}
		trim := strings.TrimLeft(line, " ")
		dl := DisplayLine{}
		ls := LineSource{Start: at, End: at + len(line)}
		var lead *SourceMap // line → lines[idx], when mapping
		if srcs != nil {
			lead = &SourceMap{Hi: len(line)}
		}
		prose := false
		if !source {
			if marker := FenceMarker(trim); marker != "" && (fence == "" || ClosesFence(trim, fence)) {
				if fence == "" {
					fence, fences = marker, fences+1
					dl.Text, dl.Fence = strings.TrimSpace(strings.TrimPrefix(trim, marker)), FenceOpen
					opener = len(out)
					block.Reset()
					lang, hl = syntaxFor(dl.Text), hlState{}
				} else {
					dl.Text, dl.Fence, fence = "", FenceClose, ""
					out[opener].Block = block.String()
				}
				dl.Decor = true
				dl.Pieces = []Piece{{End: len(dl.Text), Style: spaceview.Style{FG: theme.Dim, BG: CodeCard(theme)}}}
				out = append(out, dl)
				if srcs != nil {
					ls.Fence = fences
					*srcs = append(*srcs, ls)
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
			case strings.TrimSpace(line) == "---":
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
					line, st.FG, st.Bold = strings.TrimLeft(trim[n:], " "), HeadingColor(theme, n), true
				}
			case strings.HasPrefix(trim, "> "):
				line, st.FG, prose, dl.Quote = strings.TrimPrefix(trim, "> "), theme.Dim, true, true
			default:
				line, dl.List = listItem(line, lead)
				prose = true
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
		if prose {
			var spans []Span
			if srcs != nil {
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
		} else if dl.Code {
			dl.Pieces, hl = highlightLine(lang, dl.Text, hl, st, theme)
		} else {
			dl.Pieces = []Piece{{End: len(dl.Text), Style: st}}
		}
		if !dl.Code {
			dl.Pieces = decorateTokens(dl.Text, dl.Pieces, theme, decorate, !source)
		}
		out = append(out, dl)
		if srcs != nil {
			m := tabs.Then(lead)
			if inline != nil {
				m = inline.Then(m)
			}
			ls.Src = m.Shift(at)
			*srcs = append(*srcs, ls)
		}
	}
	if fence != "" {
		out[opener].Block = block.String() // an unclosed fence runs to the end
	}
	alignOrdered(out)
	return out
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
func bodyRows(wm ansi.Method, lines []DisplayLine, width int, theme spaceview.Theme, limited *bool) []BodyRow {
	var rows []BodyRow
	cards, pad := CodeCards(wm, lines, width), CardPad(width)
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
		tbl := dl.Table
		if tbl != nil && (n == 0 || lines[n-1].Table != tbl) {
			if tbl.SpaceBefore {
				rows = append(rows, BodyRow{Line: n, Decor: true})
				if len(rows) >= MaxBodyRows {
					return limit(n, 0)
				}
			}
			after := 0
			if tbl.SpaceAfter {
				after = 1 // the spacer after the table needs a row too
			}
			fitted, first = FitTable(wm, lines[n:], n, width, MaxBodyRows-len(rows)-after, theme), n
			rowRule = nil
			if n+1 < len(lines) && lines[n+1].Table == tbl && lines[n+1].Decor {
				rule := lines[n+1].Text
				if fitted != nil {
					rule = fitted[1][0].Runs[0].Text
				}
				rowRule = TableRowRule(wm, rule, width, theme)
			}
		}
		if tbl != nil && rowRule != nil && n > first+2 && dl.Cells != nil {
			rows = append(rows, BodyRow{Line: n, Decor: true, Runs: rowRule})
			if len(rows) >= MaxBodyRows {
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
			if len(rows) >= MaxBodyRows {
				return limit(n, 0)
			}
		default:
			if dl.Quote {
				prefix = []Run{Deco("│ ", spaceview.Style{FG: theme.Frame})}
				textWidth = max(width-2, 1)
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
				if len(rows) >= MaxBodyRows {
					return limit(n, r[1])
				}
			}
		}
		if tbl != nil && tbl.SpaceAfter && (n+1 == len(lines) || lines[n+1].Table != tbl) {
			rows = append(rows, BodyRow{Line: n + 1, Decor: true})
			if len(rows) >= MaxBodyRows {
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
	if total > 2*source+TableSlack {
		return nil, n
	}
	tbl := &TableInfo{Align: align, Natural: widths, Floor: make([]int, len(widths)), Source: source}
	for c, w := range widths {
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
				parts[c] = strings.Repeat("─", w)
			}
			rule := strings.Join(parts, "─┼─")
			out = append(out, DisplayLine{Text: rule, Decor: true, Pieces: []Piece{{End: len(rule), Style: frame}}, Table: tbl})
		}
	}
	return out, n
}

// fitTable lays out the table whose display lines start lines when its
// aligned rows are wider than width: the columns share the view (waterFill)
// and each cell wraps inside its column, so separators stay in place and the
// rule matches. first is the index of lines[0] in the message. It returns
// the rows of each of the table's lines, or nil to render the table as text
// as before: when it fits, when its columns' floors do not, or when the
// layout would exceed the table's budget (twice its source plus tableSlack
// bytes) or the rows left. Everything is counted before a row is built. A
// row's fragments keep their exact bytes, hanging spaces included, so copy,
// selection and the reading position all see the same logical line.
func FitTable(wm ansi.Method, lines []DisplayLine, first, width, rowsLeft int, theme spaceview.Theme) [][]BodyRow {
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
	if natural <= avail {
		return nil
	}
	alloc, ok := WaterFill(tbl.Natural, tbl.Floor, avail)
	if !ok {
		return nil
	}
	budget := 2*tbl.Source + TableSlack
	used := len("─┼─") * (cols - 1)
	for _, w := range alloc {
		used += w * len("─")
	}
	frags := make([][][][2]int, len(lines)) // per line, cell and row
	heights := make([]int, len(lines))
	visual := max(len(lines)-3, 0) // one divider between consecutive body rows
	for i, dl := range lines {
		heights[i] = 1
		if dl.Cells != nil {
			frags[i] = make([][][2]int, cols)
			for c, cr := range dl.Cells {
				if cr[0] == cr[1] {
					continue
				}
				rs := WrapRanges(wm, dl.Text[cr[0]:cr[1]], alloc[c], true)
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
					if shown > alloc[c] {
						return nil // never left to paint's truncation
					}
					left, right := CellGaps(tbl.Align[c], alloc[c]-shown, c == cols-1)
					used += e - f[0] + left + right
				}
				if used > budget {
					return nil
				}
			}
		}
		if visual += heights[i]; visual >= rowsLeft {
			return nil
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
				left, right := CellGaps(tbl.Align[c], alloc[c]-wm.StringWidth(dl.Text[f[0]:e]), c == cols-1)
				if left > 0 {
					row.Runs = append(row.Runs, Deco(strings.Repeat(" ", left), plainStyle))
				}
				for _, p := range dl.Pieces {
					if s, pe := max(p.Start, f[0]), min(p.End, e); s < pe {
						row.Runs = append(row.Runs, Run{Text: dl.Text[s:pe], Style: p.Style, Copy: true, Start: s, Act: p.Act, Strike: p.Strike})
					}
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
	return out
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
