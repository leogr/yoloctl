package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// A tab title that starts with a configured agent icon renders the icon
// in that agent's color only when active. Inactive icons retain the same
// foreground and underline as the rest of their tab.
func TestTabStripColorsAgentIcon(t *testing.T) {
	m := newTestModel(t, 80, 20)
	for _, title := range []string{"plain", "✳ me's box: fix"} {
		s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		defer s.Close()
		_ = s.Wait(5 * time.Second)
		m.tabs.Add(testTab(s, title, true)) // no OSC title: the command is the title
	}

	raw := m.tabStrip()
	if vis := stripSGR(raw); !strings.Contains(vis, "❶ plain") || !strings.Contains(vis, "❷ ✳  me's box: fix") {
		t.Fatalf("visible strip changed: %q", vis)
	}
	const claudeDark = "\x1b[38;2;217;119;87m" // #D97757
	if want := claudeDark + "✳" + cOrange + cBold + "  me's box"; !strings.Contains(raw, want) {
		t.Errorf("active claude tab icon not colored then restored:\n%q\nwant substring %q", raw, want)
	}
	if strings.Count(raw, "\x1b[38;2;") != 1 {
		t.Errorf("only the icon should be colored: %q", raw)
	}

	// Light background picks the light color on the active tab too.
	m.bgDetected, m.bgIsDark = true, false
	raw = m.tabStrip()
	const claudeLight = "\x1b[38;2;185;83;47m" // #B9532F
	if want := claudeLight + "✳" + cOrange + cBold + "  me's box"; !strings.Contains(raw, want) {
		t.Errorf("active claude tab icon on light bg:\n%q\nwant substring %q", raw, want)
	}
	m.tabs.Active = 0
	for _, dark := range []bool{true, false} {
		m.bgIsDark = dark
		raw = m.tabStrip()
		if strings.Contains(raw, "\x1b[38;2;") {
			t.Errorf("inactive icon recolored its underline (dark=%v): %q", dark, raw)
		}
		want := m.surfaceBg() + cBlue + cBold + cUnderline + " ❷ ✳  me's box: fix "
		if !strings.Contains(raw, want) {
			t.Errorf("inactive icon lost the tab style: %q", raw)
		}
	}
}

func TestTabIconSpacingUsesDisplayColumns(t *testing.T) {
	for _, c := range []struct {
		name, icon      string
		titleX, legacyX int
	}{
		{"agy", "⋏", 7, 7}, {"claude", "✳", 7, 7}, {"ascii", ">_", 7, 7}, {"greek", "π", 7, 7},
		{"cjk", "界", 7, 7}, {"emoji presentation", "✳\ufe0f", 7, 7},
		{"text presentation", "✳\ufe0e", 7, 7}, {"combining", "e\u0301", 7, 7},
		{"zwj", "👩‍💻", 7, 9}, {"multi-character", "DBG", 8, 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t, 80, 20)
			m.cfg.AgentIcons = map[string]config.AgentIcon{"fixture": {Icon: c.icon, Dark: "#010203"}}
			addExitedTab(t, m, c.icon+" TITLE")
			addExitedTab(t, m, "next")
			for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
				titleX := c.legacyX
				if method == ansi.GraphemeWidth {
					_, _ = m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
					titleX = c.titleX
				}
				for _, gap := range []string{" ", ""} {
					m.tabs.Tabs[0].Focused().Command = c.icon + gap + "TITLE"
					for _, active := range []int{0, 1} {
						m.tabs.Active = active
						for _, width := range []int{80, 16} {
							m.width = width
							checkIconStrip(t, m, method, titleX)
						}
					}
				}
			}
		})
	}
}

