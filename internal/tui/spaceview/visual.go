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

// PaletteColor names one 256-color palette index, for native views that
// need a shade the dashboard theme does not define.
func PaletteColor(index int) Color { return ix(index) }
