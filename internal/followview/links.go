package followview

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

type browserPrompt struct {
	url    string
	scroll int
}

func (m *Model) activateLink(a markdownview.Action) tea.Cmd {
	if a.Kind == markdownview.IntentURL {
		if m.opts.OpenLink == nil {
			m.notice = "Opening links is off (seams.browser)"
			return nil
		}
		if m.opts.ConfirmLinks {
			m.linkPrompt = &browserPrompt{url: a.Value}
			return nil
		}
		return m.opts.OpenLink(a.Value)
	}
	if m.opening {
		return nil
	}
	t, ok := m.admissions[a.Value]
	if !ok {
		return nil
	}
	m.opening = true
	gen, key := m.gen, m.snapshot.Key
	return func() tea.Msg {
		return m.jobs.run(func(ctx context.Context) tea.Msg {
			return localCheckMsg{gen: gen, key: key, target: t, err: m.opts.Navigation.Check(ctx, t)}
		})
	}
}
func (m *Model) browserKey(k tea.KeyPressMsg) tea.Cmd {
	if m.pasting || k.IsRepeat || k.Text != "" {
		return nil
	}
	switch strings.ToLower(k.Keystroke()) {
	case "esc", "ctrl+c":
		m.linkPrompt = nil
		m.notice = "Browser opening canceled"
	case "ctrl+o":
		if m.width >= 20 && m.height >= 7 {
			u := m.linkPrompt.url
			m.linkPrompt = nil
			return m.opts.OpenLink(u)
		}
	case "up", "pgup":
		m.linkPrompt.scroll = max(0, m.linkPrompt.scroll-1)
	case "down", "pgdown":
		m.linkPrompt.scroll++
	}
	return nil
}
func (m *Model) browserView() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.KeyboardEnhancements.ReportEventTypes = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = m.opts.Path
	if m.width < 20 || m.height < 7 {
		v.SetContent(markdownview.Truncate(m.method, "Resize to show browser destination · Esc cancels", m.width, "…"))
		return v
	}
	w, h := m.width, m.height
	urlRows := strings.Split(m.method.Hardwrap(m.linkPrompt.url, w-4, true), "\n")
	count := h - 4
	m.linkPrompt.scroll = min(m.linkPrompt.scroll, max(0, len(urlRows)-count))
	rows := []string{"Open host browser?"}
	for i := range count {
		line := ""
		if n := m.linkPrompt.scroll + i; n < len(urlRows) {
			line = urlRows[n]
		}
		rows = append(rows, line)
	}
	rows = append(rows, "Ctrl+O Open · Esc Cancel · ↑/↓ scroll")
	v.SetContent(strings.Join(rows, "\n"))
	return v
}
