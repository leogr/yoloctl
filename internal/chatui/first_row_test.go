package chatui

import (
	"image/color"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderedCell is one drawn cell: its grapheme ("" continues a wide one) and
// its SGR colors, "" being the terminal default.
type renderedCell struct{ text, fg, bg string }

// renderedCells parses one rendered row into the cells a terminal draws.
func renderedCells(row string) []renderedCell {
	var cells []renderedCell
	var fg, bg string
	for row != "" {
		if strings.HasPrefix(row, "\x1b[") {
			end := strings.IndexFunc(row[2:], func(r rune) bool { return r >= 0x40 && r <= 0x7e })
			if end < 0 {
				break
			}
			if row[2+end] == 'm' {
				fg, bg = applySGR(strings.Split(row[2:2+end], ";"), fg, bg)
			}
			row = row[3+end:]
			continue
		}
		g, w := ansi.FirstGraphemeCluster(row, ansi.WcWidth)
		row = row[len(g):]
		for i := range w {
			text := g
			if i > 0 {
				text = ""
			}
			cells = append(cells, renderedCell{text, fg, bg})
		}
	}
	return cells
}

func applySGR(p []string, fg, bg string) (string, string) {
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case "", "0":
			fg, bg = "", ""
		case "39":
			fg = ""
		case "49":
			bg = ""
		case "38", "48":
			n := 2 // 5;index
			if i+1 < len(p) && p[i+1] == "2" {
				n = 4 // 2;r;g;b
			}
			v := strings.Join(p[i+1:min(i+1+n, len(p))], ";")
			if c == "38" {
				fg = v
			} else {
				bg = v
			}
			i += n
		default:
			if len(c) == 2 && (c[0] == '3' || c[0] == '9') {
				fg = c
			} else if len(c) == 2 && c[0] == '4' || len(c) == 3 && c[:2] == "10" {
				bg = c
			}
		}
	}
	return fg, bg
}

// The first row continues the TUI's active tab: black across the whole
// width in every theme and screen, including padding, clipped wide text,
// hover, overlays, the browser prompt and the resize hint. Its text, colors
// and click targets do not depend on the terminal's theme.
func TestChatFirstRowIsBlackInEveryState(t *testing.T) {
	const black = "2;0;0;0"
	themes := []struct {
		name string
		bg   color.Color
	}{
		{"black", color.Black},
		{"slate", color.RGBA{0x30, 0x34, 0x46, 0xff}},
		{"white", color.White},
		{"cream", color.RGBA{0xfd, 0xf6, 0xe3, 0xff}},
	}
	states := []struct {
		name, space string
		set         func(*Model)
	}{
		{"live", "trial", func(*Model) {}},
		{"offline", "trial", func(m *Model) { m.Update(ConnectionMsg{}) }},
		{"connecting", "trial", func(m *Model) { m.Update(ConnectionMsg{}); m.reconnecting = true }},
		{"busy", "trial", func(m *Model) {
			m.Update(MembersMsg{Members: []Member{{Nick: "leo"}, {Nick: "sol", Away: true}}})
			other := m.view("#ops")
			other.unread, other.unreadMentions = 4, 1
			m.unread, m.unreadMentions = 3, 1
			m.source, m.topic = true, "release 界 checklist"
			m.hover = hoverKey{area: hitTop, id: "channels"}
		}},
		{"wide space", "空間空間空間空間空間空間空間空間空間空間", func(*Model) {}},
		{"participants", "trial", func(m *Model) { m.rosterOpen, m.rosterOverlay = false, true }},
		{"help", "trial", func(m *Model) { m.openHelp() }},
		{"browser prompt", "trial", func(m *Model) { m.linkPrompt = &linkPrompt{url: "https://example.com/" + strings.Repeat("a", 300)} }},
	}
	render := func(space string, bg color.Color, w, h int, set func(*Model)) (*Model, string) {
		m := New(Options{Space: space, Nick: "human"})
		if bg != nil {
			m.Update(tea.BackgroundColorMsg{Color: bg})
		}
		m.Update(ConnectionMsg{Connected: true})
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		set(m)
		return m, m.View().Content
	}
	topHits := func(m *Model) []hit {
		var out []hit
		for _, h := range m.hits {
			if h.y == 0 {
				out = append(out, h)
			}
		}
		return out
	}
	for _, st := range states {
		for _, size := range [][2]int{{20, 7}, {23, 9}, {40, 12}, {79, 24}, {80, 24}, {160, 40}, {19, 10}, {80, 6}, {5, 2}} {
			w, h := size[0], size[1]
			ref, screen := render(st.space, nil, w, h, st.set)
			row := renderedCells(strings.Split(screen, "\n")[0])
			if len(row) != w {
				t.Fatalf("%s %dx%d: first row has %d cells: %q", st.name, w, h, len(row), screen)
			}
			if w >= 20 && h >= 7 && strings.Count(screen, "\n")+1 != h {
				t.Fatalf("%s %dx%d: %d rows", st.name, w, h, strings.Count(screen, "\n")+1)
			}
			for x, c := range row {
				if c.bg != black {
					t.Fatalf("%s %dx%d: cell %d (%q) background %q, want black: %q", st.name, w, h, x, c.text, c.bg, strings.Split(screen, "\n")[0])
				}
				if c.text != "" && c.text != " " && c.fg == "" {
					t.Errorf("%s %dx%d: cell %d (%q) uses the terminal's foreground on black", st.name, w, h, x, c.text)
				}
			}
			for _, th := range themes {
				m, other := render(st.space, th.bg, w, h, st.set)
				if !slices.Equal(renderedCells(strings.Split(other, "\n")[0]), row) || !slices.Equal(topHits(m), topHits(ref)) {
					t.Errorf("%s %dx%d: first row differs on a %s terminal", st.name, w, h, th.name)
				}
			}
		}
	}
}
