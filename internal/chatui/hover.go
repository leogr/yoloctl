package chatui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// hoverKey identifies the click target under the pointer across frames, as
// space view's target keys do: a frame hit by area, item and id, or a
// transcript run by its message key and action. The zero value is none.
type hoverKey struct {
	area hitArea
	item int
	id   string
	msg  string
	act  action
}

// hoverAt resolves the target under a buttonless pointer from the last
// frame. While an overlay is open only its rows and buttons react; the
// composer and plain transcript text are not click targets to highlight.
func (m *Model) hoverAt(x, y int) hoverKey {
	if m.linkPrompt != nil {
		return hoverKey{}
	}
	h, ok := m.hitAt(x, y)
	if m.overlay != nil {
		if ok && (h.area == hitOverlayRow || h.area == hitOverlayButton) {
			return hoverKey{area: h.area, item: h.item, id: h.id}
		}
		return hoverKey{}
	}
	if ok {
		switch h.area {
		case hitComposer, hitOverlay, hitOverlayRow, hitOverlayButton:
			return hoverKey{}
		case hitTop:
			if h.id == "" {
				return hoverKey{}
			}
		}
		return hoverKey{area: h.area, item: h.item, id: h.id}
	}
	if row, ok := m.rowAt(y); ok && m.inTranscript(x, y) {
		if act := actionAt(m.widthMethod, row.runs, x); act.kind != actNone {
			return hoverKey{area: hitTranscript, msg: row.key, act: act}
		}
	}
	return hoverKey{}
}

// activeHover is the hover that applies to the frame being drawn: nothing
// while a drag or a modal prompt owns the pointer.
func (m *Model) activeHover() hoverKey {
	h := m.hover
	modal := h.area == hitOverlayRow || h.area == hitOverlayButton
	if m.linkPrompt != nil || m.drag.active || (m.overlay != nil) != modal {
		return hoverKey{}
	}
	return h
}

func (m *Model) hovered(area hitArea, item int, id string) bool {
	h := m.activeHover()
	return h.area == area && h.item == item && h.id == id && h.msg == ""
}

// memberKey identifies a participant's panel rows across roster changes.
func memberKey(mem Member) string { return mem.Account + "\x00" + mem.Nick }

func (m *Model) panelHovered(mem Member) bool {
	h := m.activeHover()
	return h.area == hitPanel && h.id == memberKey(mem)
}

// hoverStyle marks a hovered click target the way space view marks its
// chips: underlined and bold, keeping the target's own colors.
func hoverStyle(st spaceview.Style) spaceview.Style {
	st.Underline, st.Bold = true, true
	return st
}

// hoverRun styles a hovered target's text; padding around it, such as a
// chip's inner spaces, stays plain so only the label is underlined.
func (m *Model) hoverRun(r run, on bool) run {
	if on {
		r.style = hoverStyle(r.style)
	}
	return r
}

func hoverRuns(runs []run, on bool) []run {
	if !on {
		return runs
	}
	var out []run
	for _, r := range runs {
		core := strings.TrimSpace(r.text)
		if core == "" {
			out = append(out, r)
			continue
		}
		lead := r.text[:strings.Index(r.text, core)]
		trail := r.text[len(lead)+len(core):]
		if lead != "" {
			out = append(out, run{text: lead, style: r.style})
		}
		out = append(out, run{text: core, style: hoverStyle(r.style), act: r.act})
		if trail != "" {
			out = append(out, run{text: trail, style: r.style})
		}
	}
	return out
}

// hoverRow underlines the hovered action's runs in one transcript row; a
// wrapped link is underlined in every row of its message.
func (m *Model) hoverRow(row transcriptRow) []run {
	h := m.activeHover()
	if h.area != hitTranscript || h.msg != row.key {
		return row.runs
	}
	var out []run
	for i, r := range row.runs {
		if r.act == h.act {
			if out == nil {
				out = append([]run(nil), row.runs...)
			}
			out[i].style = hoverStyle(r.style)
		}
	}
	if out == nil {
		return row.runs
	}
	return out
}

// hoverLine underlines an already styled overlay row, re-applying the
// underline after each style reset inside it.
func hoverLine(s string) string {
	const on = "\x1b[4m"
	return on + strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+on) + "\x1b[24m"
}

