// Package logtheme applies the yoloctl terminal theme to the shared log
// palette. Only programs that own terminal input (and already ask the
// terminal for its background) report it here; line-only commands never
// query and keep the terminal's own 16 colours.
package logtheme

import (
	"image/color"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Palette maps the dashboard theme's roles to log roles.
func Palette(dark bool, bg color.Color) prettylog.Palette {
	t := spaceview.NewTheme(dark, bg, nil)
	c := func(sc spaceview.Color) prettylog.Color {
		index, r, g, b, kind := sc.Spec()
		switch kind {
		case spaceview.ColorIndexed:
			return prettylog.Index(uint8(index))
		case spaceview.ColorTrue:
			return prettylog.RGB(r, g, b)
		}
		return 0
	}
	return prettylog.Palette{
		Error: c(t.Bad), Warn: c(t.Warn), Notice: c(t.Label), Info: c(t.Text), OK: c(t.OK),
		Debug: c(t.Dim), Actor: c(t.Brand), Text: c(t.Text), Dim: c(t.Dim),
	}
}

// SetBackground applies the theme for a reported terminal background (nil
// keeps the current palette).
func SetBackground(bg color.Color) {
	b := prettylog.BackgroundFrom(bg)
	if !b.Known {
		return
	}
	logging.SetPalette(Palette(b.Dark, bg))
}
