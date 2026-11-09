package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// displaySequence uses the same decoder and ASCII/combining-mark correction as
// ultraviolet's styled-string renderer, keeping clusters intact around ANSI.
func DisplaySequence(wm ansi.Method, s string, state byte) (seq string, width, n int, next byte) {
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
func Truncate(wm ansi.Method, s string, width int, tail string) string {
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
		seq, cells, n, next := DisplaySequence(wm, s, state)
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
func TruncateLeft(wm ansi.Method, s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	var state byte
	col := 0
	for len(s) > 0 {
		seq, cells, n, next := DisplaySequence(wm, s, state)
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
