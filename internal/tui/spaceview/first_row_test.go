package spaceview

import (
	"image/color"
	"math"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// firstRowThemes are terminal backgrounds the first row must ignore: none
// reported, black, a dark non-black, white and a light non-white one.
var firstRowThemes = []struct {
	name string
	bg   color.Color
}{
	{"unreported", nil},
	{"black", color.Black},
	{"slate", color.RGBA{0x30, 0x34, 0x46, 0xff}},
	{"white", color.White},
	{"cream", color.RGBA{0xfd, 0xf6, 0xe3, 0xff}},
}

// contrastOnBlack is the WCAG contrast ratio of c against black.
func contrastOnBlack(c Color) float64 {
	r, g, b, ok := c.rgb()
	if !ok {
		return 0
	}
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return (0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b) + 0.05) / 0.05
}

// The first row continues the TUI's active tab: black across the whole
// width in every theme and state, including the operator warning, hover,
// padding, truncation and the resize hint. Its text stays readable on black,
// and its content and click targets do not depend on the theme.
func TestFirstRowIsBlackInEveryState(t *testing.T) {
	black := rgb(0, 0, 0)
	states := []struct {
		name string
		set  func(*Model)
	}{
		{"live", func(*Model) {}},
		{"paused", func(m *Model) { m.paused = true }},
		{"connecting", func(m *Model) { m.conn.state = connConnecting }},
		{"signal lost", func(m *Model) { m.conn.state, m.conn.retryAt = connLost, time.Now().Add(time.Hour) }},
		{"waiting", func(m *Model) { m.d.inv = nil }},
		{"hovered chips", func(m *Model) { m.hover = target{tDisk, ""}.key() }},
		{"hovered warning", func(m *Model) { m.hover = target{tCapability, operatorCapability}.key() }},
		{"popup", func(m *Model) { m.openHelp() }},
	}
	render := func(bg color.Color, w, h int, set func(*Model)) *Model {
		m := fixtureModel(t, w, h)
		if bg != nil {
			m.Update(tea.BackgroundColorMsg{Color: bg})
		}
		set(m)
		m.View()
		return m
	}
	sizes := append(slices.Clone(sizes), [2]int{89, 30}, [2]int{90, 30}, [2]int{69, 20}, [2]int{70, 20})
	for _, st := range states {
		for _, size := range sizes {
			w, h := size[0], size[1]
			want := render(nil, w, h, st.set)
			row := want.last.c.cells[0]
			if len(row) != w {
				t.Fatalf("%s %dx%d: first row has %d cells", st.name, w, h, len(row))
			}
			for x, cl := range row {
				if cl.style.BG != black {
					t.Fatalf("%s %dx%d: cell %d (%q) background %v, want black:\n%s", st.name, w, h, x, cl.text, cl.style.BG, want.last.c.plain())
				}
				if cl.cont || cl.text == " " {
					continue
				}
				if r := contrastOnBlack(cl.style.FG); r < 4.5 {
					t.Errorf("%s %dx%d: cell %d (%q) contrast %.1f on black", st.name, w, h, x, cl.text, r)
				}
			}
			for _, th := range firstRowThemes[1:] {
				got := render(th.bg, w, h, st.set)
				if !slices.Equal(got.last.c.cells[0], row) || !slices.Equal(got.last.targets, want.last.targets) {
					t.Errorf("%s %dx%d: first row differs on a %s terminal", st.name, w, h, th.name)
				}
			}
		}
	}
}

// The first row's warning keeps its meaning without its red background: its
// label and click target, in the dark palette's bold warning color on black.
func TestFirstRowOperatorWarningStaysDistinct(t *testing.T) {
	want := Style{FG: newTheme(true, nil, nil).OperatorFG, BG: rgb(0, 0, 0), Bold: true}
	for _, th := range firstRowThemes {
		m := fixtureModel(t, 132, 50)
		if th.bg != nil {
			m.Update(tea.BackgroundColorMsg{Color: th.bg})
		}
		m.View()
		x, y := findTarget(t, m, target{tCapability, operatorCapability})
		cl := m.last.c.cells[y][x]
		if y != 0 || cl.style != want {
			t.Errorf("%s: operator warning at row %d drawn %+v", th.name, y, cl.style)
		}
	}
}
