package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// A toast captures display context, never a session or a numeric tab index.
// The live tab carries attention separately, so closing it retains no dead view.
type toastNotice struct {
	ownerID            uint64
	kind               prettylog.Kind
	text, body, origin string
	repeats, scroll    int
}

func (m *model) postNotice(kind prettylog.Kind, text string) { m.postTabNotice(nil, kind, text) }

func (m *model) postTabNotice(t *tabs.Tab, kind prettylog.Kind, text string) {
	if text == "" {
		return
	}
	origin := m.ctxSpace
	var ownerID uint64
	if t != nil && m.tabs.Index(t) >= 0 {
		t.NoticeAttention = true
		if t.NoticeID == 0 {
			m.nextNoticeOwner++
			t.NoticeID = m.nextNoticeOwner
		}
		ownerID = t.NoticeID
		if origin != "" {
			origin += " · "
		}
		origin += t.Title()
	}
	// The shared renderer bounds and escapes the captured body and origin before
	// any wrapping or hit geometry. Rendering never interprets participant ANSI.
	body := ansi.Strip(m.noticeLine(kind, text))
	if origin != "" {
		origin = ansi.Strip(m.noticeLine(prettylog.KindNotice, origin))
	}
	plain, _ := strings.CutPrefix(body, prettylog.DefaultGlyphs.For(kind)+" ")
	n := toastNotice{ownerID: ownerID, kind: kind, text: plain, body: body, origin: origin, repeats: 1}
	last := len(m.notices) - 1
	if last >= 0 && m.notices[last].ownerID == ownerID && m.notices[last].kind == kind && m.notices[last].body == body && m.notices[last].origin == origin {
		m.notices[last].repeats++
	} else {
		m.notices = append(m.notices, n)
	}
	m.noticeIndex = len(m.notices) - 1
}

func (m *model) postSurfaceNotice(surface any, kind prettylog.Kind, text string) {
	var s *term.Session
	switch v := surface.(type) {
	case *term.Session:
		s = v
	case *imagePasteKey:
		s = v.Session
	}
	var t *tabs.Tab
	if s != nil {
		t, _ = m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == s })
	}
	m.postTabNotice(t, kind, text)
}

func (m *model) startNotice(err error) {
	if m.standalone {
		m.attachError = err
	}
	m.postNotice(prettylog.KindError, err.Error())
}

func (m *model) currentToast() *toastNotice {
	if len(m.notices) == 0 {
		return nil
	}
	m.noticeIndex = min(max(m.noticeIndex, 0), len(m.notices)-1)
	return &m.notices[m.noticeIndex]
}
func (m *model) moveToast(delta int) {
	if m.currentToast() != nil {
		m.noticeIndex = min(max(m.noticeIndex+delta, 0), len(m.notices)-1)
	}
}
func (m *model) dismissToast() {
	if m.currentToast() == nil {
		return
	}
	i := m.noticeIndex
	copy(m.notices[i:], m.notices[i+1:])
	m.notices[len(m.notices)-1] = toastNotice{}
	m.notices = m.notices[:len(m.notices)-1]
	m.noticeIndex = max(min(i, len(m.notices)-1), 0)
}
func (m *model) toastSuppressed() bool {
	return m.browserDialog != nil || m.spacePicker != nil || m.confirm != nil || m.picker != nil || m.prefix != nil || m.activeNotice() != nil
}

type toastRect struct {
	x, y, w, h int
	rows       []string
	// Controls occupy exact byte/cell spans on the ASCII control row.
	controlY, prevX, nextX, closeX int
	capacity, maxScroll            int
}

func (o toastRect) contains(x, y int) bool {
	return o.w > 0 && x >= o.x && x < o.x+o.w && y >= o.y && y < o.y+o.h
}

