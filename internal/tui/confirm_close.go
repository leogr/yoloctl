package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Closing a pane or tab ends its local client. Only an established
// holder-backed view (box attach, root attach, or a lifecycle command once
// its managed-view handshake is established) is known to leave its session
// running; `space view` and `chat` have nothing else to lose. Every other
// live command's view may end its command, so closing it asks first.

// closeEffect is what closing a running pane may end, or "" when closing it
// is instant. It reads the pane's launch argv and its managed-view
// provenance, never its title; a live route not named here asks by default.
func closeEffect(p *panes.Pane) string {
	if p.Session == nil || p.Exited || p.Session.HolderView() {
		return ""
	}
	args := strings.Fields(strings.TrimPrefix(p.Command, "yoloctl "))
	if len(args) == 0 {
		return unknownOutcome
	}
	switch {
	case isSpaceView(args), args[0] == "chat" && chatOpensLiveSurface(args[1:]):
		return ""
	case len(args) > 1 && (args[0] == "box" && args[1] == "edit" || args[0] == "account" && args[1] == "edit"),
		len(args) > 1 && (args[0] == "space" || args[0] == "sp") && args[1] == "edit",
		(args[0] == "agent" || args[0] == "feature" || args[0] == "capability") && catalogOpensLiveSurface(args[1:]):
		return "unsaved changes are lost"
	case args[0] == "follow", len(args) > 1 && args[1] == "logs" && hasFollow(args[2:]):
		return "the follow ends"
	case len(args) > 1 && args[0] == "box" && args[1] == "use",
		len(args) > 1 && args[0] == "account" && args[1] == "secret":
		return "the input ends"
	}
	return unknownOutcome
}

const unknownOutcome = "its client ends; the remote side's outcome is unknown"

