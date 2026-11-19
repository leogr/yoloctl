package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

// An eligible ended view can resume in place: r, or a click on
// the yoloctl-drawn button on the pane's last row, uses its retained
// argv or typed attach target in the same slot. Nothing resumes on its
// own; `box attach` itself decides what happens to a stopped box or one
// that needs recreating, and reports it in the new view.

const (
	endedCloseButton = "[ Close ]"
	reattachButton   = "[ Re-attach ]"
)

// attachValueFlags are the `box attach` flags that take a separate value,
// skipped when finding the box name for display.
var attachValueFlags = map[string]bool{"--space": true, "--chat-room-id": true, "--chat-member-id": true, "--chat-box-id": true}

// reattachArgs is the argv that re-attaches an ended view, and the box it
// names (empty when it cannot be shown). Only `attach` and `box attach`
// views qualify; a help request or the structured --box-use interface
// does not. --new-session is dropped in every spelling so the box's own
// conversation resumes; every other argument, chat guards included, is
// kept verbatim.
func reattachArgs(args []string) (out []string, box string, ok bool) {
	var rest []string
	switch {
	case len(args) >= 1 && args[0] == "attach":
		out, rest = []string{"attach"}, args[1:]
	case len(args) >= 2 && args[0] == "box" && args[1] == "attach":
		out, rest = []string{"box", "attach"}, args[2:]
	default:
		return nil, "", false
	}
	if hasHelp(rest) {
		return nil, "", false
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			if box == "" && i+1 < len(rest) {
				box = rest[i+1]
			}
			out = append(out, rest[i:]...)
			i = len(rest)
			continue
		case a == "--box-use" || strings.HasPrefix(a, "--box-use="):
			return nil, "", false
		case a == "--new-session" || strings.HasPrefix(a, "--new-session="):
			continue
		case attachValueFlags[a]:
			out = append(out, a)
			if i+1 < len(rest) {
				i++
				out = append(out, rest[i])
			}
			continue
		case box == "" && !strings.HasPrefix(a, "-"):
			box = a
		}
		out = append(out, a)
	}
	if layout.ValidateName("agentbox", box) != nil {
		box = ""
	}
	return slices.Clip(out), box, true
}

// An ended view action always uses retained arguments/target, never the
// current palette context or a rendered command line.
type endedViewAction struct {
	args        []string
	label, hint string
}

func reattachAction(args []string, box string) endedViewAction {
	hint := "r to re-attach"
	if box != "" {
		hint += " to " + box
	}
	return endedViewAction{args, reattachButton, hint}
}

func viewEndAction(args []string) (endedViewAction, bool) {
	v, ok := parseReopenView(args)
	if !ok || v.kind == "attach" || !v.spaceExplicit && !v.host {
		return endedViewAction{}, false
	}
	return endedViewAction{v.args, "[ Reopen ]", "r to reopen"}, true
}

func viewActionForPane(p *panes.Pane) (endedViewAction, bool) {
	if p == nil {
		return endedViewAction{}, false
	}
	// A failed implicit-context lookup leaves initial argv untouched so the
	// CLI reports its normal error, but cannot offer context-dependent replay.
	if v, ok := parseReopenView(p.Args); ok && v.kind == "attach" && !v.spaceExplicit {
		return endedViewAction{}, false
	}
	if args, box, ok := reattachArgsForPane(p); ok {
		return reattachAction(args, box), true
	}
	return viewEndAction(p.Args)
}

// reattachHint is the end banner's extra hint for a pane started with args:
// how to re-attach, or "" for a view that does not offer it. A standalone
// attach ends with its program, so it never offers one.
func (m *model) reattachHint(args []string) string {
	if m.standalone {
		return ""
	}
	if v, ok := parseReopenView(args); ok && v.kind == "attach" && !v.spaceExplicit {
		return ""
	}
	if out, box, ok := reattachArgs(args); ok {
		return reattachAction(out, box).hint
	}
	if action, ok := viewEndAction(args); ok {
		return action.hint
	}
	return ""
}