func checkIconStrip(t *testing.T, m *model, method ansi.Method, titleX int) {
	t.Helper()
	raw := m.tabStrip()
	if got := method.StringWidth(raw); got != m.width {
		t.Fatalf("strip width = %d, want %d: %q", got, m.width, raw)
	}
	buf := uv.NewScreenBuffer(m.width, 1)
	buf.Method = method
	uv.NewStyledString(raw).Draw(buf, buf.Bounds())
	segs, _, _, x := m.tabStripLayout()
	for _, seg := range segs {
		if got := 2 + method.StringWidth(seg.label) + tabCloseW; got != seg.width {
			t.Fatalf("rendered segment width = %d, geometry = %d", got, seg.width)
		}
		for col := x; col < x+seg.width; col++ {
			if got := m.tabAt(col); got != seg.index {
				t.Fatalf("column %d selects tab %d, want %d", col, got, seg.index)
			}
		}
		if close := x + seg.width - 2; m.tabCloseAt(close) != seg.index || buf.CellAt(close, 0).Content != "✕" {
			t.Fatalf("the ✕ of tab %d at column %d: %q", seg.index, close, buf.CellAt(close, 0).Content)
		}
		if m.width == 80 && seg.index == 0 {
			if sep, title := buf.CellAt(titleX-1, 0), buf.CellAt(titleX, 0); sep.Content != " " || title.Content != "T" {
				t.Fatalf("icon spacing at %d: separator %q, title %q", titleX, sep.Content, title.Content)
			}
		}
		x += seg.width
	}
}

func TestTabWidthNegotiation(t *testing.T) {
	for _, c := range []struct {
		name  string
		value ansi.ModeSetting
		want  ansi.Method
	}{
		{"unsupported", 0, ansi.WcWidth}, {"set", 1, ansi.GraphemeWidth},
		{"reset", 2, ansi.GraphemeWidth}, {"permanently set", 3, ansi.GraphemeWidth},
		{"permanently reset", 4, ansi.WcWidth},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t, 80, 20)
			if m.tabWidthMethod() != ansi.WcWidth {
				t.Fatal("initial width differs from Bubble Tea")
			}
			_, _ = m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: c.value})
			if m.tabWidthMethod() != c.want {
				t.Fatalf("width method = %v, want %v", m.tabWidthMethod(), c.want)
			}
		})
	}
}

func TestTabIconDoesNotSplitGrapheme(t *testing.T) {
	for _, c := range []struct{ icon, title string }{
		{"✳", "✳\ufe0f TITLE"}, {"e", "e\u0301 TITLE"}, {"👩", "👩‍💻 TITLE"},
	} {
		icons := map[string]config.AgentIcon{"fixture": {Icon: c.icon, Dark: "#010203"}}
		if got := tabIconTitle(c.title, icons, ansi.GraphemeWidth); got != c.title {
			t.Fatalf("split grapheme with padding: %q", got)
		}
		label := "❶ " + c.title
		if got := (&model{}).tabIconLabel(label, cOrange, icons); got != label {
			t.Fatalf("split grapheme with color: %q", got)
		}
	}
}

func TestTabTextTruncationKeepsClusters(t *testing.T) {
	for _, c := range []struct {
		text   string
		width  int
		method ansi.Method
		want   string
	}{
		{"👩‍💻", 3, ansi.WcWidth, "…"},
		{"👩‍💻x", 3, ansi.GraphemeWidth, "👩‍💻x"},
		{"✳\ufe0fabc", 3, ansi.GraphemeWidth, "✳\ufe0f…"},
		{"e\u0301abc", 2, ansi.GraphemeWidth, "e\u0301…"},
	} {
		if got := truncateTabText(c.text, c.width, c.method); got != c.want {
			t.Fatalf("truncate %q: %q, want %q", c.text, got, c.want)
		}
	}
}

// A shared glyph must not change color as Go changes map iteration order.
// Longest glyph wins; equal glyphs use the first agent name alphabetically.
func TestTabIconLabelStableSharedGlyph(t *testing.T) {
	icons := map[string]config.AgentIcon{
		"claude":   {Icon: "*", Dark: "#D97757", Light: "#B9532F"},
		"my-agent": {Icon: "*", Dark: "#7B83FF", Light: "#4450E0"},
		"z-agent":  {Icon: "**", Dark: "#010203", Light: "#040506"},
	}
	for _, c := range []struct {
		name, glyph, color string
		light              bool
	}{
		{"shared dark", "*", "\x1b[38;2;217;119;87m", false},
		{"shared light", "*", "\x1b[38;2;185;83;47m", true},
		{"longer dark", "**", "\x1b[38;2;1;2;3m", false},
		{"longer light", "**", "\x1b[38;2;4;5;6m", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &model{bgDetected: true, bgIsDark: !c.light}
			label := "❶ " + c.glyph + " me's box: task"
			want := "❶ " + c.color + c.glyph + cOrange + " me's box: task"
			for i := 0; i < 100; i++ {
				if got := m.tabIconLabel(label, cOrange, icons); got != want {
					t.Fatalf("render %d = %q, want %q", i, got, want)
				}
			}
		})
	}
}