// asking lists the running panes whose close asks, in tree order.
func asking(ps []*panes.Pane) []*panes.Pane {
	var out []*panes.Pane
	for _, p := range ps {
		if closeEffect(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// closeConfirm is the open popup: bound to the pane or tab being closed and
// to the running commands it listed. It never proceeds on its own.
type closeConfirm struct {
	space         string // nonempty: close all tabs and change this TUI default
	all           []switchPane
	tab           *tabs.Tab
	pane          *panes.Pane // nil when the whole tab closes
	current       *tabs.Tab   // the tab shown when it opened
	listed        []*panes.Pane
	exited        map[*panes.Pane]bool
	close         bool // Close is highlighted (Keep is the default)
	scroll        int
	notice        string    // a refusal, shown above the buttons
	noticeUntil   time.Time // at least noticeTime
	x, y          int       // where it was last placed: its notice stays there
	returnPalette bool
}

// requestDetachPane closes a pane at once, or asks first when it runs a
// command that closing may end.
func (m *model) requestDetachPane(t *tabs.Tab, p *panes.Pane) {
	if closeEffect(p) == "" {
		m.detachPane(t, p)
		return
	}
	m.openConfirm(&closeConfirm{tab: t, pane: p, listed: []*panes.Pane{p}})
}

// requestDetachTab closes a whole tab at once, or asks first, in one popup
// listing every running command that closing may end.
func (m *model) requestDetachTab(t *tabs.Tab) {
	listed := asking(t.Layout.Panes())
	if len(listed) == 0 {
		m.detachTab(t)
		return
	}
	m.openConfirm(&closeConfirm{tab: t, listed: listed})
}

func (m *model) openConfirm(c *closeConfirm) {
	if m.browserDialog != nil {
		return // a browser prompt is modal; the close is not taken
	}
	c.current, c.returnPalette, c.exited = m.tabs.Current(), m.focusPalette, map[*panes.Pane]bool{}
	m.confirm = c // drawn in the pane overlay; focus stays where it was
}

// cancelConfirm closes the popup keeping everything (Keep).
func (m *model) cancelConfirm() {
	if c := m.confirm; c != nil {
		m.focusPalette = c.returnPalette
		m.confirm = nil
	}
}

// acceptConfirm closes what the popup was opened for, after revalidating
// it: a running command added, moved in or replaced since it opened
// refreshes the list instead of closing anything unlisted.
func (m *model) acceptConfirm() {
	c := m.confirm
	if c.space != "" {
		m.acceptSpaceSwitch()
		return
	}
	var now []*panes.Pane
	if c.pane != nil {
		now = asking([]*panes.Pane{c.pane})
	} else {
		now = asking(c.tab.Layout.Panes())
	}
	for _, p := range now {
		if !slices.Contains(c.listed, p) {
			c.listed, c.close, c.scroll = now, false, 0
			m.paneNotice("the running commands changed: review the list again")
			return
		}
	}
	m.cancelConfirm()
	if c.pane != nil {
		m.detachPane(c.tab, c.pane)
	} else {
		m.detachTab(c.tab)
	}
}

// checkConfirm cancels the popup when its object is gone or the tab
// switched, and keeps Keep highlighted when a listed command exits.
func (m *model) checkConfirm() {
	c := m.confirm
	if c == nil {
		return
	}
	if c.space == "" && (m.tabs.Index(c.tab) < 0 || m.tabs.Current() != c.current || c.pane != nil && !hasPane(c.tab, c.pane)) {
		m.cancelConfirm()
		m.ownerClosed(c.notice, c.noticeUntil, c.x, c.y) // a live notice outlives it, inert
		return
	}
	for _, p := range c.listed {
		if p.Exited && !c.exited[p] {
			c.exited[p], c.close = true, false
		}
	}
}

// confirmKey handles every key while the popup is open; none reaches a
// child, the palette or a tab shortcut.
func (m *model) confirmKey(k tea.Key) {
	c := m.confirm
	if k.IsRepeat && k.Code != tea.KeyUp && k.Code != tea.KeyDown {
		return
	}
	switch mod := k.Mod & ^(tea.ModCapsLock | tea.ModNumLock); {
	case k.Code == tea.KeyEnter && mod == 0:
		if c.close {
			m.acceptConfirm()
		} else {
			m.cancelConfirm()
		}
	case k.Code == tea.KeyLeft || k.Code == tea.KeyRight || k.Code == tea.KeyTab:
		c.close = !c.close
	case k.Code == tea.KeyUp:
		c.scroll = max(c.scroll-1, 0)
	case k.Code == tea.KeyDown:
		o := m.confirmOverlay()
		_, _, visible := o.frame.layout(o.w, o.h)
		c.scroll = min(c.scroll+1, max(len(m.confirmBody(o.inner()))-visible, 0))
	case k.Text == "y":
		m.acceptConfirm()
	case k.Text == "n", k.Code == tea.KeyEscape && mod == 0:
		m.cancelConfirm()
	}
}

const (
	confirmKeep  = "[ Keep ]"
	confirmClose = "[ Close ]"
)

// confirmEntries are the popup's list: one entry per listed command, its
// immutable launch command (never a title the child sets) and what closing
// ends.
func (m *model) confirmEntries() []string {
	var entries []string
	if m.confirm.space != "" {
		entries = m.switchSummary()
	}
	for _, p := range m.confirm.listed {
		effect := closeEffect(p)
		if p.Exited {
			effect = "exited"
		}
		command := strings.Join(strings.Fields(ansi.Strip(strings.TrimPrefix(p.Command, "yoloctl "))), " ")
		entries = append(entries, command+" · "+effect)
	}
	return entries
}

// confirmBody is the list as display rows wrapped to width, so scrolling
// reaches every row of a long entry.
func (m *model) confirmBody(width int) []string {
	var rows []string
	for _, e := range m.confirmEntries() {
		rows = append(rows, strings.Split(ansi.Hardwrap(e, max(width, 1), true), "\n")...)
	}
	return rows
}

// confirmOverlay is the open popup in the pane overlay, at the pane being
// closed (the focused pane for a whole tab): the list, scrolled by row,
// then a refusal while it shows, then the buttons, which stay visible, the
// highlighted one in reverse video.
func (m *model) confirmOverlay() *overlay {
	c := m.confirm
	if c == nil {
		return nil
	}
	title := "Close pane?"
	if c.pane == nil {
		title = "Close tab?"
	}
	if c.space != "" {
		title = "Switch from " + m.ctxSpace + " to " + c.space + " and close all tabs?"
	}
	selected := confirmKeep
	if c.close {
		selected = m.confirmCloseLabel()
	}
	footer := confirmKeep + "   " + m.confirmCloseLabel()
	if n := liveNotice(c.notice, c.noticeUntil); n != "" {
		footer = m.noticeLine(prettylog.KindWarn, n) + "\n" + footer
	}
	frame := popup{Title: title, Footer: footer, Selected: selected}
	if liveNotice(c.notice, c.noticeUntil) != "" {
		frame.FooterStyle = m.noticeFooter(prettylog.KindWarn)
	}
	entries := m.confirmEntries()
	buttons := ansi.StringWidth(confirmKeep + "   " + m.confirmCloseLabel())
	want := max(buttons, ansi.StringWidth(title)+2)
	for _, e := range entries {
		want = max(want, ansi.StringWidth(e))
	}
	// The buttons never wrap where the pane has room for them; a narrower
	// pane centers the popup over the tab area. The height follows the list
	// and the notice wrapped at the granted width.
	x, y, w, h := m.fitOverlay(c.pane, want+4, buttons+4, func(w int) int {
		return frame.rowsFor(w, len(m.confirmBody(max(w-4, 1))))
	})
	c.x, c.y = x, y
	o := &overlay{x: x, y: y, w: w, h: h, frame: frame, scroll: c.scroll}
	body := m.confirmBody(o.inner())
	o.frame.Lines = body[min(c.scroll, len(body)):]
	return o
}

// inner is the overlay's body width.
func (o *overlay) inner() int {
	inner, _, _ := o.frame.layout(o.w, o.h)
	return inner
}

// modalPopup is the open modal's rows in the palette area: only a browser
// prompt now; the close popup and the picker draw in the pane overlay.
func (m *model) modalPopup(rows int) []string {
	return m.browserPopup(rows)
}

// confirmClick takes a press while the popup is open: a press on a button
// activates it; any other press keeps. Either way it is chrome-owned
// through its release.
func (m *model) confirmClick(mo tea.Mouse) {
	if mo.Button != tea.MouseLeft {
		return
	}
	// Only the buttons' own row is a hitbox: the list above it shows
	// commands, whose text never acts.
	o := m.confirmOverlay()
	rows := o.frame.render(o.w, o.h, "")
	mx, my := m.overlayPoint(o, mo)
	if footer := len(rows) - 2; len(rows) >= 3 && my == footer {
		line := ansi.Strip(rows[footer])
		for _, b := range []string{confirmKeep, m.confirmCloseLabel()} {
			if i := strings.Index(line, b); i >= 0 {
				x := ansi.StringWidth(line[:i])
				if mx >= x && mx < x+len(b) {
					if b == m.confirmCloseLabel() {
						m.acceptConfirm()
					} else {
						m.cancelConfirm()
					}
					return
				}
			}
		}
	}
	m.cancelConfirm()
}

func (m *model) confirmCloseLabel() string {
	if m.confirm.space != "" {
		return "[ Close tabs & switch ]"
	}
	return confirmClose
}
