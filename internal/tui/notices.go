package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
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
	box, space, tab    string
	when               time.Time
	unread             bool
}

func (m *model) postNotice(kind prettylog.Kind, text string) { m.postTabNotice(nil, kind, text) }

func (m *model) postTabNotice(t *tabs.Tab, kind prettylog.Kind, text string) {
	m.postPaneNotice(t, nil, kind, text)
}

func (m *model) postPaneNotice(t *tabs.Tab, source *panes.Pane, kind prettylog.Kind, text string) {
	if text == "" {
		return
	}
	origin := m.ctxSpace
	var ownerID uint64
	var box, tab string
	space := m.ctxSpace
	if t != nil && m.tabs.Index(t) >= 0 {
		t.NoticeAttention = true
		if t.NoticeID == 0 {
			m.nextNoticeOwner++
			t.NoticeID = m.nextNoticeOwner
		}
		ownerID = t.NoticeID
		if source == nil {
			source = t.Focused()
		}
		tab = t.Title()
		if source != nil {
			tab = source.Title()
		}
		if p := source; p != nil && p.Session != nil {
			if target, ok := p.Session.ResumeTarget(); ok && target.Box != "" {
				box, space = target.Box, target.Space
			}
		}
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
	safe := func(value string) string {
		clean := ansi.Strip(m.noticeLine(prettylog.KindNotice, value))
		clean, _ = strings.CutPrefix(clean, prettylog.DefaultGlyphs.For(prettylog.KindNotice)+" ")
		return clean
	}
	n := toastNotice{ownerID: ownerID, kind: kind, text: plain, body: body, origin: origin, repeats: 1, box: safe(box), space: safe(space), tab: safe(tab), when: time.Now(), unread: true}
	last := len(m.notices) - 1
	if last >= 0 && m.notices[last].ownerID == ownerID && m.notices[last].kind == kind && m.notices[last].body == body && m.notices[last].origin == origin {
		m.notices[last].repeats++
		m.notices[last].unread = true
	} else {
		m.notices = append(m.notices, n)
	}
	if !m.noticeFocused {
		m.noticeIndex = len(m.notices) - 1
		m.noticeVisible = true
		m.noticeSelected = 0
		m.noticeUntil = clock().Add(noticeAutoTime)
		m.noticeEpoch++
		if m.noticeTimer != nil {
			m.noticeTimer.Stop()
		}
		if m.program != nil {
			program, epoch := m.program, m.noticeEpoch
			m.noticeTimer = time.AfterFunc(noticeAutoTime, func() { program.Send(noticeExpireMsg(epoch)) })
		}
	}
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
	var p *panes.Pane
	if s != nil {
		t, p = m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == s })
	}
	m.postPaneNotice(t, p, kind, text)
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
		m.noticeSelected = 0
		if m.noticeFocused {
			m.notices[m.noticeIndex].unread = false
		}
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
	m.noticeSelected = 0
	if len(m.notices) == 0 {
		m.closeToast()
	} else if m.noticeFocused {
		m.notices[m.noticeIndex].unread = false
	}
}
func (m *model) toastSuppressed() bool {
	return m.browserDialog != nil || m.spacePicker != nil || m.confirm != nil || m.picker != nil || m.prefix != nil || m.activeNotice() != nil
}

type toastButton struct {
	action  string
	x, y, w int
}
type toastRect struct {
	x, y, w, h                     int
	rows                           []string
	controlY, prevX, nextX, closeX int
	capacity, maxScroll            int
	buttons                        []toastButton
}

func (o toastRect) contains(x, y int) bool {
	return o.w > 0 && x >= o.x && x < o.x+o.w && y >= o.y && y < o.y+o.h
}

