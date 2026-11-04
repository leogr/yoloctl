package chatui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// linkPrompt snapshots the validated destination; selection and incoming
// history cannot change the host action being confirmed.
type linkPrompt struct {
	url    string
	scroll int
}

func (m *Model) linkPromptKey(k tea.KeyPressMsg) tea.Cmd {
	// Bracketed paste, committed text and key auto-repeat cannot approve or
	// dismiss the modal. All other input is kept away from the hidden draft.
	if m.pasting || k.Text != "" || k.IsRepeat {
		return nil
	}
	switch strings.ToLower(k.Keystroke()) {
	case "esc", "ctrl+c":
		m.linkPrompt = nil
		m.say(m.noticeLevel, "Browser opening canceled")
	case "ctrl+o":
		if m.width < 20 || m.height < 7 {
			return nil // the destination is hidden behind the resize notice
		}
		url := m.linkPrompt.url
		m.linkPrompt = nil
		return m.opts.OpenLink(url)
	case "up", "pgup":
		m.linkPrompt.scroll = max(m.linkPrompt.scroll-1, 0)
	case "down", "pgdown":
		m.linkPrompt.scroll++
	}
	return nil
}

func (m *Model) linkPromptView() string {
	w, h := m.width, m.height
	inner, bodyRows := w-4, h-4
	urlRows := strings.Split(m.widthMethod.Hardwrap(m.linkPrompt.url, inner, true), "\n")
	m.linkPrompt.scroll = min(m.linkPrompt.scroll, max(len(urlRows)-bodyRows, 0))
	frame := func(s string) string { return style(s, m.theme.Frame, false) }
	row := func(s string) string { return frame("│ ") + fit(m.widthMethod, s, inner) + frame(" │") }
	// The border is the view's first row, so it sits on the same black.
	top := spaceview.StyleText("╭"+strings.Repeat("─", w-2)+"╮", spaceview.Style{FG: m.theme.FirstRow().Frame, BG: spaceview.FirstRowBG})
	lines := []string{top, row(style("Open host browser?", m.theme.Brand, true))}
	for i := range bodyRows {
		line := ""
		if n := m.linkPrompt.scroll + i; n < len(urlRows) {
			line = urlRows[n]
		}
		lines = append(lines, row(style(line, m.theme.Text, false)))
	}
	footer := "Ctrl+O Open · Esc Cancel"
	if inner < m.widthMethod.StringWidth(footer) {
		footer = "^O Open Esc No"
	}
	if len(urlRows) > bodyRows {
		if inner >= m.widthMethod.StringWidth("↑/↓ scroll · "+footer) {
			footer = "↑/↓ scroll · " + footer
		}
	}
	lines = append(lines, row(style(footer, m.theme.Brand, true)), frame("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(lines, "\n")
}
