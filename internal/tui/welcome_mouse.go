package tui

import (
	"encoding/base64"
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

type welcomeDrag struct {
	tip            welcomeTip
	visit          uint64
	size           image.Point
	top            int
	start, end     image.Point
	command, moved bool
}

func (m *model) welcomeTextHit(mo tea.Mouse) (text, command bool) {
	if !m.welcomeIdle() || mo.Y >= m.height || mo.X >= m.width {
		return false, false
	}
	rows, commandStart, commandEnd := m.welcomeLayout()
	y := mo.Y - m.tabTop()
	if y < 0 || y >= len(rows) {
		return false, false
	}
	plain := ansi.Strip(rows[y])
	hit := mo.X >= len(plain)-len(strings.TrimLeft(plain, " ")) &&
		mo.X < ansi.StringWidth(strings.TrimRight(plain, " "))
	return hit, hit && commandStart <= y && y < commandEnd
}

func (m *model) startWelcomeDrag(mo tea.Mouse) bool {
	hit, command := m.welcomeTextHit(mo)
	if !hit || mo.Mod != 0 {
		return false
	}
	at := image.Pt(mo.X, mo.Y-m.tabTop())
	m.welcome.drag = &welcomeDrag{
		tip: m.welcome.tip, visit: m.welcome.visit,
		size: image.Pt(m.width, m.height), top: m.tabTop(),
		start: at, end: at, command: command,
	}
	m.ownPopupPress(tea.MouseLeft)
	return true
}

func (m *model) sameWelcome(drag *welcomeDrag) bool {
	return m.welcomeIdle() && m.welcome.tip == drag.tip && m.welcome.visit == drag.visit &&
		drag.size == image.Pt(m.width, m.height) && drag.top == m.tabTop()
}

// follow the original mouse owner even if a popup appears during the drag.
func (m *model) continueWelcomeDrag(msg tea.Msg, mo tea.Mouse) (tea.Cmd, bool) {
	drag := m.welcome.drag
	if drag == nil {
		return nil, false
	}
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if mo.Button != tea.MouseLeft {
			return nil, false
		}
	case tea.MouseReleaseMsg:
		if mo.Button != tea.MouseLeft && mo.Button != tea.MouseNone {
			return nil, false
		}
	default:
		return nil, false
	}

	if m.sameWelcome(drag) {
		drag.end = image.Pt(mo.X, mo.Y-drag.top)
		drag.moved = drag.moved || drag.end != drag.start
	}
	if _, released := msg.(tea.MouseReleaseMsg); !released {
		return nil, true
	}
	m.welcome.drag = nil
	delete(m.mouseOwners, tea.MouseLeft)
	owned := mo.Button == tea.MouseLeft
	if !m.sameWelcome(drag) {
		return nil, owned
	}
	if !drag.moved {
		if drag.command && m.canFillWelcome() && !m.toastLayout().contains(mo.X, mo.Y) &&
			m.browserDialog == nil && m.confirm == nil && m.picker == nil && m.spacePicker == nil && m.activeNotice() == nil {
			m.fillWelcome()
		} else if m.noticeVisible && !m.toastLayout().contains(mo.X, mo.Y) {
			m.closeToast()
		}
		return nil, owned
	}
	rows, _, _ := m.welcomeLayout()
	var parts []string
	for y, span := range drag.spans(rows) {
		if span.End > span.Start {
			parts = append(parts, ansi.Cut(ansi.Strip(rows[y]), span.Start, span.End))
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		return nil, owned
	}
	msg = m.osc52HostMsg(termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte(text))})
	return func() tea.Msg { return msg }, owned
}

func (drag *welcomeDrag) spans(rows []string) []terminal.SelectionSpan {
	a, b := drag.start, drag.end
	if a == b {
		return nil
	}
	if a.Y > b.Y || a.Y == b.Y && a.X > b.X {
		a, b = b, a
	}
	spans := make([]terminal.SelectionSpan, len(rows))
	for y := max(a.Y, 0); y <= b.Y && y < len(rows) && y < drag.size.Y-drag.top; y++ {
		lo, hi := 0, ansi.StringWidth(ansi.Strip(rows[y]))
		if y == a.Y {
			lo = max(a.X, lo)
		}
		if y == b.Y {
			hi = min(b.X+1, hi)
		}
		spans[y] = terminal.SelectionSpan{Start: lo, End: hi}
	}
	return spans
}
