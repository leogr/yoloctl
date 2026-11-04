package chatui

import (
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// A reference preview's budget, so several on one line stay short: the
// author's name in cells and the start of the message in runes.
const (
	previewName    = 16
	previewSnippet = 24
)

// textSub is a reference shown as its preview: display bytes [start, end)
// of a line stand for the token copy, which starts at base in the line
// before substitution. Copy and Quote see the token, never the preview.
type textSub struct {
	start, end, base int
	copy             string
}

// previewLabel is how an inline preview shows a loaded message.
func (m *Model) previewLabel(msg *Message) string {
	snippet := strings.Join(strings.Fields(plain(msg.Text)), " ")
	if r := []rune(snippet); len(r) > previewSnippet {
		snippet = string(r[:previewSnippet]) + "…"
	}
	return "↗ " + truncate(m.widthMethod, oneLine(m.author(msg).name), previewName, "…") + ": " + snippet
}

// previewKeys lists, for message i's references in its target-independent
// lines, the preview each would show ("" stays the token): the cache key
// of its substituted lines. A reference to the message itself, one whose
// exact ID is not loaded in this view, and any on a table line stay tokens.
func (m *Model) previewKeys(i int, base []displayLine, loaded map[string]int) []string {
	var out []string
	for _, dl := range base {
		if dl.table != nil {
			continue // its cells are aligned before any preview
		}
		for k, p := range dl.pieces {
			if p.act.kind != actRef || k > 0 && dl.pieces[k-1].act == p.act && dl.pieces[k-1].end == p.start {
				continue // one entry per token, whatever pieces it spans
			}
			label := ""
			if j, ok := loaded[p.act.value]; ok && j != i {
				label = m.previewLabel(&m.messages[j])
			}
			out = append(out, label)
		}
	}
	return out
}

// substitute shows each reference with a label in keys (in previewKeys
// order) as one dimmed span with the token's own click target.
func substitute(base []displayLine, keys []string, theme spaceview.Theme) []displayLine {
	out := make([]displayLine, len(base))
	next := 0
	for n, dl := range base {
		out[n] = dl
		if dl.table != nil {
			continue
		}
		var text strings.Builder
		var pieces []piece
		var subs []textSub
		at := 0 // bytes of dl.text written
		for k := 0; k < len(dl.pieces); k++ {
			p := dl.pieces[k]
			if p.act.kind != actRef {
				continue
			}
			end := k
			for end+1 < len(dl.pieces) && dl.pieces[end+1].act == p.act && dl.pieces[end+1].start == dl.pieces[end].end {
				end++
			}
			label := keys[next]
			next++
			if label == "" {
				k = end
				continue
			}
			from, to := p.start, dl.pieces[end].end
			shift := text.Len() - at
			for _, q := range dl.pieces {
				if q.start >= at && q.end <= from {
					q.start, q.end = q.start+shift, q.end+shift
					pieces = append(pieces, q)
				}
			}
			text.WriteString(dl.text[at:from])
			start := text.Len()
			text.WriteString(label)
			pieces = append(pieces, piece{start: start, end: text.Len(), style: spaceview.Style{FG: theme.Dim}, act: p.act})
			subs = append(subs, textSub{start: start, end: text.Len(), base: from, copy: dl.text[from:to]})
			at, k = to, end
		}
		if subs == nil {
			continue
		}
		shift := text.Len() - at
		for _, q := range dl.pieces {
			if q.start >= at {
				q.start, q.end = q.start+shift, q.end+shift
				pieces = append(pieces, q)
			}
		}
		text.WriteString(dl.text[at:])
		out[n].text, out[n].pieces, out[n].subs = text.String(), pieces, subs
	}
	return out
}

// subAt widens [lo, hi) of a display line to cover any preview it touches,
// so a pointer never selects part of one.
func subAt(dl displayLine, lo, hi int) (int, int) {
	for _, s := range dl.subs {
		if lo < s.end && hi > s.start {
			lo, hi = min(lo, s.start), max(hi, s.end)
		}
	}
	return lo, hi
}

// subMap maps a line with previews back to its text before substitution,
// each preview as one unit standing for its token (see srcMap).
func subMap(dl displayLine) *srcMap {
	m := &srcMap{hi: len(dl.text)}
	if dl.subs == nil {
		for k := range len(dl.text) {
			m.at = append(m.at, k)
		}
		return m
	}
	at, base := 0, 0
	for _, s := range dl.subs {
		for ; at < s.start; at, base = at+1, base+1 {
			m.at = append(m.at, base)
		}
		m.units = append(m.units, [2]int{s.base, s.base + len(s.copy)})
		for range s.end - s.start {
			m.at = append(m.at, -len(m.units))
		}
		at, base = s.end, s.base+len(s.copy)
	}
	for ; at < len(dl.text); at, base = at+1, base+1 {
		m.at = append(m.at, base)
	}
	m.hi = base
	return m
}

// refreshPreviews brings message i's previews up to date with what is
// loaded, before its rows are built. When they change, its lines change: a
// highlight with an end in it clears, since its offsets no longer name the
// same text, and a reading position on one of its lines moves to that
// line's start.
func (m *Model) refreshPreviews(i, width int, loaded map[string]int) {
	msg := &m.messages[i]
	body := m.body(msg, width)
	keys := m.previewKeys(i, body.base, loaded)
	if slices.Equal(keys, body.keys) || len(body.keys) == 0 && !slices.ContainsFunc(keys, func(k string) bool { return k != "" }) {
		return
	}
	key := messageKey(*msg)
	body.keys, body.lines, body.width = keys, substitute(body.base, keys, m.theme), -1
	m.bodyCache[key] = body
	m.body(msg, width)
	if m.sel != nil && (m.sel.anchor.key == key || m.sel.focus.key == key) {
		m.sel = nil
	}
	if m.anchor != nil && m.anchor.key == key && m.anchor.line >= 0 {
		m.anchor.offset = 0
	}
}
