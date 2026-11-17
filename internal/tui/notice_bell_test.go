package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestNoticeBellCtrlNOpensFocused(t *testing.T) {
	m := newTestModel(t, 120, 30)
	m.postNotice(prettylog.KindWarn, "retained")
	m.focusPalette = true
	_, _ = m.Update(key('n', tea.ModCtrl, ""))
	if !m.noticeFocused {
		t.Fatal("palette Ctrl+N did not focus notices")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "(!0)") {
		t.Fatal("viewed notice did not mark bell read")
	}
}

func TestNoticeBellArrowSelectsDismiss(t *testing.T) {
	m := newTestModel(t, 120, 30)
	tab, _ := sleepTab(t, m, 1)
	m.postTabNotice(tab, prettylog.KindWarn, "problem")
	o := m.toastLayout()
	_, _ = m.Update(click(o.x+3, o.y+1))
	_, _ = m.Update(releaseAt(o.x+3, o.y+1))
	_, _ = m.Update(key(tea.KeyRight, 0, ""))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyRight})
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if len(m.notices) != 0 {
		t.Fatal("right arrow did not select Dismiss")
	}
}

func TestNoticeBellExpiryRetainsQueueAndFencesOldTimers(t *testing.T) {
	old := clock
	now := time.Unix(1000, 0)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = old })
	m := newTestModel(t, 120, 30)
	m.postNotice(prettylog.KindWarn, "first")
	first := m.noticeEpoch
	if !m.noticeVisible || m.noticeFocused || m.unreadNotices() != 1 {
		t.Fatal("arrival is not an unread unfocused popup")
	}
	now = now.Add(7 * time.Second)
	_, _ = m.Update(noticeExpireMsg(first))
	if !m.noticeVisible {
		t.Fatal("popup closed before eight seconds")
	}
	m.postNotice(prettylog.KindWarn, "second")
	second := m.noticeEpoch
	now = now.Add(2 * time.Second)
	_, _ = m.Update(noticeExpireMsg(first))
	if !m.noticeVisible {
		t.Fatal("old expiry closed a newer arrival")
	}
	now = now.Add(6 * time.Second)
	_, _ = m.Update(noticeExpireMsg(second))
	if m.noticeVisible || len(m.notices) != 2 || m.unreadNotices() != 2 {
		t.Fatal("expiry erased history/unread state")
	}
	m.focusPalette = true
	m.pal.Insert("kept draft")
	m.openToast()
	if !m.noticeFocused || !m.noticeVisible || m.unreadNotices() != 1 || !m.noticeUntil.IsZero() {
		t.Fatal("manual open failed to mark current read/stop auto expiry")
	}
	m.noticeSelected = 1
	index, selected := m.noticeIndex, m.noticeSelected
	m.postNotice(prettylog.KindError, "arrived during review")
	_, _ = m.Update(noticeExpireMsg(second))
	if !m.noticeFocused || !m.noticeVisible || m.noticeIndex != index || m.noticeSelected != selected || m.unreadNotices() != 2 {
		t.Fatal("arrival/old expiry disturbed explicit review")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if m.noticeVisible || len(m.notices) != 3 || !m.focusPalette {
		t.Fatal("Esc did not close/restore palette without erasing history")
	}
	if line, _ := m.pal.Line(); line != "kept draft" {
		t.Fatal("popup changed the palette draft")
	}
}

func TestNoticeBellMouseOpenOutsideCloseAndSiblingPopup(t *testing.T) {
	m := newTestModel(t, 120, 30)
	m.ctxSpace = "fixture"
	_, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	m.postNotice(prettylog.KindWarn, "first")
	x := m.width - 1 - ansi.StringWidth(m.headerChips())
	if !strings.HasSuffix(m.headerChips(), "fixture ▾") || !strings.HasPrefix(m.headerChips(), "(!1) ") {
		t.Fatal("bell is not before the space switch")
	}
	_, _ = m.Update(click(x, 0))
	_, _ = m.Update(releaseAt(x, 0))
	if !m.noticeFocused || m.unreadNotices() != 0 {
		t.Fatal("bell did not focus/mark read")
	}
	_, _ = m.Update(click(1, m.height-1))
	_, _ = m.Update(releaseAt(1, m.height-1))
	if m.noticeVisible || len(m.notices) != 1 {
		t.Fatal("outside click did not close and retain")
	}
	// First outside gesture is consumed, like the space switch; a later one goes to the pane.
	_, _ = m.Update(click(1, m.height-1))
	_, _ = m.Update(releaseAt(1, m.height-1))
	got := capture()
	if strings.Count(got, "M") != 1 || strings.Count(got, "m") != 1 {
		t.Fatalf("outside popup click-through or lost next gesture: %q", got)
	}
	m.openToast()
	m.openSpacePicker()
	if m.noticeVisible || m.spacePicker == nil {
		t.Fatal("space switch did not replace notice popup")
	}
	m.openToast()
	if !m.noticeFocused || m.spacePicker != nil {
		t.Fatal("notice open did not replace space popup")
	}
}

