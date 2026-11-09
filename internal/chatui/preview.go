package chatui

import (
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// A reference preview's budget, so several on one line stay short: the
// author's name in cells and the start of the message in runes.
const (
	previewName    = 16
	previewSnippet = 24
)

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
		if dl.Table != nil {
			continue // its cells are aligned before any preview
		}
		for k, p := range dl.Pieces {
			if p.Act.Kind != actRef || k > 0 && dl.Pieces[k-1].Act == p.Act && dl.Pieces[k-1].End == p.Start {
				continue // one entry per token, whatever pieces it spans
			}
			label := ""
			if j, ok := loaded[p.Act.Value]; ok && j != i {
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
	replacements := make([][]markdownview.Replacement, len(base))
	next := 0
	for n, dl := range base {
		if dl.Table != nil {
			continue
		}
		for k := 0; k < len(dl.Pieces); k++ {
			p := dl.Pieces[k]
			if p.Act.Kind != actRef {
				continue
			}
			end := k
			for end+1 < len(dl.Pieces) && dl.Pieces[end+1].Act == p.Act && dl.Pieces[end+1].Start == dl.Pieces[end].End {
				end++
			}
			label := keys[next]
			next++
			if label != "" {
				replacements[n] = append(replacements[n], markdownview.Replacement{Start: p.Start, End: dl.Pieces[end].End, Text: label, Style: spaceview.Style{FG: theme.Dim}, Act: p.Act})
			}
			k = end
		}
	}
	return markdownview.ApplyReplacements(base, replacements)
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
