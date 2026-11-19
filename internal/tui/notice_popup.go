package tui

import (
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

const noticeAutoTime = 8 * time.Second

type noticeExpireMsg uint64

type noticeChoice struct {
	action, label string
	disabled      bool
}

// Startup may emit a notice before Run assigns its program. Init supplies that
// one expiry; later arrivals schedule directly through the current program.
func (m *model) noticeExpiryCmd() tea.Cmd {
	if !m.noticeVisible || m.noticeFocused || m.noticeUntil.IsZero() || m.noticeTimer != nil {
		return nil
	}
	epoch, delay := m.noticeEpoch, max(m.noticeUntil.Sub(clock()), 0)
	return tea.Tick(delay, func(time.Time) tea.Msg { return noticeExpireMsg(epoch) })
}

func (m *model) allToastChoices() []noticeChoice {
	var choices []noticeChoice
	if m.toastTab(m.currentToast()) != nil {
		choices = append(choices, noticeChoice{action: "visit", label: "Go to tab"})
	}
	return append(choices, noticeChoice{action: "dismiss", label: "Dismiss"}, noticeChoice{action: "prev", label: "‹ Previous", disabled: m.noticeIndex == 0}, noticeChoice{action: "next", label: "Next ›", disabled: m.noticeIndex == len(m.notices)-1})
}
func (m *model) selectedToastButtonFor(choices []noticeChoice) int {
	if m.noticeSelected < 0 || m.noticeSelected >= len(choices) || choices[m.noticeSelected].disabled {
		m.noticeSelected = 0
	}
	return m.noticeSelected
}

// Keyboard actions are exactly the controls that paint and accept clicks.
// Compact geometry therefore exposes only its visible Dismiss control.
func (m *model) toastChoices() []noticeChoice {
	all := m.allToastChoices()
	var visible []noticeChoice
	for _, button := range m.toastLayout().buttons {
		for _, choice := range all {
			if choice.action == button.action {
				visible = append(visible, choice)
				break
			}
		}
	}
	return visible
}
func (m *model) selectedToastButton() int { return m.selectedToastButtonFor(m.toastChoices()) }

func (m *model) selectToastButton(delta int) {
	choices := m.toastChoices()
	selected := m.selectedToastButton()
	for range len(choices) {
		selected = (selected + delta + len(choices)) % len(choices)
		if !choices[selected].disabled {
			m.noticeSelected = selected
			return
		}
	}
}
func (m *model) stopNoticeTimer() {
	m.noticeEpoch++
	m.noticeUntil = time.Time{}
	if m.noticeTimer != nil {
		m.noticeTimer.Stop()
		m.noticeTimer = nil
	}
}
func (m *model) focusToast() {
	if n := m.currentToast(); n != nil {
		if !m.noticeFocused {
			m.noticeReturnPalette = m.focusPalette
		}
		m.noticeVisible, m.noticeFocused = true, true
		m.stopNoticeTimer()
		n.unread = false
	}
}
func (m *model) openToast() {
	if m.currentToast() == nil {
		return
	}
	if _, ok := m.beginPopup(); !ok {
		return
	}
	m.noticeSelected = 0
	m.focusToast()
}
func (m *model) toggleToast() {
	if m.noticeVisible && m.noticeFocused && !m.toastSuppressed() {
		m.closeToast()
	} else {
		m.openToast()
	}
}
func (m *model) closeToast() {
	if m.noticeFocused {
		m.restorePopupFocus(m.noticeReturnPalette)
	}
	m.noticeVisible, m.noticeFocused = false, false
	m.noticeSelected = 0
	m.stopNoticeTimer()
}
func (m *model) ownPopupPress(button tea.MouseButton) {
	if m.mouseOwners == nil {
		m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
	}
	m.mouseOwners[button] = mouseOwner{}
}
func (m *model) unreadNotices() int {
	count := 0
	for _, n := range m.notices {
		if n.unread {
			count++
		}
	}
	return count
}

// Notice counts use the same circled glyphs as tab numbers.
func (m *model) noticeChip() string {
	count := len(m.notices)
	if m.standalone || count == 0 {
		return ""
	}
	label := circledNumber(count)
	if count > 10 {
		bucket := 10
		for count/10 >= bucket {
			bucket *= 10
		}
		label = circledNumber(10)
		if bucket > 10 {
			label = ""
			for _, digit := range strconv.Itoa(bucket) {
				if digit == '0' {
					label += "⓿"
				} else {
					label += circledNumber(int(digit - '0'))
				}
			}
		}
		label += "+"
	}
	return runewidth.Truncate(label+"!", max(m.width-len([]rune(brandPrefix()))-3, 0), "")
}
func (m *model) headerChips() string {
	bell, space := m.noticeChip(), m.contextChip()
	if bell != "" && space != "" {
		return bell + " " + space
	}
	return bell + space
}
func (m *model) headerChipsStyled() string {
	bell, space := m.noticeChip(), m.contextChip()
	style := cDim
	if m.unreadNotices() > 0 || m.noticeFocused {
		style = cOrange + cBold
	}
	result := style + bell + cReset
	if bell != "" && space != "" {
		result += " "
	}
	return result + cBlue + cBold + space + cReset
}
func (m *model) noticeBellHit(mo tea.Mouse) bool {
	bell := m.noticeChip()
	x := m.width - 1 - runewidth.StringWidth(m.headerChips())
	return bell != "" && mo.Y == 0 && mo.X >= x && mo.X < x+runewidth.StringWidth(bell)
}