// reattachable reports an ended view of t that offers an explicit action.
func (m *model) reattachable(t *tabs.Tab, p *panes.Pane) bool {
	if m.standalone || t == nil || p == nil || p.Empty() || !p.Exited || !hasPane(t, p) {
		return false
	}
	_, ok := viewActionForPane(p)
	return ok
}

// reattachPane runs an eligible ended view again in its place: the same
// rectangle, split, zoom and focus. If the new view cannot start locally,
// the ended view stays as it was and a notice toast says why.
func (m *model) reattachPane(t *tabs.Tab, old *panes.Pane) {
	if !m.reattachable(t, old) {
		return
	}
	action, _ := viewActionForPane(old)
	m.laidOut(t)
	r := old.Rect()
	p := m.startPane(action.args, max(r.H, 1), max(r.W, 1))
	if p == nil {
		return // startPane set the status; the ended view is kept
	}
	m.dropPaneOwners(old)
	t.Layout.Replace(old, p)
	old.Session.Close()
	m.layoutTab(t)
	m.activatePane(t, p)
	m.scrollToLive()
	m.watchPaneExit(p)
}

// endedButtonSlot is a button on the last row of an ended pane, in tab-area cells.
type endedButtonSlot struct {
	pane     *panes.Pane
	x, y     int
	label    string
	gapAfter bool
}

func (m *model) endedButtonSlots() []endedButtonSlot {
	t := m.tabs.Current()
	if t == nil || m.standalone {
		return nil
	}
	_, visible := m.tabArea()
	var slots []endedButtonSlot
	for _, p := range m.laidOut(t).Visible() {
		r := p.Rect()
		y := r.Y + r.H - 1
		if p.Empty() || !p.Exited || r.H <= 0 || y >= visible {
			continue
		}
		action, resumable := viewActionForPane(p)
		resumeWidth := ansi.StringWidth(action.label)
		resumeFits := resumable && r.W >= resumeWidth
		x := r.X
		closeWidth := len(endedCloseButton)
		// Keep an existing resume button usable when both buttons cannot fit.
		if r.W >= closeWidth && (!resumeFits || r.W >= closeWidth+1+resumeWidth) {
			slots = append(slots, endedButtonSlot{pane: p, x: x, y: y, label: endedCloseButton, gapAfter: resumeFits})
			x += closeWidth + 1
		}
		if resumeFits {
			slots = append(slots, endedButtonSlot{pane: p, x: x, y: y, label: action.label})
		}
	}
	return slots
}

// applyEndedButtons draws before the popups, which stay on top.
func (m *model) applyEndedButtons(base []string) {
	for _, s := range m.endedButtonSlots() {
		if s.y < len(base) {
			label := "\x1b[7m" + s.label + "\x1b[27m"
			if s.gapAfter {
				label += " "
			}
			base[s.y] = spliceRow(base[s.y], label, s.x, ansi.StringWidth(label), m.width)
		}
	}
}

// endedButtonClick takes a left press on an ended view's button and keeps
// the gesture chrome-owned through its release.
func (m *model) endedButtonClick(msg tea.Msg, mo tea.Mouse) bool {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return false
	}
	mo = m.mouseHit(mo)
	y := mo.Y - m.tabTop()
	for _, s := range m.endedButtonSlots() {
		if y != s.y || mo.X < s.x || mo.X >= s.x+ansi.StringWidth(s.label) {
			continue
		}
		if m.mouseOwners == nil {
			m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
		}
		m.mouseOwners[tea.MouseLeft] = mouseOwner{}
		m.selectionPointer = nil
		t := m.tabs.Current()
		if s.label == endedCloseButton {
			m.closeEndedPane(t, s.pane)
		} else {
			m.reattachPane(t, s.pane)
		}
		return true
	}
	return false
}

// closeEndedPane shares the keyboard and mouse dismissal path.
func (m *model) closeEndedPane(t *tabs.Tab, p *panes.Pane) {
	focused := !m.focusPalette && t.Focused() == p
	m.requestDetachPane(t, p)
	if focused {
		m.scrollToLive()
	}
}
