package chatui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// draftRow is one wrapped composer row: its draft bytes [start, end), the
// cells it shows as text and chip parts, and the cursor stops on it with
// their columns, shared by rendering, vertical movement and clicks.
type draftRow struct {
	text       string
	start, end int
	parts      []draftPart
	stops      []draftStop
}

// draftPart is a run of draft text, or a collapsed paste's chip when fold is
// a fold index (otherwise -1). col is its first cell.
type draftPart struct {
	text      string
	fold, col int
}

type draftStop struct{ offset, col int }

// stopAt is the last cursor stop on the row at or before cell col.
func (r draftRow) stopAt(col int) int {
	at := r.start
	for _, s := range r.stops {
		if s.col > col {
			break
		}
		at = s.offset
	}
	return at
}

// chipLabel is a collapsed paste's or an image's chip, shortened to fit
// width cells.
func chipLabel(wm ansi.Method, f fold, text string, width int) string {
	if f.img != nil {
		return fitLabel(wm, []string{imageLabel(f.n), "[…]"}, width)
	}
	body := text[f.start:f.end]
	lines := strings.Count(body, "\n")
	if !strings.HasSuffix(body, "\n") {
		lines++
	}
	unit := "lines"
	if lines == 1 {
		unit = "line"
	}
	labels := []string{
		fmt.Sprintf("[Pasted #%d · %d %s · %s]", f.n, lines, unit, byteCount(len(body))),
		fmt.Sprintf("[Pasted #%d · %d %s]", f.n, lines, unit),
		fmt.Sprintf("[Pasted #%d]", f.n),
		"[…]",
	}
	return fitLabel(wm, labels, width)
}

// fitLabel is the first label that fits width cells.
func fitLabel(wm ansi.Method, labels []string, width int) string {
	for _, label := range labels {
		if wm.StringWidth(label) <= width {
			return label
		}
	}
	return "…"
}

type viewAnchor struct {
	key          string
	line, offset int
}

// cachedBody keeps a message's display lines (dependent on the width method) and
// their rows at the last width.
type cachedBody struct {
	text, me string
	source   bool
	// base is the lines before reference previews; keys are the previews
	// lines shows (see previewKeys).
	base  []displayLine
	keys  []string
	lines []displayLine
	width int
	rows  []bodyRow
}

func messageKey(msg Message) string {
	if msg.ClientID != "" {
		return "client:" + msg.ClientID
	}
	return "server:" + msg.ID
}

func plain(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// oneLine neutralizes text for single-row chrome: names, notices, labels.
func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(plain(s))
}

func expandTabs(wm ansi.Method, s string) string { return expandTabsMap(wm, s, nil) }

// expandTabsMap is expandTabs that, given src, records each tab's spaces as
// shown in place of it.
func expandTabsMap(wm ansi.Method, s string, src *srcMap) string {
	var b strings.Builder
	x, at := 0, 0
	for at < len(s) {
		g, w := ansi.FirstGraphemeCluster(s[at:], wm)
		in := g
		if g == "\t" {
			w = 4 - x%4
			g = strings.Repeat(" ", w)
		}
		src.add(g, s, at, at+len(in))
		b.WriteString(g)
		at += len(in)
		x += w
	}
	return b.String()
}

func displayWidth(wm ansi.Method, s string) int { return wm.StringWidth(expandTabs(wm, s)) }

