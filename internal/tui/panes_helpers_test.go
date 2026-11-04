package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// testTab is a one-pane tab, as tabs were before panes.
func testTab(s *term.Session, command string, exited bool) *tabs.Tab {
	return tabs.New(&panes.Pane{Session: s, Command: command, Exited: exited})
}

// ctrlQ is the default pane prefix as a host decodes it.
var ctrlQ = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}

// prefixed presses the pane prefix, then k.
func prefixed(m *model, k tea.KeyPressMsg) {
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(k)
}
