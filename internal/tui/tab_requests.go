package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/leogr/yoloctl/internal/filelink"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

type tabRequestMsg struct {
	generation uint64
	req        termio.TabRequest
	chat       bool
	navigation *filelink.Context
	source     *atomic.Pointer[term.Session]
	from       *term.Session // the requesting pane's session; nil if not yet known
}

// tabRequestHandler offers the private channel only to native views. File
// context registration is immutable and launch-bound. Participant attaches
// remain guarded for chat, plain for space view; follow cannot send attaches.
// Lifecycle resume metadata is handled separately by Session.
func (m *model) tabRequestHandler(args []string, self *atomic.Pointer[term.Session]) func(termio.TabRequest) {
	chat := isChatView(args)
	view, isFollow := parseReopenView(args)
	isFollow = isFollow && view.kind == "follow"
	if !isSpaceView(args) && !chat && !isFollow {
		return nil
	}
	generation := m.spaceGeneration
	var navigation *filelink.Context
	return func(r termio.TabRequest) {
		if r.Op == "follow-context" {
			c, ok := navigationForSource(args, r)
			if navigation != nil || !ok {
				return
			}
			navigation = &c
			if m.program != nil {
				m.program.Send(tabRequestMsg{req: r, from: self.Load(), generation: generation, navigation: navigation, source: self})
			}
			return
		}
		if isFollow && r.Op != "follow" {
			return
		}

		if m.program != nil {
			m.program.Send(tabRequestMsg{req: r, chat: chat, from: self.Load(), generation: generation, navigation: navigation, source: self})
		}
	}
}

func isChatView(args []string) bool {
	return len(args) > 0 && args[0] == "chat" && chatOpensLiveSurface(args[1:])
}

func (m *model) handleTabRequest(r termio.TabRequest, chat bool, from *term.Session) {
	var home *tabs.Tab
	if from != nil {
		home, _ = m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == from })
	}
	var args []string
	switch {
	case chat:
		if r.ValidateChat() != nil {
			m.postTabNotice(home, prettylog.KindWarn, "chat sent an invalid attach request; nothing was opened")
			return
		}
		args = r.ChatAttachArgs()
	case r.Follow() || r.Chat() || r.NoAutoupdate || r.Passthrough || r.Op != "attach" || layout.ValidateExistingSpaceName(r.Space) != nil || layout.ValidateName("agentbox", r.Box) != nil:
		m.postTabNotice(home, prettylog.KindWarn, "space view sent an invalid request; nothing was opened")
		return
	default:
		args = []string{"box", "attach", r.Box, "--space", r.Space}
	}
	// The requesting pane's tab, as it is now: its empty pane takes the
	// attach. A request never moves an existing view.
	// A pane (in any tab) is reused only for the identical argv. For chat that
	// includes the identities the click just resolved afresh; any change opens
	// a new guarded attach, which validates again before it touches a session.
	command := "yoloctl " + strings.Join(args, " ")
	if t, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Command == command && !p.Exited }); p != nil {
		if home != nil && home.Layout.Empty() != nil {
			m.postTabNotice(home, prettylog.KindNotice, fmt.Sprintf("already open in tab %d", m.tabs.Index(t)+1))
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

// A local file always opens a new tab, never an empty attach placeholder.
func (m *model) handleFollowRequest(r termio.TabRequest, c *filelink.Context, from *term.Session) {
	if from == nil {
		return
	}
	home, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == from })
	if p == nil || p.Exited {
		return
	}
	select {
	case <-from.Done():
		return
	default:
	}
	t, err := r.FollowTarget()
	if err != nil || c == nil || !c.Accept(t) {
		m.postTabNotice(home, prettylog.KindWarn, "invalid local file request; nothing was opened")
		return
	}
	args := followTargetArgs(t)
	if tab, pane := m.tabs.Owner(func(p *panes.Pane) bool {
		v, ok := parseReopenView(p.Args)
		live := !p.Exited && p.Session != nil
		if live {
			select {
			case <-p.Session.Done():
				live = false
			default:
			}
		}
		return live && (slices.Equal(p.Args, args) || ok && v.kind == "follow" && v.path == t.Path && v.root == t.Root && v.space == t.Space && v.host == (t.Mode == "host"))
	}); pane != nil {
		m.activatePane(tab, pane)
		return
	}
	m.openTab(args)
}

func followTargetArgs(t filelink.Target) []string { return followSourceArgs(t, true) }
func followSourceArgs(t filelink.Target, bounded bool) []string {
	flag := "--navigation-root"
	if bounded {
		flag = "--link-root"
	}
	args := []string{"follow", flag, t.Root}
	if t.Mode == "host" {
		args = append(args, "--host")
	} else {
		args = append(args, "--space", t.Space)
	}
	return append(args, "--", t.Path)
}
func (m *model) pinFollowSource(msg tabRequestMsg) {
	if msg.from == nil || msg.navigation == nil || msg.req.FollowPath == "" {
		return
	}
	_, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == msg.from })
	if p == nil {
		return
	}
	old, ok := parseReopenView(p.Args)
	if !ok || old.kind != "follow" {
		return
	}
	validated, valid := navigationForSource(p.Args, msg.req)
	if !valid || validated != *msg.navigation {
		return
	}
	t := filelink.Target{Mode: msg.navigation.Mode, Space: msg.navigation.Space, Root: msg.navigation.Root, Path: msg.req.FollowPath}
	if !msg.navigation.Accept(t) {
		return
	}
	args := followSourceArgs(t, msg.req.FollowBounded)
	if old.format != "" && old.format != "auto" {
		args = append(args[:len(args)-2], append([]string{"--format", old.format}, args[len(args)-2:]...)...)
	}
	p.Args = args
	p.Command = "yoloctl " + strings.Join(args, " ")
}

// Navigation registration comes only from the trusted native view's private
// channel. Pin it once against its launch mode/space and inherited boundary.
func navigationForSource(args []string, r termio.TabRequest) (filelink.Context, bool) {
	c, err := r.NavigationContext()
	if err != nil {
		return c, false
	}
	v, ok := parseReopenView(args)
	if !ok || v.kind != "chat" && v.kind != "follow" || v.space != c.Space || v.host != (c.Mode == "host") {
		return c, false
	}
	if v.kind == "chat" {
		return c, c.Base == c.Root && r.FollowPath == ""
	}
	root := v.root
	if root == "" {
		root = v.navigationRoot
	}
	if r.FollowBounded != (v.root != "") || r.FollowPath == "" || root != "" && root != c.Root || filepath.IsAbs(v.path) && v.path != r.FollowPath {
		return c, false
	}
	return c, true
}
