package spaceview

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/leogr/yoloctl/internal/config"
)

// Theme maps the view's roles to colors. Chrome follows the TUI: orange for
// brand and focus, steel blue for frames, gray for quiet text, and raised
// surfaces derived from the terminal background.
type Theme struct {
	Dark bool

	Brand, Frame, Label, Dim, Faint, Text, Bright Color
	OK, Bad                                       Color
	Warn                                          Color // explicit away
	Workdir, WorkdirHi, Port, PortHi              Color
	Account, Feature, Capability, Room, RoomHi    Color
	Builds, System                                Color

	Surface, Raised, Popup Color // derived backgrounds
	OperatorBG, OperatorFG Color

	Heat    [4]Color
	Accents []Color
	Agents  map[string]Color
}

func newTheme(dark bool, bg color.Color, icons map[string]config.AgentIcon) Theme {
	var t Theme
	if dark {
		t = Theme{
			Dark: true, Brand: ix(215), Frame: ix(67), Label: ix(110), Dim: ix(243), Faint: ix(238),
			Text: ix(252), Bright: ix(255), OK: ix(108), Bad: ix(203), Warn: ix(221),
			Workdir: ix(180), WorkdirHi: ix(223), Port: ix(116), PortHi: ix(195),
			Account: ix(147), Feature: ix(151), Capability: ix(223), Room: ix(174), RoomHi: ix(217),
			Builds: ix(137), System: ix(245),
			Surface: ix(235), Raised: ix(237), Popup: ix(236),
			OperatorBG: ix(52), OperatorFG: ix(217),
			Heat:    [4]Color{ix(74), ix(180), ix(215), ix(203)},
			Accents: []Color{ix(209), ix(111), ix(79), ix(186), ix(146), ix(175), ix(116), ix(180), ix(150), ix(217)},
		}
	} else {
		t = Theme{
			Brand: ix(166), Frame: ix(67), Label: ix(25), Dim: ix(244), Faint: ix(252),
			Text: ix(236), Bright: ix(232), OK: ix(28), Bad: ix(160), Warn: ix(136),
			Workdir: ix(130), WorkdirHi: ix(166), Port: ix(30), PortHi: ix(37),
			Account: ix(61), Feature: ix(29), Capability: ix(130), Room: ix(132), RoomHi: ix(162),
			Builds: ix(94), System: ix(246),
			Surface: ix(254), Raised: ix(253), Popup: ix(255),
			OperatorBG: ix(224), OperatorFG: ix(124),
			Heat:    [4]Color{ix(31), ix(136), ix(166), ix(160)},
			Accents: []Color{ix(166), ix(26), ix(29), ix(100), ix(61), ix(125), ix(30), ix(94), ix(64), ix(168)},
		}
	}
	if bg != nil {
		// Same derivation as the TUI chrome: ~15% toward the opposite end.
		t.Surface = nudge(bg, dark, 38)
		t.Popup = nudge(bg, dark, 26)
		t.Raised = nudge(bg, dark, 48)
	}
	t.Agents = map[string]Color{}
	for name, icon := range icons {
		hex := icon.Dark
		if !dark {
			hex = icon.Light
		}
		if c, ok := parseHex(hex); ok {
			t.Agents[name] = c
		}
	}
	return t
}

// FirstRowBG is the background of a native view's whole first row: explicit
// RGB black in every theme. In the TUI that row lies directly under the tab
// strip, so the view continues its active tab instead of opening on a
// lighter surface.
var FirstRowBG = rgb(0, 0, 0)

// FirstRow is the palette for text on FirstRowBG. Light themes take the dark
// palette's foregrounds there, which stay readable on black; its derived
// backgrounds and agent colors are not meant for that row.
func (t Theme) FirstRow() Theme {
	if t.Dark {
		return t
	}
	return newTheme(true, nil, nil)
}

// An active dot breathes once per PulseCycle. PulseFrame is the repaint
// cadence that animates it: space view's steady tick, and the chat panel's
// while a participant is active. Every view draws activity through
// ActiveTone, so their dots breathe identically.
const (
	PulseCycle = 2 * time.Second
	PulseFrame = 160 * time.Millisecond
	// pulseLift is how far toward white the breath's peak goes: a clearly
	// visible brightening that keeps the hue, never a blink to another
	// color. Past about 0.6 a light theme's dot fades into its background.
	pulseLift = 0.55
)

// ActiveTone is the color of a dot whose box shows observed activity, over
// its base color. The dot keeps its hue and breathes smoothly from its own
// color to a brighter tone and back once per PulseCycle, with no off phase;
// with motion off it holds that brighter tone. The phase is a function of
// the wall time alone, so every dot and view agrees at the same instant.
func (t Theme) ActiveTone(base Color, motion bool, now time.Time) Color {
	level := 1.0
	if motion {
		cycle := PulseCycle.Milliseconds()
		phase := float64(now.UnixMilli()%cycle) / float64(cycle)
		level = (1 - math.Cos(2*math.Pi*phase)) / 2
	}
	return lift(base, pulseLift*level)
}

// lift mixes a color toward white by f, which keeps its hue. Colors whose
// value the terminal defines (the default and the 16 system colors) are
// returned unchanged.
func lift(c Color, f float64) Color {
	r, g, b, ok := c.rgb()
	if !ok || f <= 0 {
		return c
	}
	up := func(v uint8) uint8 { return uint8(math.Round(float64(v) + (255-float64(v))*f)) }
	return rgb(up(r), up(g), up(b))
}

// rgb is a color's value: truecolor as is, and the 256-color cube and gray
// ramp by their standard xterm levels.
func (c Color) rgb() (r, g, b uint8, ok bool) {
	switch n := int(c) - 1; {
	case c&trueColor != 0:
		v := int(c &^ trueColor)
		return uint8(v >> 16), uint8(v >> 8), uint8(v), true
	case n >= 16 && n < 232:
		level := func(i int) uint8 {
			if i == 0 {
				return 0
			}
			return uint8(55 + 40*i)
		}
		n -= 16
		return level(n / 36), level(n / 6 % 6), level(n % 6), true
	case n >= 232 && n < 256:
		v := uint8(8 + 10*(n-232))
		return v, v, v, true
	}
	return 0, 0, 0, false
}

func nudge(c color.Color, dark bool, delta int) Color {
	r, g, b, _ := c.RGBA()
	step := func(v uint32) uint8 {
		n := int(v >> 8)
		if dark {
			return uint8(min(n+delta, 255))
		}
		return uint8(max(n-delta, 0))
	}
	return rgb(step(r), step(g), step(b))
}

func parseHex(s string) (Color, bool) {
	var r, g, b uint8
	if len(s) != 7 || s[0] != '#' {
		return 0, false
	}
	if n, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b); n != 3 || err != nil {
		return 0, false
	}
	return rgb(r, g, b), true
}

// heat colors a fill level so fuller gauges read hotter.
func (t Theme) heat(p float64) Color {
	switch {
	case p < 0.40:
		return t.Heat[0]
	case p < 0.65:
		return t.Heat[1]
	case p < 0.85:
		return t.Heat[2]
	default:
		return t.Heat[3]
	}
}

func (t Theme) agent(name string) Color {
	if c, ok := t.Agents[name]; ok {
		return c
	}
	return t.Label
}
