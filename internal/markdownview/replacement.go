package markdownview

import (
	"strings"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Replacement is an ordered, nonoverlapping display range supplied by the
// caller, with endpoints aligned to piece boundaries. Its original bytes
// remain the whole-region copy/source value.
type Replacement struct {
	Start, End int
	Text       string
	Style      spaceview.Style
	Act        Action
}

// ApplyReplacements owns only byte/geometry updates. Choosing a preview,
// excluding table cells and resolving an action remain caller decisions.
func ApplyReplacements(base []DisplayLine, replacements [][]Replacement) []DisplayLine {
	out := make([]DisplayLine, len(base))
	for n, dl := range base {
		out[n] = dl
		if n >= len(replacements) || len(replacements[n]) == 0 {
			continue
		}
		var text strings.Builder
		var pieces []Piece
		var subs []TextSub
		at := 0
		for _, replacement := range replacements[n] {
			from, to := replacement.Start, replacement.End
			shift := text.Len() - at
			for _, p := range dl.Pieces {
				if p.Start >= at && p.End <= from {
					p.Start, p.End = p.Start+shift, p.End+shift
					pieces = append(pieces, p)
				}
			}
			text.WriteString(dl.Text[at:from])
			start := text.Len()
			text.WriteString(replacement.Text)
			pieces = append(pieces, Piece{Start: start, End: text.Len(), Style: replacement.Style, Act: replacement.Act})
			subs = append(subs, TextSub{Start: start, End: text.Len(), Base: from, Copy: dl.Text[from:to]})
			at = to
		}
		shift := text.Len() - at
		for _, p := range dl.Pieces {
			if p.Start >= at {
				p.Start, p.End = p.Start+shift, p.End+shift
				pieces = append(pieces, p)
			}
		}
		text.WriteString(dl.Text[at:])
		out[n].Text, out[n].Pieces, out[n].Subs = text.String(), pieces, subs
	}
	return out
}
