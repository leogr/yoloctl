package tui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestPaletteBackgroundFillsConsoleRows(t *testing.T) {
	for _, output := range []string{
		"short\r\n\r\nnext",
		"\x1b[31mred\x1b[39m default\x1b[0m\r\n界e\u0301",
		"\x1b]8;;https://example.org\x1b\\link\x1b]8;;\x1b\\",
		strings.Repeat("old\r\n", 40) + "last",
	} {
		t.Run(output, func(t *testing.T) {
			m := newTestModel(t, 40, 16)
			m.cfg.PaletteBg = "#262626"
			_, _ = m.console.Write([]byte(output))
			for _, scroll := range []int{0, 1} {
				m.scrollBy(scroll)
				view := m.View().Content
				lines := strings.Split(view, "\n")
				buf := uv.NewScreenBuffer(m.width, m.height)
				uv.NewStyledString(view).Draw(buf, buf.Bounds())
				for y := 1; y < m.tabStripRow(); y++ {
					if width := ansi.StringWidth(lines[y]); width != m.width {
						t.Fatalf("overlay row %d is %d columns, want %d: %q", y, width, m.width, lines[y])
					}
					for x := 0; x < m.width; x++ {
						if x > 0 && buf.CellAt(x-1, y).Width == 2 {
							continue // continuation of a wide grapheme
						}
						c := buf.CellAt(x, y)
						if c.Style.Bg == nil {
							t.Fatalf("missing palette background at (%d,%d), row %q", x, y, lines[y])
						}
						r, g, b, _ := c.Style.Bg.RGBA()
						if r != 0x2626 || g != 0x2626 || b != 0x2626 {
							t.Fatalf("palette background at (%d,%d) = %v", x, y, c.Style.Bg)
						}
					}
				}
			}
		})
	}
}

func TestPalettePreservesExplicitCellBackground(t *testing.T) {
	m := newTestModel(t, 40, 16)
	m.cfg.PaletteBg = "#262626"
	_, _ = m.console.Write([]byte("\x1b[48;2;1;2;3mX\x1b[49m tail"))
	view := m.View().Content
	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(view).Draw(buf, buf.Bounds())
	bg := buf.CellAt(0, 1).Style.Bg
	if bg == nil {
		t.Fatal("explicit child background was removed")
	}
	r, g, b, _ := bg.RGBA()
	if r != 0x0101 || g != 0x0202 || b != 0x0303 {
		t.Fatalf("explicit child background = %v", bg)
	}
}
