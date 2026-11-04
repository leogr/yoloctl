package chatui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// selPoint is one selection endpoint in logical coordinates: a message, one
// of its display lines and the grapheme [lo, hi) under the pointer. Logical
// points survive scrolling, resizing, the participants panel and arrivals.
type selPoint struct {
	key    string
	line   int
	lo, hi int
}

// selection is a drag over transcript text. It copies the displayed body
// text only; headers, times, chips, gutters and frames are never part of it.
type selection struct {
	anchor, focus selPoint
}

// dragState tracks one left-button gesture from press to release.
type dragState struct {
	active       bool
	moved        bool
	x, y         int // press cell
	lastX, lastY int
	press        selPoint
	pressOK      bool
	edge         int // -1 above, +1 below the transcript while dragging
	ticking      bool
	act          action // click target under the press
	key          string // message under the press
	area         hitArea
	areaItem     int
	id           string
}

type dragTickMsg struct{ gesture uint64 }

// dragTick auto-scrolls while the pointer is held past a transcript edge.
func dragTick(gesture uint64) tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return dragTickMsg{gesture} })
}

// maxSelectionBytes matches the host OSC 52 bound: an oversized selection
// is refused with a notice rather than truncated.
const maxSelectionBytes = 1 << 20

// ordered returns the selection's start and end as message indexes, lines
// and byte offsets. The earlier point starts at its grapheme's start and
// the later ends after its grapheme, so the cells under both are included.
func (m *Model) ordered(s selection) (ia, la, oa, ib, lb, ob int, ok bool) {
	a, b := s.anchor, s.focus
	ia, ib = m.indexOf(a.key), m.indexOf(b.key)
	if ia < 0 || ib < 0 {
		return 0, 0, 0, 0, 0, 0, false
	}
	if ib < ia || ib == ia && (b.line < a.line || b.line == a.line && b.lo < a.lo) {
		a, b, ia, ib = b, a, ib, ia
	}
	return ia, a.line, a.lo, ib, b.line, b.hi, true
}

// selectedText assembles the selection from display-line byte ranges:
// soft-wrapped rows of a line join with nothing, lines with "\n", and
// messages with one blank line. Fence frames and messages a filter hides
// contribute nothing.
func (m *Model) selectedText(s selection) (string, bool) {
	parts := m.selectedParts(s)
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.text
	}
	text := strings.Join(texts, "\n\n")
	return text, text != ""
}

// selectedPart is the selected text of one message.
type selectedPart struct {
	index int
	text  string
}

// selectedParts lists the selection's text per shown message, in order. An
// endpoint resting at a message edge selects nothing of that message, so
// such a part is left out.
func (m *Model) selectedParts(s selection) []selectedPart {
	ia, la, oa, ib, lb, ob, ok := m.ordered(s)
	if !ok {
		return nil
	}
	b := nameBindings{}
	if m.filter != nil {
		b = m.bindings()
	}
	shown := m.shown(b)
	var parts []selectedPart
	for i := ia; i <= ib; i++ {
		if !shown[i] {
			continue
		}
		lines := m.body(&m.messages[i], max(m.lastTextWidth, 1)).lines
		first, last := 0, len(lines)-1
		if i == ia {
			first = la
		}
		if i == ib {
			last = lb
		}
		var out []string
		for n := max(first, 0); n <= last && n < len(lines); n++ {
			if lines[n].decor {
				continue
			}
			text := lines[n].text
			lo, hi := 0, len(text)
			if i == ia && n == la {
				lo = min(oa, len(text))
			}
			if i == ib && n == lb {
				hi = min(ob, len(text))
			}
			if lo > hi {
				lo = hi
			}
			out = append(out, lines[n].copyText(lo, hi))
		}
		parts = append(parts, selectedPart{i, strings.Join(out, "\n")})
	}
	for len(parts) > 1 && parts[len(parts)-1].text == "" {
		parts = parts[:len(parts)-1]
	}
	for len(parts) > 1 && parts[0].text == "" {
		parts = parts[1:]
	}
	return parts
}