// iconSlot gives a glyph at least two cells plus a gap, as the TUI tab
// strip does: fonts often draw symbols such as ✳ wider than their one cell,
// which would otherwise cover the space after them.
func iconSlot(wm ansi.Method, glyph string) string {
	return glyph + strings.Repeat(" ", max(2-wm.StringWidth(glyph), 0)+1)
}

// topHints name the top band's targets and what a click on each does.
var topHints = map[string][2]string{
	"channels": {"Channels", "click or Ctrl+G to choose a channel"},
	"live":     {"Jump to latest", "click, Esc or Alt+End to return to the latest messages"},
}

// hoverHint names the hovered target and what a click does, like space
// view's footer hint. Footer actions, suggestions and overlays explain
// themselves, so they have none.
func (m *Model) hoverHint() []run {
	h := m.activeHover()
	t := m.theme
	var name, what string
	switch h.area {
	case hitTop:
		// The target may be gone, as the jump is once used.
		if m.drawn(hitTop, h.id) {
			top := topHints[h.id]
			name, what = top[0], top[1]
		}
	case hitFilter:
		switch {
		case h.id == "clear":
			name, what = "All messages", "click to show every message again"
		case m.filter == nil:
			name, what = "For me", "click or Alt+M: only messages from or mentioning you, and replies to those · Alt+F chooses people"
		case m.selfOnly():
			name, what = "For me", "click or Alt+M to show all messages · Alt+F chooses people"
		case m.filter.thread != "":
			name, what = "Thread", "click or Alt+F to choose people instead · Alt+M for you · ✕ shows all"
		default:
			name, what = "Filter "+m.filterName(), "click or Alt+F to choose people · Alt+M for you · ✕ shows all"
		}
	case hitChannel:
		if h.item < len(m.order) {
			name, what = m.order[h.item], "click to show it"
		}
	case hitPanel:
		account, nick, _ := strings.Cut(h.id, "\x00")
		name, what = m.participantHint(account, nick)
		if label, ok := m.hoveredLabel(); ok && (m.opts.NoMotion || marqueeSteps(m.widthMethod, label) == 0) {
			// A clipped row that does not scroll is named here in full.
			what = label.text + " · " + what
		}
	case hitReplyCancel:
		name, what = "✕", "click to cancel the reply"
	case hitFold:
		name, what = "[Pasted #"+h.id+"]", "click or Alt+P to expand · Backspace removes it"
		if h.item < len(m.editor.folds) && strings.HasPrefix(h.id, imageHitID) {
			if img := m.editor.folds[h.item].img; img != nil {
				name, what = imageLabel(m.editor.folds[h.item].n), imageDetails(img)+" · sent in place, in draft order · Backspace removes it"
			}
		}
	case hitTranscript:
		switch h.act.kind {
		case actAuthor:
			name, what = m.participantHint(h.act.value, h.act.extra)
		case actMention:
			name, what = "@"+h.act.value, "click for details"
		case actReply:
			name, what = "Reply", "click to show the original message"
		case actRef:
			name, what = "Reference", "click to show it if it is loaded in this channel"
		case actFilter:
			name, what = "All messages", "click to show every message again"
		case actCopyCode:
			name, what = "Copy code", "click to copy this code block"
			if m.opts.Copy == nil || m.opts.Clipboard == "off" {
				what = "the host clipboard is off (seams.clipboard)"
			}
		case actLink:
			name = h.act.value
			switch {
			case m.opts.OpenLink == nil:
				what = "opening links is off (seams.browser)"
			case m.opts.ConfirmLinks:
				what = "click, then confirm, to open"
			default:
				what = "click to open"
			}
		}
	}
	if name == "" {
		return nil
	}
	return []run{
		deco(" ▸ ", spaceview.Style{FG: t.Brand, Bold: true}),
		deco(oneLine(name), spaceview.Style{FG: t.Bright, Bold: true}),
		deco("  "+what, spaceview.Style{FG: t.Dim}),
	}
}

// drawn reports whether this frame registered the target so far: the top
// band and divider are drawn before the footer that names a hovered one.
func (m *Model) drawn(area hitArea, id string) bool {
	for _, h := range m.hits {
		if h.area == area && h.id == id {
			return true
		}
	}
	return false
}

// participantHint says what a click on a participant does. Attachment is
// only for a trusted box; others open their details card.
func (m *Model) participantHint(account, nick string) (string, string) {
	w := m.identify(nick, account)
	if w.trusted && w.box {
		return w.name, "click to attach · right-click for details"
	}
	return w.name, "click for details"
}
