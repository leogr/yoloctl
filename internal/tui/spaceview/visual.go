package spaceview

import (
	"image/color"

	"github.com/leogr/yoloctl/internal/config"
)

// ChatIcon identifies chat rooms across native views.
const ChatIcon = "◈"

// NewTheme shares the dashboard's palette and background derivation with other
// native views without exposing its telemetry model or layout internals.
func NewTheme(dark bool, background color.Color, icons map[string]config.AgentIcon) Theme {
	return newTheme(dark, background, icons)
}

// StyleText renders plain text with the same cell styles as the dashboard.
// Callers must neutralize untrusted terminal controls before supplying text.
func StyleText(text string, style Style) string {
	return style.sgr() + text + "\x1b[0m"
}

// ColorKind says how a Color is encoded.
type ColorKind int

// Color kinds.
const (
	ColorDefault ColorKind = iota // the terminal's default
	ColorIndexed                  // a 256-color palette index
	ColorTrue                     // a truecolor value
)

// Spec exposes a color's encoding to renderers outside this package, such as
// the shared log palette: its 256-color index or its RGB value.
func (c Color) Spec() (index int, r, g, b uint8, kind ColorKind) {
	switch {
	case c == 0:
		return 0, 0, 0, 0, ColorDefault
	case c&trueColor != 0:
		v := int(c &^ trueColor)
		return 0, uint8(v >> 16), uint8(v >> 8), uint8(v), ColorTrue
	}
	return int(c) - 1, 0, 0, 0, ColorIndexed
}

// RGB names one truecolor value, for native views that must draw an exact
// colour (an image placeholder's ID).
func RGB(r, g, b uint8) Color { return rgb(r, g, b) }

// PaletteColor names one 256-color palette index, for native views that
// need a shade the dashboard theme does not define.
func PaletteColor(index int) Color { return ix(index) }