// toastLayout is the one geometry for paint, cursor occlusion and all hits.
// Dashboard toasts begin below the fixed strip, over palette output as needed.
func (m *model) toastLayout() toastRect {
	n := m.currentToast()
	if n == nil || m.toastSuppressed() || m.width <= 0 || m.height <= m.chromeRows() {
		return toastRect{}
	}
	w := min(m.width, 64)
	y := m.chromeRows()
	available := m.height - y
	inner := max(w-4, 1)
	body := n.body
	if n.origin != "" {
		body = n.origin + "\n" + body
	}
	if n.repeats > 1 {
		body += fmt.Sprintf(" (×%d)", n.repeats)
	}
	wrapped := strings.Split(ansi.Hardwrap(body, inner, true), "\n")
	h := min(len(wrapped)+3, min(available, 12))
	o := toastRect{x: m.width - w, y: y, w: w, h: h, controlY: y + 1, prevX: -1, nextX: -1, closeX: -1}
	controls := fmt.Sprintf("[prev] %d/%d [next] [x]", m.noticeIndex+1, len(m.notices))
	framed := w >= 24 && h >= 4 && w-4 >= len(controls)
	if !framed {
		// Compact terminals retain the counter/close control, and Esc always works.
		line := fmt.Sprintf("%d/%d [x] ", m.noticeIndex+1, len(m.notices))
		o.controlY = y
		o.closeX = o.x + strings.Index(line, "[x]")
		o.capacity = max(h-1, 0)
		o.maxScroll = max(len(wrapped)-o.capacity, 0)
		o.rows = append(o.rows, ansi.Truncate(line+strings.ReplaceAll(body, "\n", " "), w, ""))
	} else {
		o.prevX = o.x + 2
		o.nextX = o.x + 2 + strings.Index(controls, "[next]")
		o.closeX = o.x + 2 + strings.Index(controls, "[x]")
		title := " notices "
		o.rows = append(o.rows, cDim+"╭─"+title+strings.Repeat("─", w-3-ansi.StringWidth(title))+"╮"+cReset)
		control := controls
		if m.noticeIndex == 0 {
			control = strings.Replace(control, "[prev]", cDim+"[prev]"+cReset, 1)
		}
		if m.noticeIndex == len(m.notices)-1 {
			control = strings.Replace(control, "[next]", cDim+"[next]"+cReset, 1)
		}
		o.rows = append(o.rows, toastRow(control, w))
		o.capacity = h - 3
		o.maxScroll = max(len(wrapped)-o.capacity, 0)
	}
	scroll := min(max(n.scroll, 0), o.maxScroll)
	for i := 0; i < o.capacity; i++ {
		line := ""
		if scroll+i < len(wrapped) {
			line = wrapped[scroll+i]
		}
		if framed {
			o.rows = append(o.rows, toastRow(m.noticeFooter(n.kind)(line, i), w))
		} else {
			o.rows = append(o.rows, ansi.Truncate(line, w, ""))
		}
	}
	if framed {
		o.rows = append(o.rows, cDim+"╰"+strings.Repeat("─", w-2)+"╯"+cReset)
	}
	return o
}
func toastRow(s string, w int) string {
	return cDim + "│" + cReset + " " + s + strings.Repeat(" ", max(w-4-ansi.StringWidth(s), 0)) + " " + cDim + "│" + cReset
}
func (m *model) applyToast(lines []string) {
	o := m.toastLayout()
	for i, row := range o.rows {
		if y := o.y + i; y < len(lines) {
			lines[y] = spliceRow(lines[y], paintRow(row, m.surfaceBg()), o.x, o.w, m.width)
		}
	}
}
func (m *model) scrollToast(delta int) {
	o := m.toastLayout()
	if n := m.currentToast(); n != nil {
		n.scroll = min(max(n.scroll, 0)+delta, o.maxScroll)
		n.scroll = max(n.scroll, 0)
	}
}
func (m *model) toastCommand(k tea.Key) bool {
	if m.currentToast() == nil {
		return false
	}
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	if mod != 0 {
		return false
	}
	switch {
	case k.Text == "[" || k.Code == '[':
		m.moveToast(-1)
	case k.Text == "]" || k.Code == ']':
		m.moveToast(1)
	case k.Code == tea.KeyPgUp:
		m.scrollToast(-max(m.toastLayout().capacity, 1))
	case k.Code == tea.KeyPgDown:
		m.scrollToast(max(m.toastLayout().capacity, 1))
	default:
		return false
	}
	return true
}
func (m *model) toastEscape(k tea.Key) bool {
	if m.toastLayout().w == 0 || m.chrome != nil || m.selectionPointer != nil || len(m.mouseOwners) > 0 {
		return false
	}
	if k.Code != tea.KeyEscape || k.Mod&^(tea.ModCapsLock|tea.ModNumLock) != 0 {
		return false
	}
	if m.keyOwners[keyIdentity(k)] != nil {
		return false
	}
	if !k.IsRepeat {
		m.consumeToastKey(k)
		m.dismissToast()
	}
	return true
}
func (m *model) toastMouse(msg tea.Msg, mo tea.Mouse) bool {
	// Existing press owners get every motion/release even if a toast arrived.
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if mo.Button != tea.MouseNone {
			return false
		}
		return m.toastLayout().contains(mo.X, mo.Y)
	case tea.MouseClickMsg, tea.MouseWheelMsg:
	default:
		return false
	}
	if m.chrome != nil {
		return false
	}
	o := m.toastLayout()
	if !o.contains(mo.X, mo.Y) {
		return false
	}
	if _, wheel := msg.(tea.MouseWheelMsg); wheel {
		if mo.Button == tea.MouseWheelUp {
			m.scrollToast(-1)
		}
		if mo.Button == tea.MouseWheelDown {
			m.scrollToast(1)
		}
		return true
	}
	if m.mouseOwners == nil {
		m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
	}
	m.mouseOwners[mo.Button] = mouseOwner{}
	if mo.Button != tea.MouseLeft {
		return true
	}
	switch {
	case mo.Y == o.controlY && mo.X >= o.prevX && o.prevX >= 0 && mo.X < o.prevX+6:
		m.moveToast(-1)
	case mo.Y == o.controlY && mo.X >= o.nextX && o.nextX >= 0 && mo.X < o.nextX+6:
		m.moveToast(1)
	case mo.Y == o.controlY && mo.X >= o.closeX && mo.X < o.closeX+3:
		m.dismissToast()
	case mo.Y != o.controlY:
		m.dismissToast()
	}
	return true
}

func (m *model) applyDockHint(lines []string) {
	if m.chrome == nil || m.chrome.status == "" || m.height <= m.chromeRows() {
		return
	}
	y := min(m.chromeRows(), len(lines)-1)
	hint := ansi.Truncate(m.noticeLine(prettylog.KindNotice, m.chrome.status), m.width, "")
	lines[y] = spliceRow(lines[y], paintRow(hint, m.surfaceBg()), 0, ansi.StringWidth(hint), m.width)
}

func (m *model) consumeToastKey(k tea.Key) {
	if m.keyOwners[keyIdentity(k)] == nil {
		if m.consumedKeys == nil {
			m.consumedKeys = make(map[rune]bool)
		}
		m.consumedKeys[keyIdentity(k)] = true
		if m.toastKeys == nil {
			m.toastKeys = make(map[rune]bool)
		}
		m.toastKeys[keyIdentity(k)] = true
	}
}
