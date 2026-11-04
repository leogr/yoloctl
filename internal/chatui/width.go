package chatui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// setWidthMethod discards display coordinates from the old method. Body lines
// contain table padding and expanded tabs, so discarding wrapped rows alone
// would leave stale byte positions, including in channels that are not visible.
func (m *Model) setWidthMethod(method ansi.Method) {
	if m.widthMethod == method {
		return
	}
	m.widthMethod = method
	m.eachView(func() {
		m.captureAnchor()
		if m.anchor != nil {
			m.anchor.offset = 0 // retain the message/logical line, not old padding
		}
		m.sel = nil
		m.bodyCache = map[string]cachedBody{}
		m.built.rows, m.lastTranscript, m.draft.rows = nil, nil, nil
		m.editor.column = nil
		m.touch()
	})
	// Cancel every pending gesture/tick. Its eventual release sees no active
	// press, so it cannot copy a selection or activate a moved target.
	m.gesture++
	m.drag, m.hover = dragState{}, hoverKey{}
	m.marquee = marquee{gen: m.marquee.gen + 1}
	m.hits, m.layout = nil, viewLayout{}
	if m.linkPrompt != nil {
		m.linkPrompt.scroll = 0
	}
	// Update can receive another mouse event before the framework paints.
	// Publish fresh coordinates now, without waiting for a window resize.
	m.View()
}

// displaySequence uses the same decoder and ASCII/combining-mark correction as
// ultraviolet's styled-string renderer, keeping clusters intact around ANSI.
func displaySequence(wm ansi.Method, s string, state byte) (seq string, width, n int, next byte) {
	seq, width, n, next = wm.DecodeSequenceInString(s, state, nil)
	if n == 1 && seq[0] > 0x1f && seq[0] < 0x7f && len(s) > 1 && s[1] >= 0xc0 {
		if cluster, cw := ansi.FirstGraphemeCluster(s, wm); len(cluster) > 1 {
			seq, width, n = cluster, cw, len(cluster)
		}
	}
	return
}

// truncate keeps whole clusters within width, then emits tail and the remaining
// ANSI controls (not text), so styles still end correctly. x/ansi v0.11.8's
// Method.Truncate tests its fast path with grapheme StringWidth even in wcwidth
// mode; long ZWJ strings can pass through untruncated. Measure and decode with
// the selected method throughout instead.
func truncate(wm ansi.Method, s string, width int, tail string) string {
	if wm.StringWidth(s) <= width {
		return s
	}
	room := width - wm.StringWidth(tail)
	if room < 0 {
		return ""
	}
	var b strings.Builder
	var state byte
	used, clipped := 0, false
	for len(s) > 0 {
		seq, cells, n, next := displaySequence(wm, s, state)
		if !clipped && used+cells > room {
			b.WriteString(tail)
			clipped = true
		}
		if !clipped || cells == 0 && seq[0] == '\x1b' {
			b.WriteString(seq)
		}
		used += cells
		s, state = s[n:], next
	}
	return b.String()
}

// truncateLeft removes width cells while preserving ANSI style state. If the
// cut crosses a wide cluster, only its uncovered cells remain, as spaces; the
// pinned Method.TruncateLeft instead keeps that entire crossing cluster.
func truncateLeft(wm ansi.Method, s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	var state byte
	col := 0
	for len(s) > 0 {
		seq, cells, n, next := displaySequence(wm, s, state)
		switch {
		case col >= width || cells == 0 && seq[0] == '\x1b':
			b.WriteString(seq)
		case col+cells > width:
			b.WriteString(strings.Repeat(" ", col+cells-width))
		}
		col += cells
		s, state = s[n:], next
	}
	return b.String()
}
