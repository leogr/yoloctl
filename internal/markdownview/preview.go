package markdownview

// TextSub records display bytes [Start, End) standing for Copy at Base in
// the line before substitution. Whole-region copy/source selection uses Copy.
type TextSub struct {
	Start, End, Base int
	Copy             string
}

// subAt widens [lo, hi) of a display line to cover any preview it touches,
// so a pointer never selects part of one.
func ReplacementBounds(dl DisplayLine, lo, hi int) (int, int) {
	for _, s := range dl.Subs {
		if lo < s.End && hi > s.Start {
			lo, hi = min(lo, s.Start), max(hi, s.End)
		}
	}
	return lo, hi
}

// subMap maps a line with previews back to its text before substitution,
// each preview as one unit standing for its token (see srcMap).
func ReplacementSourceMap(dl DisplayLine) *SourceMap {
	m := &SourceMap{Hi: len(dl.Text)}
	if dl.Subs == nil {
		for k := range len(dl.Text) {
			m.At = append(m.At, k)
		}
		return m
	}
	at, base := 0, 0
	for _, s := range dl.Subs {
		for ; at < s.Start; at, base = at+1, base+1 {
			m.At = append(m.At, base)
		}
		m.Units = append(m.Units, [2]int{s.Base, s.Base + len(s.Copy)})
		for range s.End - s.Start {
			m.At = append(m.At, -len(m.Units))
		}
		at, base = s.End, s.Base+len(s.Copy)
	}
	for ; at < len(dl.Text); at, base = at+1, base+1 {
		m.At = append(m.At, base)
	}
	m.Hi = base
	return m
}
