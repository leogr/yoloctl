package markdownview

import "strings"

// SourceMap maps each byte of a derived string to the string it came from:
// a verbatim copy maps byte for byte; text shown in place of other bytes (a
// bullet, a tab's spaces, an escaped character, a link's address) is a unit
// that maps only as a whole. Input bytes no output byte maps to are markup
// the view drops. A nil *SourceMap records nothing.
type SourceMap struct {
	At     []int    // per byte: input offset, or -1-u inside unit u
	Units  [][2]int // input ranges shown as other text
	Lo, Hi int      // the input's bounds
}

// add records text, shown in place of in[from:to].
func (m *SourceMap) Add(text, in string, from, to int) {
	if m == nil {
		return
	}
	if in[from:to] == text {
		for k := range len(text) {
			m.At = append(m.At, from+k)
		}
		return
	}
	m.Units = append(m.Units, [2]int{from, to})
	for range len(text) {
		m.At = append(m.At, -len(m.Units))
	}
}

// startOf and endOf bound the input that output byte k shows.
func (m *SourceMap) StartOf(k int) int {
	if a := m.At[k]; a >= 0 {
		return a
	}
	return m.Units[-1-m.At[k]][0]
}

func (m *SourceMap) EndOf(k int) int {
	if a := m.At[k]; a >= 0 {
		return a + 1
	}
	return m.Units[-1-m.At[k]][1]
}

// before and after map an output boundary to the input before or after any
// dropped markup there.
func (m *SourceMap) Before(k int) int {
	if k == 0 {
		return m.Lo
	}
	return m.EndOf(k - 1)
}

func (m *SourceMap) After(k int) int {
	if k == len(m.At) {
		return m.Hi
	}
	return m.StartOf(k)
}

// then composes m (output → middle) with inner (middle → input).
func (m *SourceMap) Then(inner *SourceMap) *SourceMap {
	out := &SourceMap{Lo: inner.Lo, Hi: inner.Hi}
	mine, theirs := map[int]int{}, map[int]int{}
	unit := func(seen map[int]int, key int, r [2]int) int {
		u, ok := seen[key]
		if !ok {
			out.Units = append(out.Units, r)
			u = len(out.Units) - 1
			seen[key] = u
		}
		return -1 - u
	}
	for _, a := range m.At {
		switch {
		case a >= 0 && inner.At[a] >= 0:
			out.At = append(out.At, inner.At[a])
		case a >= 0:
			out.At = append(out.At, unit(theirs, inner.At[a], inner.Units[-1-inner.At[a]]))
		default:
			r := m.Units[-1-a]
			out.At = append(out.At, unit(mine, a, [2]int{inner.After(r[0]), inner.Before(r[1])}))
		}
	}
	return out
}

// shift moves the input offsets by base.
func (m *SourceMap) Shift(base int) *SourceMap {
	out := &SourceMap{At: make([]int, len(m.At)), Units: make([][2]int, len(m.Units)), Lo: m.Lo + base, Hi: m.Hi + base}
	for k, a := range m.At {
		out.At[k] = a
		if a >= 0 {
			out.At[k] = a + base
		}
	}
	for u, r := range m.Units {
		out.Units[u] = [2]int{r[0] + base, r[1] + base}
	}
	return out
}

// LineSource is where one display line comes from in plain(text): its source
// line, how its bytes map (nil for a line that maps only whole: a fence
// frame or a rebuilt table line, which Quote reads as cells instead), and the
// fence it belongs to (1-based).
type LineSource struct {
	Start, End int
	Src        *SourceMap
	Fence      int
}

// QuoteLine is one line Quote inserts: source, or the selected text of a
// table row, which is kept as is even when it holds only TABs.
type QuoteLine struct {
	Text  string
	Table bool
}