// rowRange reports which display-line bytes of a body row are selected.
func (m *Model) rowRange(row transcriptRow, ia, la, oa, ib, lb, ob int) (int, int, bool) {
	if row.kind != rowBody {
		return 0, 0, false
	}
	i := m.indexOf(row.key)
	before := func(i1, l1, i2, l2 int) bool { return i1 < i2 || i1 == i2 && l1 < l2 }
	if before(i, row.line, ia, la) || before(ib, lb, i, row.line) {
		return 0, 0, false
	}
	lo, hi := row.offset, row.end
	if i == ia && row.line == la {
		lo = max(lo, oa)
	}
	if i == ib && row.line == lb {
		hi = min(hi, ob)
	}
	return lo, hi, lo < hi
}

// pointAt maps a transcript cell to a selection point. Rows around a body
// snap to its start (separators, header, reply label) or end (errors and
// the spacer), so a drag across them selects whole message text.
func (m *Model) pointAt(x, y int) (selPoint, bool) {
	l := m.layout
	i := m.scroll + y - l.bodyTop
	if y < l.bodyTop {
		i = m.scroll
	}
	if y >= l.bodyTop+l.bodyHeight {
		i = m.scroll + l.bodyHeight - 1
	}
	if len(m.lastTranscript) == 0 {
		return selPoint{}, false
	}
	i = min(max(i, 0), len(m.lastTranscript)-1)
	row := m.lastTranscript[i]
	if row.kind == rowBody {
		col := min(max(x, 0), m.lastTextWidth+bodyIndent)
		if y < l.bodyTop {
			col = 0
		}
		if y >= l.bodyTop+l.bodyHeight {
			col = l.transcriptWidth
		}
		lo, hi := hitRun(m.widthMethod, row.runs, col, row.end)
		if i := m.indexOf(row.key); i >= 0 {
			if lines := m.body(&m.messages[i], max(m.lastTextWidth, 1)).lines; row.line < len(lines) {
				lo, hi = subAt(lines[row.line], lo, hi)
			}
		}
		return selPoint{key: row.key, line: row.line, lo: lo, hi: hi}, true
	}
	msg := m.indexOf(row.key)
	if msg < 0 {
		return selPoint{}, false
	}
	lines := m.body(&m.messages[msg], max(m.lastTextWidth, 1)).lines
	switch {
	case row.line < 0:
		return selPoint{key: row.key}, true
	case row.line < len(lines): // a fence frame inside the body
		return selPoint{key: row.key, line: row.line}, true
	}
	last := max(len(lines)-1, 0)
	end := 0
	if last < len(lines) {
		end = len(lines[last].text)
	}
	return selPoint{key: row.key, line: last, lo: end, hi: end}, true
}

// copySelection writes the finished selection through the host clipboard
// callback, which applies seams.clipboard. It reports what happened.
func (m *Model) copySelection() tea.Cmd {
	if m.sel == nil {
		return nil
	}
	text, ok := m.selectedText(*m.sel)
	switch {
	case !ok:
		return nil
	case !m.copyOnSelect:
		m.say(levelInfo, "Selected; copy on select is off in /settings")
		return nil
	case len(text) > maxSelectionBytes:
		m.say(levelWarn, "Selection is larger than 1 MiB; nothing was copied")
		return nil
	case m.opts.Copy == nil || m.opts.Clipboard == "off":
		m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
		return nil
	}
	lines := strings.Count(text, "\n") + 1
	what := fmt.Sprintf("%d characters", len([]rune(text)))
	if lines > 1 {
		what = fmt.Sprintf("%d lines", lines)
	}
	m.say(levelOK, "Copied "+what+" of displayed text · Esc clears the highlight")
	return m.opts.Copy(text)
}
