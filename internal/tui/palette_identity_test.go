package tui

import (
	"bytes"
	"fmt"
	"image/color"
	"testing"

	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Run the outer renderer, not just our VT formatter. Its diff must distinguish
// palette references from each other and from RGB, regardless of the library's
// built-in palette. The host may assign completely different colors to them.
func TestHostRendererPreservesPaletteIdentity(t *testing.T) {
	for _, field := range []string{"foreground", "background", "underline"} {
		for _, pair := range [][2]color.Color{
			{ansi.IndexedColor(0), ansi.IndexedColor(16)},
			{ansi.IndexedColor(9), ansi.IndexedColor(196)},
			{ansi.IndexedColor(0), color.Black},
			{color.Black, ansi.IndexedColor(0)},
		} {
			t.Run(fmt.Sprintf("%s/%T-%v-to-%T-%v", field, pair[0], pair[0], pair[1], pair[1]), func(t *testing.T) {
				var out bytes.Buffer
				renderer := uv.NewTerminalRenderer(&out, []string{"TERM=xterm-256color"})
				renderer.SetColorProfile(colorprofile.TrueColor)
				buf := uv.NewRenderBuffer(4, 1)
				for x, c := range pair {
					style := uv.Style{Underline: uv.UnderlineSingle}
					switch field {
					case "foreground":
						style.Fg = c
					case "background":
						style.Bg = c
					case "underline":
						style.UnderlineColor = c
					}
					buf.SetCell(x, 0, &uv.Cell{Content: string(rune('A' + x)), Width: 1, Style: style})
				}
				renderer.Render(buf)
				if err := renderer.Flush(); err != nil {
					t.Fatal(err)
				}
				// Decode the emitted SGR without the buggy renderer's RGBA-only
				// equality helper. These two cells must retain their identities.
				captured := uv.NewScreenBuffer(4, 1)
				uv.NewStyledString(out.String()).Draw(captured, captured.Bounds())
				for x, want := range pair {
					style := captured.CellAt(x, 0).Style
					got := map[string]color.Color{"foreground": style.Fg, "background": style.Bg, "underline": style.UnderlineColor}[field]
					if !sameColorIdentity(got, want) {
						t.Fatalf("cell %d lost color identity: got %T %v, want %T %v; output %q", x, got, got, want, want, out.String())
					}
				}
				// A color-only update of otherwise identical text must also
				// repaint, rather than disappear in the frame comparison.
				out.Reset()
				changed := buf.CellAt(0, 0).Clone()
				changed.Style = buf.CellAt(1, 0).Style
				buf.SetCell(0, 0, changed)
				renderer.Render(buf)
				if err := renderer.Flush(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(out.Bytes(), []byte("A")) {
					t.Fatalf("color-only update was not painted: %q", out.String())
				}
			})
		}
	}
}

func sameColorIdentity(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	index := func(c color.Color) (uint8, bool) {
		switch c := c.(type) {
		case ansi.BasicColor:
			return uint8(c), true
		case ansi.IndexedColor:
			return uint8(c), true
		default:
			return 0, false
		}
	}
	ai, ap := index(a)
	bi, bp := index(b)
	if ap || bp {
		return ap && bp && ai == bi
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