// draftLayout wraps the draft into composer rows of width cells. A fold is
// one unbreakable chip that moves to the next row rather than splitting. It
// returns the rows and the cursor's row and column.
func draftLayout(wm ansi.Method, text string, folds []fold, cursor, width int) ([]draftRow, int, int) {
	width = max(width, 1)
	var rows []draftRow
	var b strings.Builder
	var row draftRow
	start, offset, col := 0, 0, 0
	part := func(text string, fold int) {
		if n := len(row.parts); fold < 0 && n > 0 && row.parts[n-1].fold < 0 {
			row.parts[n-1].text += text
		} else {
			row.parts = append(row.parts, draftPart{text: text, fold: fold, col: col})
		}
		b.WriteString(text)
	}
	flush := func(end int) {
		row.text, row.start, row.end = b.String(), start, end
		row.stops = append(row.stops, draftStop{offset: end, col: col})
		rows = append(rows, row)
		b.Reset()
		row, col = draftRow{}, 0
	}
	next := 0 // index of the next fold
	for offset < len(text) {
		for next < len(folds) && folds[next].end <= offset {
			next++
		}
		// A fold whose start fell inside a grapheme still shows as a chip
		// from the first boundary in it.
		if next < len(folds) && folds[next].start <= offset {
			f := folds[next]
			label := chipLabel(wm, f, text, width)
			w := wm.StringWidth(label)
			if col+w > width && col > 0 {
				flush(offset)
				start = offset
			}
			row.stops = append(row.stops, draftStop{offset: offset, col: col})
			part(label, next)
			col += w
			offset, next = f.end, next+1
			continue
		}
		g, w := ansi.FirstGraphemeCluster(text[offset:], wm)
		if g == "\n" {
			flush(offset)
			offset += len(g)
			start = offset
			continue
		}
		if g == "\t" {
			w = 4 - col%4
		}
		if col+w > width && col > 0 {
			flush(offset)
			start = offset
			if g == "\t" {
				w = 4
			}
		}
		shown := g
		if g == "\t" {
			w = min(w, width)
			shown = strings.Repeat(" ", w)
		} else if w > width {
			shown, w = "�", 1
		}
		row.stops = append(row.stops, draftStop{offset: offset, col: col})
		part(shown, -1)
		col += w
		offset += len(g)
	}
	if col == width {
		flush(offset)
		start = offset
	}
	flush(offset) // includes a cursor cell after a full final row
	at, atCol := 0, 0
	for i, r := range rows {
		if cursor < r.start || cursor > r.end {
			continue
		}
		at, atCol = i, 0
		for _, s := range r.stops {
			if s.offset <= cursor {
				atCol = s.col
			}
		}
	}
	return rows, at, min(atCol, width)
}

func style(text string, fg spaceview.Color, bold bool) string {
	return spaceview.StyleText(text, spaceview.Style{FG: fg, Bold: bold})
}

func fit(wm ansi.Method, s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = truncate(wm, s, max(width, 0), "")
	return s + strings.Repeat(" ", max(width-wm.StringWidth(s), 0))
}

func fenceMarker(line string) string {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	i := 0
	for i < len(line) && line[i] == line[0] {
		i++
	}
	if i < 3 {
		return ""
	}
	return line[:i]
}

func closesFence(line, fence string) bool {
	marker := fenceMarker(line)
	return len(marker) >= len(fence) && marker[0] == fence[0] && strings.TrimSpace(line[len(marker):]) == ""
}

func firstCode(text string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	fence := ""
	var code strings.Builder
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if fence == "" {
			fence = fenceMarker(trim)
			continue
		}
		if closesFence(trim, fence) {
			return code.String(), true
		}
		code.WriteString(line)
	}
	return code.String(), fence != ""
}

func firstLink(text string) string {
	for _, word := range strings.Fields(text) {
		for _, scheme := range []string{"https://", "http://"} {
			if i := strings.Index(word, scheme); i >= 0 {
				link := strings.TrimRight(word[i:], ").,;:]>\"'")
				if hostintegration.ValidateURL(link) == nil {
					return link
				}
			}
		}
	}
	return ""
}

func (m *Model) moveScroll(delta int) {
	if m.follow {
		m.scroll = max(m.lastRows-m.lastBodyHeight, 0)
	}
	m.scroll = min(max(m.scroll+delta, 0), max(m.lastRows-m.lastBodyHeight, 0))
	m.follow = m.scroll == max(m.lastRows-m.lastBodyHeight, 0)
	m.anchor = nil
	if m.follow {
		m.unread, m.unreadMentions = 0, 0
	}
}

func (m *Model) captureAnchor() {
	if !m.follow && m.anchor == nil && m.scroll < len(m.lastTranscript) {
		row := m.lastTranscript[m.scroll]
		m.anchor = &viewAnchor{key: row.key, line: row.line, offset: row.offset}
	}
}
