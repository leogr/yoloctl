package chatui

import "github.com/leogr/yoloctl/internal/tui/spaceview"

const (
	actNone     actionKind = iota
	actLink                // value: URL
	actAuthor              // value: authenticated account ("" if unknown), extra: nick
	actReply               // value: replied-to message ID
	actMention             // value: mentioned nick
	actRef                 // value: referenced message's server ID
	actFilter              // shows all messages again
	actCopyCode            // value: a fenced block's code, copied as is
)

type rowKind int

const (
	rowBody rowKind = iota
	rowHeader
	rowDecor // reply labels, fence frames, separators, errors and limits
	rowSpacer
	// rowEvent is a room notice (join, part, role, topic, guidance). It is
	// keyed to the preceding message only to anchor the reading position:
	// it never shows that message's marks, and clicking it selects nothing.
	rowEvent
)

// transcriptRow is one display row. key/line/offset anchor the reading
// position; body rows also carry [offset, end) of their display line, so a
// selection maps back to exact bytes regardless of wrapping. A wrapped
// table row shows only frags of that range.
type transcriptRow struct {
	key          string
	line, offset int
	end          int
	kind         rowKind
	runs         []run
	// fill paints the cells after the runs, e.g. a code block background.
	fill  spaceview.Color
	frags [][2]int
}

// holds reports whether a wrapped table row shows the display-line byte at
// offset: its [offset, end) envelope alone does not prove it.
func (r transcriptRow) holds(offset int) bool {
	for _, f := range r.frags {
		if f[0] <= offset && offset < f[1] {
			return true
		}
	}
	return false
}

// nextFrag returns where the row's first cell fragment after offset starts,
// or -1: the next text a table shows after an offset in its padding or
// separators.
func (r transcriptRow) nextFrag(offset int) int {
	next := -1
	for _, f := range r.frags {
		if f[0] > offset && (next < 0 || f[0] < next) {
			next = f[0]
		}
	}
	return next
}
