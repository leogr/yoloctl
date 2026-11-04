package spaceview

import "strings"

const eighths = " ▏▎▍▌▋▊▉"

// gauge is a thermometer bar: every filled cell keeps the heat color of its
// own position, so a fuller bar reveals hotter colors. Cells excludes the two
// end caps.
func gauge(t Theme, frac float64, cells int, hit int) line {
	out := line{{text: "▕", style: fg(t.Dim), hit: hit}}
	filled := clamp01(frac) * float64(cells)
	for i := range cells {
		p := (float64(i) + 0.5) / float64(cells)
		switch part := int((filled - float64(i)) * 8); {
		case float64(i+1) <= filled:
			out = append(out, seg{text: "█", style: fg(t.heat(p)), hit: hit})
		case part >= 2: // a one-eighth sliver reads as the end cap
			out = append(out, seg{text: string([]rune(eighths)[part]), style: fg(t.heat(p)), hit: hit})
		default:
			out = append(out, seg{text: "░", style: fg(t.Faint), hit: hit})
		}
	}
	return append(out, seg{text: "▏", style: fg(t.Dim), hit: hit})
}

// part is one consumer in a stacked bar.
type part struct {
	value float64
	color Color
	hit   int
}

// stacked shows who uses what: one solid run per consumer in its accent
// color, in order, with the remainder empty. Rounding never overflows.
func stacked(t Theme, parts []part, total float64, cells int, hit int) line {
	out := line{{text: "▕", style: fg(t.Dim), hit: hit}}
	used := 0
	acc := 0.0
	for _, p := range parts {
		if total <= 0 || p.value <= 0 {
			continue
		}
		acc += p.value
		end := min(int(acc/total*float64(cells)+0.5), cells)
		if n := end - used; n > 0 {
			h := hit
			if p.hit != 0 {
				h = p.hit
			}
			out = append(out, seg{text: strings.Repeat("█", n), style: fg(p.color), hit: h})
			used = end
		}
	}
	if rest := cells - used; rest > 0 {
		out = append(out, seg{text: strings.Repeat("░", rest), style: fg(t.Faint), hit: hit})
	}
	return append(out, seg{text: "▏", style: fg(t.Dim), hit: hit})
}

// Braille dot bits, bottom to top, for the left and right columns of a cell.
var (
	leftDots  = [4]rune{0x40, 0x04, 0x02, 0x01}
	rightDots = [4]rune{0x80, 0x20, 0x10, 0x08}
)

func brailleLevel(a, b float64, levels, lo int) rune {
	var bits rune
	for _, v := range []struct {
		value float64
		dots  [4]rune
	}{{a, leftDots}, {b, rightDots}} {
		n := min(max(int(clamp01(v.value)*float64(levels)+0.5)-lo, 0), 4)
		for k := range n {
			bits |= v.dots[k]
		}
	}
	return bits
}

// tail returns the last 2*cells values, left-padded with zeros.
func tail(history []float64, cells int) []float64 {
	out := make([]float64, 2*cells)
	n := min(len(history), len(out))
	copy(out[len(out)-n:], history[len(history)-n:])
	return out
}

// spark is a one-row braille sparkline: two samples per cell, four levels,
// each cell colored by its hotter sample. Values are fractions of the scale.
func spark(t Theme, history []float64, cells int, hit int) line {
	s := tail(history, cells)
	out := make(line, 0, cells)
	for i := range cells {
		a, b := s[2*i], s[2*i+1]
		if bits := brailleLevel(a, b, 4, 0); bits != 0 {
			out = append(out, seg{text: string(0x2800 + bits), style: fg(t.heat(max(a, b))), hit: hit})
		} else {
			out = append(out, seg{text: "⣀", style: fg(t.Faint), hit: hit})
		}
	}
	return out
}

// chart2 is a two-row braille area chart with eight levels.
func chart2(t Theme, history []float64, cells int, hit int) (top, bottom line) {
	s := tail(history, cells)
	for i := range cells {
		a, b := s[2*i], s[2*i+1]
		st := fg(t.heat(max(a, b)))
		if bits := brailleLevel(a, b, 8, 0); bits != 0 {
			bottom = append(bottom, seg{text: string(0x2800 + bits), style: st, hit: hit})
		} else {
			bottom = append(bottom, seg{text: "⣀", style: fg(t.Faint), hit: hit})
		}
		if bits := brailleLevel(a, b, 8, 4); bits != 0 {
			top = append(top, seg{text: string(0x2800 + bits), style: st, hit: hit})
		} else {
			top = append(top, seg{text: " ", hit: hit})
		}
	}
	return top, bottom
}

func clamp01(v float64) float64 {
	switch {
	case v != v || v < 0: // NaN or negative
		return 0
	case v > 1:
		return 1
	}
	return v
}
