package followview

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Body rows keep the terminal background behind and after their text, as
// chat transcript rows do: no bar as wide as a line of prose, a heading or a
// header-less pipe line. Only code cards keep their own card background.
func TestBodyRowsKeepTerminalBackground(t *testing.T) {
	doc := strings.Join([]string{
		"# Heading",
		"",
		"Prose paragraph with **strong** text.",
		"| feat-x | leogr | pipe text without a header row |",
		"| fix-y | 2026-11-10 | more pipe text |",
		"",
		"| Ticket | Result |",
		"|---|---|",
		"| one | merged |",
		"",
		"```go",
		"code line",
		"```",
		"",
		"Trailing prose.",
	}, "\n")
	for _, tc := range []struct {
		name string
		bg   color.Color // nil: no host report (default dark palette theme)
	}{
		{"default", nil},
		{"dark", color.RGBA{0x28, 0x2c, 0x34, 0xff}},
		{"light", color.RGBA{0xff, 0xff, 0xff, 0xff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := fileModel(t, doc, Markdown)
			if tc.bg != nil {
				_, cmd := m.Update(tea.BackgroundColorMsg{Color: tc.bg})
				applyWork(t, m, cmd)
			}
			_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
			applyWork(t, m, cmd)
			card := cellBackground(t, spaceview.StyleText(" ", spaceview.Style{BG: markdownview.CodeCard(m.theme)}), 0)
			rows := strings.Split(m.View().Content, "\n")
			var cardRows, textRows int
			for y, row := range rows[1 : len(rows)-1] { // title and status rows keep their own fills
				buf := uv.NewScreenBuffer(m.width, 1)
				uv.NewStyledString(row).Draw(buf, buf.Bounds())
				onCard, text := false, false
				for x := 0; x < m.width; x++ {
					cell := buf.CellAt(x, 0)
					if cell == nil {
						continue
					}
					if strings.TrimSpace(cell.Content) != "" {
						text = true
					}
					switch {
					case cell.Style.Bg == nil:
					case sameColor(cell.Style.Bg, card):
						onCard = true
					default:
						t.Fatalf("body row %d cell %d has background %v: %q", y+1, x, cell.Style.Bg, row)
					}
				}
				if onCard {
					cardRows++
				} else if text {
					textRows++
				}
			}
			if cardRows == 0 || textRows < 6 {
				t.Fatalf("fixture lost code cards or text rows: %d card rows, %d text rows", cardRows, textRows)
			}
		})
	}
}

func cellBackground(t *testing.T, styled string, x int) color.Color {
	t.Helper()
	buf := uv.NewScreenBuffer(x+1, 1)
	uv.NewStyledString(styled).Draw(buf, buf.Bounds())
	bg := buf.CellAt(x, 0).Style.Bg
	if bg == nil {
		t.Fatal("reference style has no background")
	}
	return bg
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
