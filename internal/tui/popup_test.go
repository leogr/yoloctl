package tui

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestPopupWrapAndBounds(t *testing.T) {
	p := popup{
		Title:  "A 界e\u0301 title\nwith a second line",
		Lines:  []string{strings.Repeat("界e\u0301", 100)},
		Footer: "Ctrl+O Open     Esc Cancel",
	}
	for _, width := range []int{0, 1, 4, 5, 8, 24, 80} {
		for _, rows := range []int{0, 1, 2, 3, 5, 8} {
			t.Run(fmt.Sprintf("%dx%d", width, rows), func(t *testing.T) {
				lines := p.render(width, rows, "")
				if len(lines) > rows || (width == 0 && len(lines) != 0) {
					t.Fatalf("popup exceeds bounds: %q", lines)
				}
				for _, line := range lines {
					if strings.ContainsAny(line, "\r\n") || ansi.StringWidth(line) != width {
						t.Fatalf("popup row wraps outside %d columns: %q", width, line)
					}
				}
				if width >= 5 && rows >= 3 {
					buf := uv.NewScreenBuffer(width, len(lines))
					uv.NewStyledString(strings.Join(lines, "\n")).Draw(buf, buf.Bounds())
					for i, line := range lines {
						plain := ansi.Strip(line)
						left, right := "│", "│"
						if i == 0 {
							left, right = "╭", "╮"
						} else if i == len(lines)-1 {
							left, right = "╰", "╯"
						}
						if !strings.HasPrefix(plain, left) || !strings.HasSuffix(plain, right) {
							t.Fatalf("incomplete frame at row %d: %q", i, plain)
						}
						if got := buf.CellAt(width-1, i).Content; got != right {
							t.Fatalf("right border at (%d,%d) = %q, want %q", width-1, i, got, right)
						}
					}
				}
			})
		}
	}
}

// Exercise the composed view through the same styled-cell reader as the host
// renderer: blank padding and borders must carry the palette's background too.
func assertBrowserPopupSurface(t *testing.T, m *model, view string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Fatalf("dialog changed viewport height: %d, want %d", len(lines), m.height)
	}
	start := 0
	if m.standalone {
		start = m.height - 6 // title, source, URL, gap, choices, bottom border
	}
	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(view).Draw(buf, buf.Bounds())
	for y := start; y < start+6; y++ {
		plain := ansi.Strip(lines[y])
		left, right := "│", "│"
		switch y {
		case start:
			left, right = "╭", "╮"
		case start + 5:
			left, right = "╰", "╯"
		}
		if ansi.StringWidth(plain) != m.width || !strings.HasPrefix(plain, left) || !strings.HasSuffix(plain, right) {
			t.Fatalf("dialog frame row %d: %q", y, plain)
		}
		if got := buf.CellAt(m.width-1, y).Content; got != right {
			t.Fatalf("right border at row %d is %q, want %q in column %d", y, got, right, m.width-1)
		}
		for x := 0; x < m.width; x++ {
			bg := buf.CellAt(x, y).Style.Bg
			if bg == nil {
				t.Fatalf("missing popup background at (%d,%d)", x, y)
			}
			r, g, b, _ := bg.RGBA()
			if r != 0x2626 || g != 0x2626 || b != 0x2626 {
				t.Fatalf("popup background at (%d,%d) = %v, want palette background", x, y, bg)
			}
		}
	}
	if !m.standalone && m.tabStripRow() != 6 {
		t.Fatalf("dialog moved tab strip to row %d, want 6", m.tabStripRow())
	}
}