// toastLayout owns the dialog's paint, hit and cursor geometry.
// Only the body scrolls; the title, buttons and hint remain fixed.
func (m *model) toastLayout() toastRect {
	n := m.currentToast()
	if n == nil || !m.noticeVisible || m.toastSuppressed() || m.width <= 0 || m.height <= m.chromeRows() {
		return toastRect{}
	}
	w := min(m.width, max(min(100, m.width*7/10), min(32, m.width)))
	x, top := m.headerPopupOrigin(w)
	if m.standalone {
		x, top = (m.width-w)/2, 0
	}
	available := m.height - top
	title := fmt.Sprintf("Notice %d of %d", m.noticeIndex+1, len(m.notices))
	if n.repeats > 1 {
		title += fmt.Sprintf(" · ×%d", n.repeats)
	}
	o := toastRect{w: w, x: x, prevX: -1, nextX: -1, closeX: -1}
	inner := max(w-4, 1)
	theme := spaceview.NewTheme(!m.bgDetected || m.bgIsDark, m.background, nil)
	if m.noticeFocused {
		theme.Frame = theme.Brand
	}
	frame := spaceview.DialogFrame{Theme: theme, Method: ansi.GraphemeWidth, Width: w}
	style := func(s string, c spaceview.Color) string {
		return spaceview.StyleText(s, spaceview.Style{FG: c, BG: theme.Popup})
	}
	var body []string
	severity := strings.TrimSuffix(prettylog.PlainPrefix(n.kind), ": ")
	if severity == "" {
		severity = "information"
	}
	if n.kind == prettylog.KindOK {
		severity = "success"
	}
	if n.kind == prettylog.KindUnknown {
		severity = n.text
	}
	if n.kind == prettylog.KindSkipped {
		severity = "skipped"
	}
	if n.box != "" {
		severity = n.box + " · " + severity
	}
	for _, line := range strings.Split(ansi.Wrap(prettylog.DefaultGlyphs.For(n.kind)+" "+severity, inner, "/\\"), "\n") {
		body = append(body, m.noticeFooter(n.kind)(line, 0))
	}
	body = append(body, "")
	field := func(label, value string) {
		if value == "" {
			return
		}
		labelW := min(10, max(inner/3, 1))
		valueW := max(inner-labelW, 1)
		for i, line := range strings.Split(ansi.Wrap(value, valueW, "/\\"), "\n") {
			left := strings.Repeat(" ", labelW)
			if i == 0 {
				left = ansi.Truncate(label, labelW, "")
				left += strings.Repeat(" ", max(labelW-ansi.StringWidth(left), 0))
			}
			body = append(body, style(left, theme.Dim)+style(line, theme.Text))
		}
	}
	if n.box != "" {
		value := n.box
		if n.space != "" {
			value += "   (space " + n.space + ")"
		}
		field("Box", value)
	} else {
		field("Space", n.space)
	}
	field("Tab", n.tab)
	field("Detail", n.text)
	field("When", n.when.Format("15:04:05"))
	compact := func() toastRect {
		o.h = min(available, max(len(body)+1, 1))
		o.y = top
		if m.standalone {
			o.y += (available - o.h) / 2
		}
		o.controlY = o.y
		o.closeX = o.x
		o.capacity = max(o.h-1, 0)
		o.maxScroll = max(len(body)-o.capacity, 0)
		header := ansi.Truncate("[x] "+title, w, "")
		if m.noticeFocused {
			header = style(header, theme.Brand)
		}
		o.rows = append(o.rows, header)
		o.buttons = []toastButton{{action: "dismiss", x: o.x, y: o.y, w: min(3, w)}}
		scroll := min(max(n.scroll, 0), o.maxScroll)
		for i := 0; i < o.capacity; i++ {
			o.rows = append(o.rows, ansi.Truncate(body[scroll+i], w, ""))
		}
		return o
	}
	if w < 24 || available < 7 {
		return compact()
	}
	choices := m.allToastChoices()
	selected := m.selectedToastButtonFor(choices)
	var buttonRows []string
	var hits []toastButton
	row, col := 0, 0
	for i, c := range choices {
		label := ansi.Truncate(c.label, max(inner-2, 1), "")
		size := ansi.StringWidth(label) + 2
		gap := 0
		if col > 0 {
			gap = 2
		}
		if col+gap+size > inner {
			row++
			col = 0
			gap = 0
		}
		if row == len(buttonRows) {
			buttonRows = append(buttonRows, "")
		}
		buttonRows[row] += style(strings.Repeat(" ", gap), theme.Text) + spaceview.DialogButton(theme, label, m.noticeFocused && i == selected, false, c.disabled)
		col += gap
		hits = append(hits, toastButton{action: c.action, x: o.x + 2 + col, y: row, w: size})
		col += size
	}
	// Wrapped controls must leave at least three body rows. Otherwise use the
	// same bounded compact paint/hit geometry as very small viewports.
	if available < 7+len(buttonRows) {
		return compact()
	}
	o.h = min(len(body)+4+len(buttonRows), available)
	o.y = top
	if m.standalone {
		o.y += (available - o.h) / 2
	}
	o.capacity = max(o.h-4-len(buttonRows), 0)
	o.maxScroll = max(len(body)-o.capacity, 0)
	o.rows = append(o.rows, frame.Header(title), frame.Row(""))
	scroll := min(max(n.scroll, 0), o.maxScroll)
	for i := 0; i < o.capacity; i++ {
		line := ""
		if scroll+i < len(body) {
			line = body[scroll+i]
		}
		o.rows = append(o.rows, frame.Row(line))
	}
	o.rows = append(o.rows, frame.Row(""))
	o.controlY = o.y + len(o.rows)
	for _, hit := range hits {
		hit.y += o.controlY
		o.buttons = append(o.buttons, hit)
		switch hit.action {
		case "prev":
			o.prevX = hit.x
		case "next":
			o.nextX = hit.x
		case "dismiss":
			o.closeX = hit.x
		}
	}
	for _, row := range buttonRows {
		o.rows = append(o.rows, frame.Row(row))
	}
	o.rows = append(o.rows, frame.Footer("← → Tab select · Enter runs · ‹ › notices · Esc closes"))
	return o
}

