package prettylog

import (
	"errors"
	"image/color"
	"io"
	"strconv"
	"strings"
)

// Color is a foreground colour. The zero value is the terminal's default
// foreground.
type Color uint32

const (
	colorIndex Color = 1 << 24
	colorRGB   Color = 2 << 24
	colorKind  Color = 0xff << 24
)

// Faint is the default foreground drawn with SGR 2: a dim tone that stays
// readable on light and dark backgrounds.
const Faint Color = 3 << 24

// Index is a 256-colour palette index. Indices 0–15 are the terminal's own
// themed colours.
func Index(n uint8) Color { return colorIndex | Color(n) }

// RGB is a truecolor value.
func RGB(r, g, b uint8) Color { return colorRGB | Color(r)<<16 | Color(g)<<8 | Color(b) }

// Palette maps presentation roles to colours. The caller supplies it, so the
// package carries no theme of its own beyond ANSIPalette.
type Palette struct{ Error, Warn, Notice, Info, OK, Debug, Actor, Text, Dim Color }

// ANSIPalette uses the terminal's own 16 colours and Faint. It is readable on
// any background, so it is the palette to use until the background is known.
var ANSIPalette = Palette{
	Error: Index(1), Warn: Index(3), Notice: Index(6), Info: Index(6), OK: Index(2),
	Debug: Faint, Actor: Index(5), Text: 0, Dim: Faint,
}

// sgr returns the escape sequence that selects c (and bold), or "" when the
// terminal defaults apply.
func sgr(c Color, bold bool) string {
	var parts []string
	if bold {
		parts = append(parts, "1")
	}
	switch c & colorKind {
	case colorIndex:
		parts = append(parts, "38;5;"+strconv.Itoa(int(c&0xff)))
	case colorRGB:
		v := int(c &^ colorKind)
		parts = append(parts, "38;2;"+strconv.Itoa(v>>16&0xff)+";"+strconv.Itoa(v>>8&0xff)+";"+strconv.Itoa(v&0xff))
	case Faint:
		parts = append(parts, "2")
	}
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

const sgrReset = "\x1b[0m"

// ColorMode is the --color policy.
type ColorMode int

// Colour modes.
const (
	ColorAuto ColorMode = iota
	ColorAlways
	ColorNever
)

// ParseColorMode reads "auto", "always" or "never".
func ParseColorMode(s string) (ColorMode, error) {
	switch s {
	case "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	}
	return ColorAuto, errors.New(`colour mode must be "auto", "always" or "never"`)
}

// String returns the flag spelling of m.
func (m ColorMode) String() string {
	switch m {
	case ColorAlways:
		return "always"
	case ColorNever:
		return "never"
	}
	return "auto"
}

// DecideColor chooses the format and whether to colour, once, for the final
// writer: a terminal gets the pretty format and a pipe or file the plain one;
// colour is on for a terminal under auto unless NO_COLOR is non-empty or
// TERM=dumb, always on with ColorAlways (plain output too) and never on with
// ColorNever. isTerminal is injected so the package reads no terminal itself.
func DecideColor(w io.Writer, mode ColorMode, getenv func(string) string, isTerminal func(io.Writer) bool) (Format, bool) {
	tty := isTerminal != nil && isTerminal(w)
	format := FormatPlain
	if tty {
		format = FormatPretty
	}
	switch mode {
	case ColorAlways:
		return format, true
	case ColorNever:
		return format, false
	}
	if !tty || getenv == nil {
		return format, false
	}
	return format, getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// Background is what an input owner learned about the terminal background.
type Background struct {
	Known, Dark bool
	Color       color.Color
}

// BackgroundFrom classifies a reported background colour: HSL lightness
// below one half is dark. A nil colour is unknown.
func BackgroundFrom(c color.Color) Background {
	if c == nil {
		return Background{}
	}
	r, g, b, _ := c.RGBA()
	hi, lo := max(r, g, b), min(r, g, b)
	lightness := float64(hi+lo) / 2 / 0xffff
	return Background{Known: true, Dark: lightness < 0.5, Color: c}
}
