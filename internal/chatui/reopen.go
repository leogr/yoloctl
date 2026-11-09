package chatui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Reopen is explicit and uses the existing stream owner. A fresh model is
// initially connecting, not a failed connection; telemetry loss is separate.
func (m *Model) reopenOffered() bool {
	return m.connectionEnded && !m.connected && !m.reconnecting && !m.deleted && m.opts.Reconnect != nil
}

func (m *Model) reopen() tea.Cmd {
	if !m.reopenOffered() || m.overlay != nil || m.linkPrompt != nil || m.pasting || m.width < 20 || m.height < 7 {
		return nil
	}
	// connection has already marked any in-flight delivery uncertain. Keep
	// that marker and the draft in this model: reconnect never replays it.
	m.reconnecting = true
	m.focus = focusComposer
	m.say(levelInfo, "Reconnecting… drafts and uncertain deliveries are kept")
	return m.opts.Reconnect()
}

func (m *Model) reopenKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if !m.reopenOffered() || m.width < 20 || m.height < 7 || m.suggest != nil {
		return nil, false
	}
	// Committed text may accompany a control-looking code. Only the
	// physical r matching its text is a shortcut in this focused context.
	if k.Text != "" && (k.Code != 'r' || !strings.EqualFold(k.Text, "r")) {
		return nil, false
	}
	key := strings.ToLower(k.Keystroke())
	if key == "shift+tab" && k.Code == tea.KeyTab {
		if m.focus == focusReopen {
			m.focus = focusComposer
		} else {
			m.focus = focusReopen
		}
		return nil, true
	}
	if m.focus != focusReopen {
		return nil, false
	}
	switch {
	case k.Code == tea.KeyEnter && k.Mod == 0,
		k.Code == 'r' && k.Mod == 0 && (k.Text == "" || strings.EqualFold(k.Text, "r")):
		if k.IsRepeat {
			return nil, true
		}
		return m.reopen(), true
	case key == "tab" || key == "esc":
		m.focus = focusComposer
		return nil, true
	}
	return nil, false
}

func (m *Model) reopenFooter(w, y int) string {
	t := m.theme
	label := "[ Reopen ]"
	style := spaceview.Style{FG: t.Text, BG: t.Raised, Bold: true}
	hint := " Shift+Tab to focus"
	if m.focus == focusReopen {
		style.FG, style.BG = t.Bright, t.Frame
		hint = " r/Enter · Tab/Esc draft"
	}
	m.hits = append(m.hits, hit{x0: 1, x1: 1 + len(label), y: y, area: hitFooter, id: "reopen"})
	runs := []run{deco(" ", spaceview.Style{})}
	runs = append(runs, hoverRuns([]run{deco(label, style)}, m.hovered(hitFooter, 0, "reopen"))...)
	runs = append(runs, deco(hint, spaceview.Style{FG: t.Dim}))

	return paint(m.widthMethod, runs, w, 0, nil, spaceview.Style{})
}
