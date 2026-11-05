package tui

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

type tabRequestMsg struct {
	req  termio.TabRequest
	chat bool
	from *term.Session // the requesting pane's session; nil if not yet known
}

// tabRequestHandler gives only `space view` and native `chat` tabs a request
// channel. A request names a space and a box; the TUI validates it and opens
// the same `box attach` argv a user could type in the palette. Chat tabs may
// only send guarded participant attaches, space views only plain ones.
func (m *model) tabRequestHandler(args []string, self *atomic.Pointer[term.Session]) func(termio.TabRequest) {
	chat := isChatView(args)
	if !isSpaceView(args) && !chat {
		return nil
	}
	return func(r termio.TabRequest) {
		if m.program != nil {
			m.program.Send(tabRequestMsg{req: r, chat: chat, from: self.Load()})
		}
	}
}

func isChatView(args []string) bool {
	return len(args) > 0 && args[0] == "chat" && chatOpensLiveSurface(args[1:])
}

func (m *model) handleTabRequest(r termio.TabRequest, chat bool, from *term.Session) {
	var args []string
	switch {
	case chat:
		if r.ValidateChat() != nil {
			m.status = "chat sent an invalid attach request; nothing was opened"
			return
		}
		args = r.ChatAttachArgs()
	case r.Chat() || r.Op != "attach" || layout.ValidateExistingSpaceName(r.Space) != nil || layout.ValidateName("agentbox", r.Box) != nil:
		m.status = "space view sent an invalid request; nothing was opened"
		return
	default:
		args = []string{"box", "attach", r.Box, "--space", r.Space}
	}
	// The requesting pane's tab, as it is now: its empty pane takes the
	// attach. A request never moves an existing view.
	var home *tabs.Tab
	if from != nil {
		home, _ = m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == from })
	}
	// A pane (in any tab) is reused only for the identical argv. For chat that
	// includes the identities the click just resolved afresh; any change opens
	// a new guarded attach, which validates again before it touches a session.
	command := "yoloctl " + strings.Join(args, " ")
	if t, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Command == command && !p.Exited }); p != nil {
		if home != nil && home.Layout.Empty() != nil {
			m.status = fmt.Sprintf("already open in tab %d", m.tabs.Index(t)+1)
		}
		m.activatePane(t, p)
		return
	}
	if home != nil && home.Layout.Empty() != nil {
		m.fillPlaceholder(home, home.Layout.Empty(), args)
		return
	}
	m.openTab(args)
}
