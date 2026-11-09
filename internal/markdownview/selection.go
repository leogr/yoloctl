package markdownview

import "strings"

// SourceSelection is one parsed document and its lazily requested mappings.
// Render must use the same pure source/rendered mode as its display lines.
type SourceSelection struct {
	Lines   []DisplayLine
	Sources []LineSource
	Text    string
	Render  func(string) string
}

func (q SourceSelection) Blank(from, to int) bool {
	for n := from; n <= to; n++ {
		if q.Lines[n].Text != "" || q.Lines[n].Decor || strings.TrimSpace(q.Text[q.Sources[n].Start:q.Sources[n].End]) != "" {
			return false
		}
	}
	return true
}

func (q SourceSelection) Exact(la, oa, lb, ob int, startEdge, endEdge bool) (string, bool) {
	if q.Render == nil {
		return "", false
	}
	var parts []string
	for n := la; n <= lb; n++ {
		if q.Lines[n].Decor {
			continue
		}
		lo, hi := 0, len(q.Lines[n].Text)
		if n == la {
			lo = min(oa, hi)
		}
		if n == lb {
			hi = min(ob, hi)
		}
		parts = append(parts, q.Lines[n].CopyText(min(lo, hi), hi))
	}
	// Line breaks at the highlight's own ends are the edges of lines
	// highlighted from their end or to their start: they quote nothing.
	want := strings.Join(parts, "\n")
	sb, se, okA := LineStart(q.Sources[la], oa)
	eb, ee, okB := LineEnd(q.Sources[lb], ob)
	if startEdge {
		want = strings.TrimLeft(want, "\n")
	} else {
		sb, se, okA = q.Sources[la].Start, q.Sources[la].Start, true
	}
	if endEdge {
		want = strings.TrimRight(want, "\n")
	} else {
		eb, ee, okB = q.Sources[lb].End, q.Sources[lb].End, true
	}
	if !okA || !okB {
		return "", false
	}
	// Markup touching the ends first, so whole spans keep their markers.
	for _, c := range [][2]int{{se, ee}, {sb, eb}} {
		s, e := c[0], c[1]
		for startEdge && s < e && q.Text[s] == '\n' {
			s++
		}
		for endEdge && e > s && q.Text[e-1] == '\n' {
			e--
		}
		if s >= e {
			continue
		}
		src := Fenced(q.Text, q.Sources, la, lb, s, e)
		if q.Render(src) == want {
			return src, true
		}
	}
	if want == "" {
		return "", true
	}
	return "", false
}
