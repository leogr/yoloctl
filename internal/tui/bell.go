package tui

import (
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// Leave sound/visual/disabled bell policy to the outer terminal, as for a
// native CLI. Send one bounded PTY batch through the serialized host writer;
// frame redraws and tab selection do not emit bells.
func (m *model) handleBellOutput(count int) {
	if m.program != nil && count > 0 {
		m.program.Send(tea.RawMsg{Msg: strings.Repeat("\a", count)})
	}
}

// paneBellMsg names the session whose child rang a bell.
type paneBellMsg struct{ from *term.Session }

// paneBellHandler forwards a pane's bells live to the host, as every bell
// is, and reports which pane rang so an unfocused one can be marked.
func (m *model) paneBellHandler(self *atomic.Pointer[term.Session]) func(int) {
	return func(count int) {
		m.handleBellOutput(count)
		if m.program != nil && count > 0 {
			m.program.Send(paneBellMsg{from: self.Load()})
		}
	}
}

// markUnfocused gives attention to a pane that is not the focused pane of
// the current tab (or while the palette has focus): the pane is marked on
// its border until focused, and its tab is badged when not current, as a
// tab was before panes.
func (m *model) markUnfocused(t *tabs.Tab, p *panes.Pane) {
	current := m.tabs.Current() == t
	if !current || m.focusPalette {
		t.Badge = true
	}
	if p != nil && (!current || m.focusPalette || p != t.Focused()) {
		p.Mark = true
	}
}