func TestNoticeBellCtrlNDoesNotInterceptAgentOrStandalone(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(map[bool]string{false: "pane", true: "standalone"}[standalone], func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			m.standalone = standalone
			_, capture := mouseCapture(t, m, "\x1b[>11u")
			m.postNotice(prettylog.KindWarn, "auto")
			_, _ = m.Update(key('n', tea.ModCtrl, ""))
			_, _ = m.Update(tea.KeyReleaseMsg{Code: 'n', Mod: tea.ModCtrl})
			if m.noticeFocused || capture() == "" {
				t.Fatal("Ctrl+N was stolen outside palette")
			}
		})
	}
}

func TestNoticeBellButtonsTabPagesAndNoPrefixRoute(t *testing.T) {
	m := newTestModel(t, 120, 30)
	tab, _ := sleepTab(t, m, 1)
	m.postTabNotice(tab, prettylog.KindWarn, "first")
	m.postTabNotice(tab, prettylog.KindError, "second")
	m.openToast()
	_, _ = m.Update(key(tea.KeyTab, 0, ""))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyTab})
	if m.toastChoices()[m.selectedToastButton()].action != "dismiss" {
		t.Fatal("Tab cannot select Dismiss")
	}
	_, _ = m.Update(key('‹', 0, "‹"))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: '‹'})
	if m.noticeIndex != 0 || m.unreadNotices() != 0 {
		t.Fatal("paging did not visit/mark read")
	}
	m.closeToast()
	prefixed(m, key('[', 0, "["))
	if m.noticeFocused || m.noticeIndex != 0 || m.prefix == nil {
		t.Fatal("removed prefix notice route still acts")
	}
}

func TestNoticeBellStartupExpiryAndFocusedCursor(t *testing.T) {
	old := clock
	now := time.Unix(1000, 0)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = old })
	m := newTestModel(t, 120, 30)
	m.focusPalette = true
	m.postNotice(prettylog.KindWarn, "startup")
	now = now.Add(noticeAutoTime)
	cmd := m.noticeExpiryCmd()
	if cmd == nil {
		t.Fatal("startup notice has no expiry command")
	}
	_, _ = m.Update(cmd())
	if m.noticeVisible || len(m.notices) != 1 {
		t.Fatal("startup expiry did not retain hidden history")
	}
	m.openToast()
	if m.hostCursor() != nil {
		t.Fatal("focused popup leaves a pane/editor cursor")
	}
	m.closeToast()
	if m.hostCursor() == nil {
		t.Fatal("closing popup did not restore editor cursor")
	}
}

func TestNoticeBellCompactKeyboardMatchesPaintedControl(t *testing.T) {
	for _, size := range [][2]int{{24, 9}, {24, 10}, {32, 9}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := newTestModel(t, size[0], size[1])
			tab, _ := sleepTab(t, m, 1)
			m.postTabNotice(tab, prettylog.KindWarn, "problem")
			m.openToast()
			o := m.toastLayout()
			if len(o.buttons) != 1 || o.buttons[0].action != "dismiss" {
				t.Fatalf("not compact: %+v", o.buttons)
			}
			for _, code := range []rune{tea.KeyLeft, tea.KeyRight, tea.KeyTab} {
				_, _ = m.Update(key(code, 0, ""))
				_, _ = m.Update(tea.KeyReleaseMsg{Code: code})
				if choice := m.toastChoices()[m.selectedToastButton()].action; choice != "dismiss" {
					t.Fatalf("invisible action selected: %s", choice)
				}
			}
			_, _ = m.Update(key(tea.KeyEnter, 0, ""))
			if len(m.notices) != 0 || m.noticeVisible {
				t.Fatal("Enter did not run sole painted Dismiss")
			}
		})
	}
}
