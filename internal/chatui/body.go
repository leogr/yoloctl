package chatui

import (
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// piece styles display text [start, end) of one line.
type piece struct {
	start, end int
	style      spaceview.Style
	code       bool // inline code: never re-styled as a mention or URL
	strike     bool
	act        action
}

// displayLine is one logical line as shown: Markdown markers removed,
// bullets and tabs expanded. Its text is exactly what a drag selection
// copies; decorations (quote bars, fence frames) live outside it.
type displayLine struct {
	text   string
	pieces []piece
	quote  bool // a quote bar precedes every wrapped row
	decor  bool // a fence frame or table rule: shown, never copied
	code   bool // fenced code: hard-wrapped inside its block's card
	fence  fenceFrame
	// block is a fence opener's code, its source lines as its copy button
	// copies them ("" for an empty block, which shows no button).
	block string
	// table is set on the rows and the rule of a rendered table; cells are
	// the byte ranges of a row's cell texts in text (nil on the rule).
	table *tableInfo
	cells [][2]int
	// subs are the references shown as previews (see substitute).
	subs []textSub
}

// tableInfo is shared by the display lines of one rendered table: what
// bodyRows needs to fit its columns to the view.
type tableInfo struct {
	align   []byte
	natural []int // widest cell per column: the aligned layout
	floor   []int // narrowest a wrapped column may get
	source  int   // source bytes, for the wrapped layout's budget
	// spaceBefore and spaceAfter ask bodyRows for an empty row between the
	// table and a neighbouring source line that is not blank. They are
	// display rows only: no logical line, source map or copied byte.
	spaceBefore, spaceAfter bool
}

// fenceFrame marks the decor line of a fence: the opener shows the info
// string (the language) as the card's label row, the closer its bottom
// padding row.
type fenceFrame int

const (
	fenceNone fenceFrame = iota
	fenceOpen
	fenceClose
)

// copyText returns the copyable bytes of text[lo:hi]: exactly what is
// shown, except that a preview it covers whole is its reference token.
func (dl displayLine) copyText(lo, hi int) string {
	var b strings.Builder
	at := lo
	for _, s := range dl.subs {
		if s.start >= lo && s.end <= hi {
			b.WriteString(dl.text[at:s.start])
			b.WriteString(s.copy)
			at = s.end
		}
	}
	b.WriteString(dl.text[at:hi])
	return b.String()
}

// bodyRow is one wrapped row of a display line. A row of a wrapped table
// shows cell fragments that are not contiguous in the line: frags lists
// them, and [start, end) only bounds them.
type bodyRow struct {
	line, start, end int
	runs             []run
	decor            bool
	frags            [][2]int
}

// codeCard is a fenced block's background: a light raised tint over the
// block's own width rather than a full-width slab.
func codeCard(t spaceview.Theme) spaceview.Color { return t.Popup }

// maxBodyRows bounds the rows one message can occupy; the original text
// stays available to explicit copy.
const maxBodyRows = 16384

// displayLines implements a deliberately small Markdown display subset:
// headings, quotes, unordered lists, tables, fenced code and inline code,
// strong, emphasis, strikethrough and links (whose URL stays visible). Fenced code and source mode
// are literal. Mentions and bare URLs become click targets; mentions of me
// (any of my case-folded tokens, see selfMention) stand out. Remote ANSI is
// inert.
func displayLines(wm ansi.Method, text string, source bool, theme spaceview.Theme, me string) []displayLine {
	return displayLinesMap(wm, text, source, theme, me, nil)
}

// displayLinesMap is displayLines that, given srcs, also records where each
// display line comes from in plain(text), one lineSrc per display line
// (see quoteSource). The display is the same either way.
func displayLinesMap(wm ansi.Method, text string, source bool, theme spaceview.Theme, me string, srcs *[]lineSrc) []displayLine {
	var out []displayLine
	fence, fences := "", 0
	lines := strings.Split(plain(text), "\n")
	opener := -1 // the open fence's display line
	var block strings.Builder
	var lang *syntax // the open fence's highlighter, nil for plain
	var hl hlState
	at := 0      // the source offset of lines[idx]
	literal := 0 // lines of a rejected table: shown as text, never re-detected
	for idx := 0; idx < len(lines); at, idx = at+len(lines[idx])+1, idx+1 {
		line := lines[idx]
		// A fence opener always wins over a table, even with pipes in it.
		if !source && fence == "" && idx >= literal && fenceMarker(strings.TrimLeft(line, " ")) == "" {
			table, n := tableLines(wm, lines[idx:], theme, me)
			if table != nil {
				// A blank or whitespace-only neighbour already separates the
				// table; the message's own edges need nothing.
				table[0].table.spaceBefore = idx > 0 && strings.TrimSpace(lines[idx-1]) != ""
				table[0].table.spaceAfter = idx+n < len(lines) && strings.TrimSpace(lines[idx+n]) != ""
				out = append(out, table...)
				if srcs != nil {
					// A table's display lines are its source lines, rebuilt:
					// header, rule (the delimiter row), then each row.
					for k := range n {
						*srcs = append(*srcs, lineSrc{start: at, end: at + len(lines[idx+k])})
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
		dl := displayLine{}
		ls := lineSrc{start: at, end: at + len(line)}
		var lead *srcMap // line → lines[idx], when mapping
		if srcs != nil {
			lead = &srcMap{hi: len(line)}
		}
		prose := false
		if !source {
			if marker := fenceMarker(trim); marker != "" && (fence == "" || closesFence(trim, fence)) {
				if fence == "" {
					fence, fences = marker, fences+1
					dl.text, dl.fence = strings.TrimSpace(strings.TrimPrefix(trim, marker)), fenceOpen
					opener = len(out)
					block.Reset()
					lang, hl = syntaxFor(dl.text), hlState{}
				} else {
					dl.text, dl.fence, fence = "", fenceClose, ""
					out[opener].block = block.String()
				}
				dl.decor = true
				dl.pieces = []piece{{end: len(dl.text), style: spaceview.Style{FG: theme.Dim, BG: codeCard(theme)}}}
				out = append(out, dl)
				if srcs != nil {
					ls.fence = fences
					*srcs = append(*srcs, ls)
				}
				continue
			}
			switch {
			case fence != "":
				dl.code = true
				block.WriteString(line)
				if idx < len(lines)-1 {
					block.WriteByte('\n')
				}
				st = spaceview.Style{FG: theme.Text, BG: codeCard(theme)}
				ls.fence = fences
			case strings.HasPrefix(trim, "#"):
				prose = true
				if n := len(trim) - len(strings.TrimLeft(trim, "#")); n <= 6 && strings.HasPrefix(trim[n:], " ") {
					line, st.FG, st.Bold = strings.TrimLeft(trim[n:], " "), theme.Brand, true
				}
			case strings.HasPrefix(trim, "> "):
				line, st.FG, prose, dl.quote = strings.TrimPrefix(trim, "> "), theme.Dim, true, true
			default:
				line, prose = listItem(line, lead), true
			}
		}
		if lead != nil && len(lead.at) == 0 {
			// line is what remains of lines[idx] after a marker the view drops.
			lead.add(line, lines[idx], len(lines[idx])-len(line), len(lines[idx]))
		}
		var tabs, inline *srcMap
		if srcs != nil {
			tabs = &srcMap{hi: len(line)}
		}
		dl.text = expandTabsMap(wm, line, tabs)
		if prose {
			var spans []span
			if srcs != nil {
				inline = &srcMap{hi: len(dl.text)}
			}
			dl.text, spans = inlineMarkdownMap(dl.text, st, theme, inline)
			for _, sp := range spans {
				p := piece{start: sp.start, end: sp.end, style: sp.style, code: sp.code, strike: sp.strike}
				if sp.link != "" {
					p.act = action{kind: actLink, value: sp.link}
				}
				dl.pieces = append(dl.pieces, p)
			}
		} else if dl.code {
			dl.pieces, hl = highlightLine(lang, dl.text, hl, st, theme)
		} else {
			dl.pieces = []piece{{end: len(dl.text), style: st}}
		}
		if !dl.code {
			dl.pieces = decorateTokens(dl.text, dl.pieces, theme, me, !source)
		}
		out = append(out, dl)
		if srcs != nil {
			m := tabs.then(lead)
			if inline != nil {
				m = inline.then(m)
			}
			ls.src = m.shift(at)
			*srcs = append(*srcs, ls)
		}
	}
	if fence != "" {
		out[opener].block = block.String() // an unclosed fence runs to the end
	}
	return out
}

// decorateTokens marks bare http(s) URLs and, in prose (outside source
// mode), @mentions and message references. An @ followed only by digits is
// neither: it stays plain text.
func decorateTokens(text string, pieces []piece, theme spaceview.Theme, me string, prose bool) []piece {
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "https://") || strings.HasPrefix(text[i:], "http://"):
			end := i + strings.IndexAny(text[i:]+" ", " \t")
			link := strings.TrimRight(text[i:end], ").,;:]>\"'")
			pieces = overlay(pieces, i, i+len(link), func(p piece) piece {
				p.style.FG, p.style.Underline = theme.Label, true
				p.act = action{kind: actLink, value: link}
				return p
			})
			i = end
			continue
		case prose && strings.HasPrefix(text[i:], refPrefix) && (i == 0 || !nickByte(text[i-1])):
			// base64url's alphabet is the nickname bytes: a longer run, or
			// padding after it, is not a reference with a valid prefix.
			end := i + refLen
			if end <= len(text) && (end == len(text) || !nickByte(text[end]) && text[end] != '=') && !linked(pieces, i, end) {
				if id, ok := refID(text[i:end]); ok {
					pieces = overlay(pieces, i, end, func(p piece) piece {
						p.style.FG, p.style.Underline = theme.Label, true
						p.act = action{kind: actRef, value: id}
						return p
					})
					i = end
					continue
				}
			}
		case prose && strings.HasPrefix(text[i:], msgidPrefix) && (i == 0 || !nickByte(text[i-1])):
			end := i + len(msgidPrefix) + len("00000000-0000-0000-0000-000000000000")
			if end <= len(text) && messageID(text[i+len(msgidPrefix):end]) && (end == len(text) || !nickByte(text[end])) && !linked(pieces, i, end) {
				id := text[i+len(msgidPrefix) : end]
				pieces = overlay(pieces, i, end, func(p piece) piece {
					p.style.FG, p.style.Underline = theme.Label, true
					p.act = action{kind: actRef, value: id}
					return p
				})
				i = end
				continue
			}
		case prose && text[i] == '@' && (i == 0 || !nickByte(text[i-1])):
			end := i + 1
			for end < len(text) && nickByte(text[end]) {
				end++
			}
			switch {
			case end == i+1:
			case strings.Trim(text[i+1:end], "0123456789") == "":
				// A nickname starts with a letter or _; nothing resolves a
				// number such as @289 written in older messages.
			default:
				nick := text[i+1 : end]
				self := selfMention(me, nick)
				pieces = overlay(pieces, i, end, func(p piece) piece {
					p.style.Bold = true
					p.style.FG = theme.Label
					if self {
						p.style.FG, p.style.BG = mentionColors(theme)
					}
					p.act = action{kind: actMention, value: nick}
					return p
				})
			}
			i = end
			continue
		}
		i++
	}
	return pieces
}

// refPrefix starts a shared message reference: m: and the unpadded base64url
// encoding of the server ID's 16 bytes in display order, the spelling AIRC
// prints for every client in the room. Copy reference writes one; a click
// shows the message when it is loaded in the same channel.
const refPrefix = "m:"

// refLen is the length of a reference: 128 bits take 22 base64url characters.
const refLen = len(refPrefix) + 22

// msgidPrefix and the full server ID are the same reference, longer.
const msgidPrefix = "msgid:"

// compactRef returns the shared reference of a server message ID.
func compactRef(id string) (string, bool) {
	var raw [16]byte
	if !messageID(id) {
		return "", false
	}
	if _, err := hex.Decode(raw[:], []byte(strings.ReplaceAll(id, "-", ""))); err != nil {
		return "", false
	}
	return refPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), true
}

// refID returns the server message ID a shared reference names. Only exactly
// 22 base64url characters whose decoding re-encodes to the same text qualify:
// padding, other alphabets and nonzero unused bits are not references.
func refID(ref string) (string, bool) {
	encoded, ok := strings.CutPrefix(ref, refPrefix)
	if !ok || len(ref) != refLen {
		return "", false
	}
	for i := 0; i < len(encoded); i++ {
		if !nickByte(encoded[i]) {
			return "", false
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return "", false
	}
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
}

// messageID reports whether id has the lowercase canonical UUID form of the
// room server's message IDs. Partial or other forms are never references.
func messageID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// linked reports whether a Markdown link already targets part of
// [start, end): a reference never replaces its destination.
func linked(pieces []piece, start, end int) bool {
	for _, p := range pieces {
		if p.act.kind == actLink && p.start < end && p.end > start {
			return true
		}
	}
	return false
}

func nickByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// eachMention calls f with the token of each explicit @mention in text, in
// order, until f returns false: an @ that does not follow a nickname byte,
// then the nickname bytes after it.
func eachMention(text string, f func(token string) bool) {
	for i := strings.IndexByte(text, '@'); i >= 0; {
		end := i + 1
		for end < len(text) && nickByte(text[end]) {
			end++
		}
		if (i == 0 || !nickByte(text[i-1])) && end > i+1 && !f(text[i+1:end]) {
			return
		}
		next := strings.IndexByte(text[end:], '@')
		if next < 0 {
			return
		}
		i = end + next
	}
}

// mentions reports whether text addresses one of me (see selfMention) with an
// explicit @mention.
func mentions(text, me string) bool {
	found := false
	eachMention(text, func(token string) bool {
		found = selfMention(me, token)
		return !found
	})
	return found
}

// selfMention reports whether token is one of me: the local user's mention
// tokens, NUL-separated (see selfNames); a plain nick is a list of one.
func selfMention(me, token string) bool {
	for name := range strings.SplitSeq(me, "\x00") {
		if name != "" && strings.EqualFold(name, token) {
			return true
		}
	}
	return false
}

// overlay applies f to the parts of pieces inside [start, end), splitting
// pieces at the boundaries. Ranges touching inline code stay unchanged.
func overlay(pieces []piece, start, end int, f func(piece) piece) []piece {
	for _, p := range pieces {
		if p.code && p.start < end && p.end > start {
			return pieces
		}
	}
	var out []piece
	for _, p := range pieces {
		if p.end <= start || p.start >= end {
			out = append(out, p)
			continue
		}
		if p.start < start {
			left := p
			left.end = start
			out = append(out, left)
		}
		mid := p
		mid.start, mid.end = max(p.start, start), min(p.end, end)
		out = append(out, f(mid))
		if p.end > end {
			right := p
			right.start = end
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
func bodyRows(wm ansi.Method, lines []displayLine, width int, theme spaceview.Theme) []bodyRow {
	var rows []bodyRow
	cards, pad := codeCards(wm, lines, width), cardPad(width)
	var fitted [][]bodyRow // the current table's rows, nil when it renders as text
	var rowRule []run      // shared decorative divider between logical body rows
	first := 0
	limit := func(n, at int) []bodyRow {
		return append(rows, bodyRow{line: n, start: at, end: at, decor: true, runs: []run{deco("[display row limit; Copy message retains the entire original]", spaceview.Style{FG: theme.Bad})}})
	}
	for n, dl := range lines {
		tbl := dl.table
		if tbl != nil && (n == 0 || lines[n-1].table != tbl) {
			if tbl.spaceBefore {
				rows = append(rows, bodyRow{line: n, decor: true})
				if len(rows) >= maxBodyRows {
					return limit(n, 0)
				}
			}
			after := 0
			if tbl.spaceAfter {
				after = 1 // the spacer after the table needs a row too
			}
			fitted, first = fitTable(wm, lines[n:], n, width, maxBodyRows-len(rows)-after, theme), n
			rowRule = nil
			if n+1 < len(lines) && lines[n+1].table == tbl && lines[n+1].decor {
				rule := lines[n+1].text
				if fitted != nil {
					rule = fitted[1][0].runs[0].text
				}
				rowRule = tableRowRule(wm, rule, width, theme)
			}
		}
		if tbl != nil && rowRule != nil && n > first+2 && dl.cells != nil {
			rows = append(rows, bodyRow{line: n, decor: true, runs: rowRule})
			if len(rows) >= maxBodyRows {
				return limit(n, 0)
			}
		}
		var prefix, suffix []run
		textWidth := width
		if cards[n] > 0 {
			// Every row of a fenced block, frames included, fills its card.
			bg := spaceview.Style{BG: codeCard(theme)}
			textWidth = max(cards[n]-2*pad, 1)
			prefix = []run{deco(strings.Repeat(" ", pad), bg)}
			suffix = []run{deco("", bg)}
		}
		switch {
		case tbl != nil && fitted != nil:
			rows = append(rows, fitted[n-first]...)
		case dl.decor:
			text := dl.text
			if dl.fence != fenceNone {
				text = truncate(wm, text, textWidth, "…")
			}
			runs := append(slicesCloneRuns(prefix), deco(text, dl.pieces[0].style))
			if button := copyButtonRuns(wm, dl, textWidth, theme); button != nil {
				runs = append(slicesCloneRuns(prefix), button...)
			}
			runs = append(runs, suffix...)
			rows = append(rows, bodyRow{line: n, runs: padCard(wm, runs, cards[n]), decor: true})
			if len(rows) >= maxBodyRows {
				return limit(n, 0)
			}
		default:
			if dl.quote {
				prefix = []run{deco("│ ", spaceview.Style{FG: theme.Frame})}
				textWidth = max(width-2, 1)
			}
			for _, r := range wrapRanges(wm, dl.text, textWidth, !dl.code) {
				runs := slicesCloneRuns(prefix)
				for _, p := range dl.pieces {
					s, e := max(p.start, r[0]), min(p.end, r[1])
					if s < e {
						runs = append(runs, run{text: dl.text[s:e], style: p.style, copy: true, start: s, act: p.act, strike: p.strike})
					}
				}
				runs = padCard(wm, append(runs, suffix...), cards[n])
				rows = append(rows, bodyRow{line: n, start: r[0], end: r[1], runs: runs})
				if len(rows) >= maxBodyRows {
					return limit(n, r[1])
				}
			}
		}
		if tbl != nil && tbl.spaceAfter && (n+1 == len(lines) || lines[n+1].table != tbl) {
			rows = append(rows, bodyRow{line: n + 1, decor: true})
			if len(rows) >= maxBodyRows {
				return limit(n+1, 0)
			}
		}
	}
	return rows
}

// codeCards sizes each fenced block, from its opener through its closer, to
// its widest code line or label plus one cell of padding on each side,
// within width. Other lines get 0.
func codeCards(wm ansi.Method, lines []displayLine, width int) []int {
	cards := make([]int, len(lines))
	pad := cardPad(width)
	for n := 0; n < len(lines); {
		if lines[n].fence != fenceOpen {
			n++
			continue
		}
		end, inner := n+1, wm.StringWidth(lines[n].text)
		if lines[n].block != "" {
			inner += copyGap(lines[n].text) + wm.StringWidth(copyLabel)
		}
		for end < len(lines) && lines[end].code {
			inner = max(inner, wm.StringWidth(lines[end].text))
			end++
		}
		if end < len(lines) && lines[end].fence == fenceClose {
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
const copyLabel = "copy"

// copyGap separates a block's language label from its copy button.
func copyGap(label string) int {
	if label == "" {
		return 0
	}
	return 2
}

// copyButtonRuns renders a fence opener with code as its label, truncated
// first, and the copy button flush right within textWidth. It is nil when
// the block is empty or the card is too narrow for the button: the row then
// shows the label alone and Alt+C still copies the first block.
func copyButtonRuns(wm ansi.Method, dl displayLine, textWidth int, theme spaceview.Theme) []run {
	button := wm.StringWidth(copyLabel)
	if dl.fence != fenceOpen || dl.block == "" || textWidth < button {
		return nil
	}
	label, room := "", textWidth-button-copyGap(dl.text)
	if room > 0 {
		label = truncate(wm, dl.text, room, "…")
	}
	bg := spaceview.Style{BG: codeCard(theme)}
	return []run{
		deco(label, dl.pieces[0].style),
		deco(strings.Repeat(" ", textWidth-button-wm.StringWidth(label)), bg),
		{text: copyLabel, style: spaceview.Style{FG: theme.Label, BG: codeCard(theme)}, act: action{kind: actCopyCode, value: dl.block}},
	}
}

// cardPad is a card's padding on each side: one cell, none on views too
// narrow to spare it.
func cardPad(width int) int { return min(width/3, 1) }

// padCard widens a code row's last run, its card padding, so the row
// fills its card exactly; other rows are unchanged.
func padCard(wm ansi.Method, runs []run, card int) []run {
	if gap := card - runsWidth(wm, runs); card > 0 && gap > 0 {
		runs[len(runs)-1].text += strings.Repeat(" ", gap)
	}
	return runs
}

// mentionColors highlight a mention of the local user like a marker pen,
// distinct from error colors in both themes.
func mentionColors(t spaceview.Theme) (fg, bg spaceview.Color) {
	if t.Dark {
		return spaceview.PaletteColor(230), spaceview.PaletteColor(94)
	}
	return spaceview.PaletteColor(94), spaceview.PaletteColor(229)
}

// Table bounds. Padding columns to their widest cell can multiply a skewed
// table's size, so a table over these bounds, or whose aligned rendering
// would exceed twice its source plus tableSlack bytes, stays literal text.
// The same budget bounds a table wrapped to the view (see fitTable).
const (
	maxTableColumns = 32
	maxTableRows    = 512
	tableSlack      = 4 << 10
	tableFloor      = 4 // cells a wrapped column keeps, if it is that wide
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
func tableLines(wm ansi.Method, lines []string, theme spaceview.Theme, me string) (out []displayLine, n int) {
	if len(lines) < 2 {
		return nil, 0
	}
	header, ok := tableCells(lines[0])
	align, delim := tableAlign(lines[1])
	if !ok || !delim || len(align) != len(header) {
		return nil, 0
	}
	n = 2
	for n < len(lines) && tableRow(lines[n]) {
		n++
	}
	if len(header) > maxTableColumns || n-2 > maxTableRows {
		return nil, n
	}
	rows := [][]string{header}
	source := len(lines[0]) + len(lines[1]) + 2
	for _, line := range lines[2:n] {
		cells, _ := tableCells(line)
		cells = append(cells, make([]string, max(len(header)-len(cells), 0))...)
		rows, source = append(rows, cells[:len(header)]), source+len(line)+1
	}
	type cell struct {
		text  string
		spans []span
	}
	shown := make([][]cell, len(rows))
	widths := make([]int, len(header))
	for r, cells := range rows {
		st := spaceview.Style{FG: theme.Text, Bold: r == 0}
		for c, raw := range cells {
			text, spans := inlineMarkdown(expandTabs(wm, raw), st, theme)
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
	if total > 2*source+tableSlack {
		return nil, n
	}
	tbl := &tableInfo{align: align, natural: widths, floor: make([]int, len(widths)), source: source}
	for c, w := range widths {
		// A wrapped column keeps a few cells, and always its widest
		// grapheme, which cannot break.
		tbl.floor[c] = min(w, tableFloor)
		if w > tableFloor {
			for _, cells := range shown {
				tbl.floor[c] = max(tbl.floor[c], widestGrapheme(wm, cells[c].text))
			}
		}
	}
	frame := spaceview.Style{FG: theme.Frame}
	for r, cells := range shown {
		dl := displayLine{table: tbl, cells: make([][2]int, len(cells))}
		var b strings.Builder
		add := func(text string, sp span) {
			start := b.Len()
			b.WriteString(text)
			if text != "" {
				dl.pieces = append(dl.pieces, piece{start: start + sp.start, end: start + sp.end, style: sp.style, code: sp.code, strike: sp.strike})
			}
		}
		for c, cl := range cells {
			if c > 0 {
				add(" │ ", span{end: len(" │ "), style: frame})
			}
			gap := widths[c] - wm.StringWidth(cl.text)
			left := 0
			switch align[c] {
			case 'r':
				left = gap
			case 'c':
				left = gap / 2
			}
			add(strings.Repeat(" ", left), span{end: left, style: spaceview.Style{FG: theme.Text}})
			start := b.Len()
			b.WriteString(cl.text)
			dl.cells[c] = [2]int{start, b.Len()}
			for _, sp := range cl.spans {
				p := piece{start: start + sp.start, end: start + sp.end, style: sp.style, code: sp.code, strike: sp.strike}
				if sp.link != "" {
					p.act = action{kind: actLink, value: sp.link}
				}
				dl.pieces = append(dl.pieces, p)
			}
			add(strings.Repeat(" ", gap-left), span{end: gap - left, style: spaceview.Style{FG: theme.Text}})
		}
		dl.text = strings.TrimRight(b.String(), " ")
		for c, cr := range dl.cells {
			// Trimming can only shorten the last cell's own trailing spaces.
			end := min(cr[1], len(dl.text))
			dl.cells[c] = [2]int{min(cr[0], end), end}
		}
		var kept []piece
		for _, p := range dl.pieces {
			if p.start < len(dl.text) {
				p.end = min(p.end, len(dl.text))
				kept = append(kept, p)
			}
		}
		dl.pieces = decorateTokens(dl.text, kept, theme, me, true)
		out = append(out, dl)
		if r == 0 {
			parts := make([]string, len(widths))
			for c, w := range widths {
				parts[c] = strings.Repeat("─", w)
			}
			rule := strings.Join(parts, "─┼─")
			out = append(out, displayLine{text: rule, decor: true, pieces: []piece{{end: len(rule), style: frame}}, table: tbl})
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
func fitTable(wm ansi.Method, lines []displayLine, first, width, rowsLeft int, theme spaceview.Theme) [][]bodyRow {
	tbl := lines[0].table
	k := 1
	for k < len(lines) && lines[k].table == tbl {
		k++
	}
	lines = lines[:k]
	cols := len(tbl.natural)
	avail := width - 3*(cols-1) // a separator takes three cells
	natural := 0
	for _, w := range tbl.natural {
		natural += w
	}
	if natural <= avail {
		return nil
	}
	alloc, ok := waterFill(tbl.natural, tbl.floor, avail)
	if !ok {
		return nil
	}
	budget := 2*tbl.source + tableSlack
	used := len("─┼─") * (cols - 1)
	for _, w := range alloc {
		used += w * len("─")
	}
	frags := make([][][][2]int, len(lines)) // per line, cell and row
	heights := make([]int, len(lines))
	visual := max(len(lines)-3, 0) // one divider between consecutive body rows
	for i, dl := range lines {
		heights[i] = 1
		if dl.cells != nil {
			frags[i] = make([][][2]int, cols)
			for c, cr := range dl.cells {
				if cr[0] == cr[1] {
					continue
				}
				rs := wrapRanges(wm, dl.text[cr[0]:cr[1]], alloc[c], true)
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
					e := drawnEnd(wm, dl.text, f, alloc[c])
					shown := wm.StringWidth(dl.text[f[0]:e])
					if shown > alloc[c] {
						return nil // never left to paint's truncation
					}
					left, right := cellGaps(tbl.align[c], alloc[c]-shown, c == cols-1)
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
	out := make([][]bodyRow, len(lines))
	for i, dl := range lines {
		if dl.cells == nil {
			parts := make([]string, cols)
			for c, w := range alloc {
				parts[c] = strings.Repeat("─", w)
			}
			out[i] = []bodyRow{{line: first + i, runs: []run{deco(strings.Join(parts, "─┼─"), frame)}, decor: true}}
			continue
		}
		for v := range heights[i] {
			row := bodyRow{line: first + i, start: len(dl.text)}
			for c := range cols {
				if c > 0 {
					row.runs = append(row.runs, deco(" │ ", frame))
				}
				if v >= len(frags[i][c]) {
					if c < cols-1 {
						row.runs = append(row.runs, deco(strings.Repeat(" ", alloc[c]), plainStyle))
					}
					continue
				}
				f := frags[i][c][v]
				e := drawnEnd(wm, dl.text, f, alloc[c])
				left, right := cellGaps(tbl.align[c], alloc[c]-wm.StringWidth(dl.text[f[0]:e]), c == cols-1)
				if left > 0 {
					row.runs = append(row.runs, deco(strings.Repeat(" ", left), plainStyle))
				}
				for _, p := range dl.pieces {
					if s, pe := max(p.start, f[0]), min(p.end, e); s < pe {
						row.runs = append(row.runs, run{text: dl.text[s:pe], style: p.style, copy: true, start: s, act: p.act, strike: p.strike})
					}
				}
				if right > 0 {
					row.runs = append(row.runs, deco(strings.Repeat(" ", right), plainStyle))
				}
				row.frags = append(row.frags, f)
				row.start, row.end = min(row.start, f[0]), max(row.end, f[1])
			}
			if row.frags == nil { // a row of empty cells
				row.start, row.end = 0, len(dl.text)
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
func waterFill(natural, floor []int, avail int) (alloc []int, ok bool) {
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
func cellGaps(align byte, gap int, last bool) (before, after int) {
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
func drawnEnd(wm ansi.Method, text string, f [2]int, width int) int {
	e := f[1]
	for e > f[0] && text[e-1] == ' ' && wm.StringWidth(text[f[0]:e]) > width {
		e--
	}
	return e
}

// widestGrapheme returns the width of the widest grapheme cluster in s.
func widestGrapheme(wm ansi.Method, s string) int {
	widest := 0
	for o := 0; o < len(s); {
		g, w := ansi.FirstGraphemeCluster(s[o:], wm)
		widest, o = max(widest, w), o+len(g)
	}
	return widest
}

// tableRow reports whether a line continues a table: it has an unescaped
// pipe and does not start another block (a fence, heading, quote or list
// item), as in GFM. It allocates nothing.
func tableRow(line string) bool {
	trim := strings.TrimLeft(line, " ")
	bullet := len(trim) >= 2 && strings.IndexByte("-*+", trim[0]) >= 0 && trim[1] == ' '
	if fenceMarker(trim) != "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ">") || bullet || orderedItem(trim) {
		return false
	}
	for i := 0; i < len(trim); i++ {
		switch trim[i] {
		case '\\':
			i++
		case '|':
			return true
		}
	}
	return false
}

// orderedItem reports whether a line starts an ordered list item ("1. ").
func orderedItem(trim string) bool {
	digits := len(trim) - len(strings.TrimLeft(trim, "0123456789"))
	return digits > 0 && digits < 10 && (strings.HasPrefix(trim[digits:], ". ") || strings.HasPrefix(trim[digits:], ") "))
}

// tableCells splits a table row on pipes that are not escaped, dropping one
// leading and one trailing pipe. One scan decides every pipe, so an escaped
// backslash before a pipe (\\|) leaves that pipe a separator. ok is false for
// a line without an unescaped pipe.
func tableCells(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	var cells []string
	start, last := 0, -1 // last is the index of the last unescaped pipe
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == '\\' && i+1 < len(t):
			i++ // an escaped character stays in its cell; inline Markdown unescapes it
		case t[i] == '|':
			cells = append(cells, t[start:i])
			start, last = i+1, i
			if len(cells) > maxTableColumns+2 {
				return cells, true // too many columns for a table either way
			}
		}
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
func tableAlign(line string) ([]byte, bool) {
	cells, ok := tableCells(line)
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
