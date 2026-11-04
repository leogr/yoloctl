package terminal

import (
	"errors"
	"fmt"
	"image/color"
	"strings"
)

// SetPaletteColor changes a host default without overwriting application
// overrides. Resetting a palette entry in the child restores this default.
func (t *Terminal) SetPaletteColor(index int, value color.Color) error {
	if index < 0 || index > 255 || value == nil {
		return errors.New("invalid palette color")
	}
	r, g, b, _ := value.RGBA()
	rgb := uint64(r>>8)<<16 | uint64(g>>8)<<8 | uint64(b>>8)
	if t.call("yolo_vt_palette_color", t.ptr, uint64(index), rgb) != 0 && t.err == nil {
		t.err = errors.New("terminal palette update failed")
	}
	return t.err
}

// PaletteColor returns the effective color and whether the application changed
// it. This scalar bridge call is also safe inside a native reply callback.
func (t *Terminal) PaletteColor(index int) (color.RGBA, bool) {
	if index < 0 || index > 255 {
		return color.RGBA{}, false
	}
	v := t.call("yolo_vt_palette_get", t.ptr, uint64(index))
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, v>>24 != 0
}

func (t *Terminal) addPaletteOverride(value uint32) {
	index, r, g, b := value>>24, uint8(value>>16), uint8(value>>8), uint8(value)
	for _, attr := range []int{38, 48, 58} {
		t.palettePairs = append(t.palettePairs,
			fmt.Sprintf("\x1b[%d;5;%dm", attr, index),
			fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", attr, r, g, b))
	}
}

// Only rewrite the pinned formatter's complete, canonical indexed-color SGR
// tokens, never child VT input. Its row text has no raw controls; hyperlink
// controls/URIs are emitted and escaped by our bridge. Other style bytes and
// unmodified palette references remain intact. No host palette is mutated.
func (t *Terminal) renderPalette(line string) string {
	if len(t.palettePairs) == 0 {
		return line
	}
	if t.paletteStyle == nil {
		t.paletteStyle = strings.NewReplacer(t.palettePairs...)
	}
	return t.paletteStyle.Replace(line)
}