// tableText is the selected text of a rendered table's display lines from
// through to, the selection starting at byte lo of line from and ending at
// byte hi of line to: one line per row, holding its reached cells' selected
// text joined by TAB, an empty cell between them an empty field. A row the
// selection covers from its start or to its end reaches its first or last
// cell; otherwise its first and last reached cells are those with selected
// text, so padding and separators at either end add nothing. The rule and a
// row reached only in padding or separators contribute nothing.
func TableText(lines []DisplayLine, from, to, lo, hi int) []QuoteLine {
	var out []QuoteLine
	for n := from; n <= to; n++ {
		dl := lines[n]
		if dl.Decor {
			continue
		}
		a, b := 0, len(dl.Text)
		if n == from {
			a = min(lo, b)
		}
		if n == to {
			b = min(hi, b)
		}
		first, last := -1, -1
		for c, cr := range dl.Cells {
			if max(cr[0], a) < min(cr[1], b) {
				if first < 0 {
					first = c
				}
				last = c
			}
		}
		if a == 0 && b == len(dl.Text) {
			first, last = 0, len(dl.Cells)-1
		} else if first >= 0 {
			if a == 0 {
				first = 0
			}
			if b == len(dl.Text) {
				last = len(dl.Cells) - 1
			}
		}
		if first < 0 {
			continue
		}
		fields := make([]string, 0, last-first+1)
		for _, cr := range dl.Cells[first : last+1] {
			s, e := max(cr[0], a), min(cr[1], b)
			fields = append(fields, dl.Text[s:max(s, e)])
		}
		out = append(out, QuoteLine{Text: strings.Join(fields, "\t"), Table: true})
	}
	return out
}

// lineStart maps a highlight starting at display offset o of a line to the
// source offset of that point and the one before any markup touching it.
func LineStart(ls LineSource, o int) (bare, wide int, ok bool) {
	if ls.Src == nil {
		return ls.Start, ls.Start, true // a fence frame, from its start
	}
	m := ls.Src
	if o == len(m.At) {
		return m.Hi, m.Hi, true
	}
	if m.At[o] < 0 && o > 0 && m.At[o-1] == m.At[o] {
		return 0, 0, false // inside a unit
	}
	return m.StartOf(o), m.Before(o), true
}

// lineEnd maps a highlight ending at display offset o of a line like
// lineStart, with markup after it.
func LineEnd(ls LineSource, o int) (bare, wide int, ok bool) {
	if ls.Src == nil {
		return ls.End, ls.End, true
	}
	m := ls.Src
	if o == 0 {
		return m.Lo, m.Lo, true
	}
	if m.At[o-1] < 0 && o < len(m.At) && m.At[o] == m.At[o-1] {
		return 0, 0, false
	}
	return m.EndOf(o - 1), m.After(o), true
}

// fenced is text[s:e] with the opener of the fence it starts inside and the
// closer of the one it ends inside, so a quoted code block stays one.
func Fenced(text string, srcs []LineSource, la, lb, s, e int) string {
	q := text[s:e]
	if f := srcs[la].Fence; f > 0 {
		if open := FenceLine(srcs, f, true); open >= 0 && s > srcs[open].Start {
			q = text[srcs[open].Start:srcs[open].End] + "\n" + q
		}
	}
	if f := srcs[lb].Fence; f > 0 {
		open, end := FenceLine(srcs, f, true), FenceLine(srcs, f, false)
		switch {
		case end >= 0 && end != open && e < srcs[end].End:
			q += "\n" + text[srcs[end].Start:srcs[end].End]
		case end == open && open >= 0:
			// The source never closes it: close with its own marker.
			q += "\n" + FenceMarker(strings.TrimLeft(text[srcs[open].Start:srcs[open].End], " "))
		}
	}
	return q
}

// fenceLine finds fence f's opener, or its closer (the opener again when the
// source never closes it).
func FenceLine(srcs []LineSource, f int, open bool) int {
	first, last := -1, -1
	for k, ls := range srcs {
		if ls.Fence == f {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if open || last == first {
		return first
	}
	if srcs[last].Src == nil {
		return last // the closing frame
	}
	return first // unclosed: its last line is code
}