func (m *model) toastTab(n *toastNotice) *tabs.Tab {
	if n == nil || n.ownerID == 0 {
		return nil
	}
	for _, t := range m.tabs.Tabs {
		if t.NoticeID == n.ownerID {
			return t
		}
	}
	return nil
}
func (m *model) toastAction(action string) {
	switch action {
	case "prev":
		m.moveToast(-1)
	case "next":
		m.moveToast(1)
	case "dismiss":
		m.dismissToast()
	case "visit":
		if t := m.toastTab(m.currentToast()); t != nil {
			m.closeToast()
			m.activateTab(m.tabs.Index(t))
		}
	}
}

// Focused dialog input yields to existing modal/prefix and held-gesture owners.
func (m *model) toastInputFocused() bool {
	// Mouse/selection owners finish their own gestures in the mouse route;
	// they do not own new keyboard input after explicit notice focus.
	return m.noticeFocused && m.toastLayout().w != 0
}

func (m *model) toastActionKey(k tea.Key) bool {
	if !m.toastInputFocused() || m.keyOwners[keyIdentity(k)] != nil {
		return false
	}
	action := ""
	mod := k.Mod & ^(tea.ModCapsLock | tea.ModNumLock)
	if !k.IsRepeat {
		switch {
		case mod == 0 && k.Code == tea.KeyEnter:
			action = m.toastChoices()[m.selectedToastButton()].action
		case mod == 0 && k.Code == tea.KeyLeft:
			m.selectToastButton(-1)
		case mod == 0 && (k.Code == tea.KeyRight || k.Code == tea.KeyTab):
			m.selectToastButton(1)
		case k.Code == tea.KeyTab && mod == tea.ModShift:
			m.selectToastButton(-1)
		case mod == 0 && k.Text == "‹":
			action = "prev"
		case mod == 0 && k.Text == "›":
			action = "next"
		case mod == 0 && k.Code == tea.KeyPgUp:
			m.scrollToast(-max(m.toastLayout().capacity, 1))
		case mod == 0 && k.Code == tea.KeyPgDown:
			m.scrollToast(max(m.toastLayout().capacity, 1))
		}
	}
	// Every other key belongs to this focused dialog too, including modifier
	// reports. Keep its press ownership after dismissal or another arrival.
	if !k.IsRepeat {
		m.consumeToastKey(k)
		m.toastAction(action)
	}
	return true
}
func (m *model) applyToast(lines []string) {
	o := m.toastLayout()
	for i, row := range o.rows {
		if y := o.y + i; y < len(lines) {
			lines[y] = spliceRow(lines[y], row, o.x, o.w, m.width)
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
func (m *model) toastEscape(k tea.Key) bool {
	if !m.toastInputFocused() {
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
		m.closeToast()
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
	if _, press := msg.(tea.MouseClickMsg); press && mo.Button == tea.MouseLeft && m.noticeBellHit(mo) {
		m.ownPopupPress(mo.Button)
		m.toggleToast()
		return true
	}
	if !o.contains(mo.X, mo.Y) {
		if _, press := msg.(tea.MouseClickMsg); press && o.w > 0 && mo.Button == tea.MouseLeft {
			m.ownPopupPress(mo.Button)
			m.closeToast()
			return true
		}
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
	m.focusToast()
	for i, button := range o.buttons {
		if mo.Y == button.y && mo.X >= button.x && mo.X < button.x+button.w {
			m.noticeSelected = i
			m.toastAction(button.action)
			return true
		}
	}
	// A body press acquires notice focus without dismissing its content.

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
