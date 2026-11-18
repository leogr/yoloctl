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
	if m.noticeChip() != "" {
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
	if !strings.HasSuffix(m.headerChips(), "fixture ▾") || !strings.HasPrefix(m.headerChips(), "❶! ") {
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

func TestNoticePaletteControlArrowsCloseReview(t *testing.T) {
	for _, code := range []rune{tea.KeyLeft, tea.KeyRight} {
		m := newTestModel(t, 120, 30)
		sleepTab(t, m, 1)
		sleepTab(t, m, 2)
		m.tabs.Select(0)
		m.focusPalette = true
		m.pal.Insert("kept draft")
		m.postNotice(prettylog.KindWarn, "review")
		m.openToast()
		_, _ = m.Update(key(code, tea.ModCtrl, ""))
		if m.tabs.Active != 1 || m.noticeVisible || !m.focusPalette {
			t.Fatalf("palette control arrow did not close review and switch: active=%d visible=%v palette=%v", m.tabs.Active, m.noticeVisible, m.focusPalette)
		}
		if line, _ := m.pal.Line(); line != "kept draft" {
			t.Fatal("tab navigation changed draft")
		}
	}
}

func TestNoticeBellCircledCountBoundaries(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  string
	}{
		{0, ""}, {1, "❶!"}, {9, "❾!"}, {10, "❿!"}, {11, "❿+!"}, {99, "❿+!"}, {100, "❶⓿⓿+!"}, {999, "❶⓿⓿+!"}, {1000, "❶⓿⓿⓿+!"},
	} {
		m := newTestModel(t, 120, 30)
		for range tc.count {
			m.notices = append(m.notices, toastNotice{unread: true})
		}
		if got := m.noticeChip(); got != tc.want {
			t.Errorf("count %d: %q want %q", tc.count, got, tc.want)
		}
	}
}

func TestNoticeTabArrowsRemainChildInput(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		m := newTestModel(t, 120, 30)
		m.standalone = standalone
		_, capture := mouseCapture(t, m, "\x1b[>11u")
		m.postNotice(prettylog.KindWarn, "unfocused")
		for _, code := range []rune{tea.KeyLeft, tea.KeyRight} {
			_, _ = m.Update(key(code, tea.ModCtrl, ""))
			_, _ = m.Update(tea.KeyReleaseMsg{Code: code, Mod: tea.ModCtrl})
		}
		if got := capture(); got == "" || m.noticeFocused {
			t.Fatalf("standalone=%v: live Ctrl+arrows were stolen: %q", standalone, got)
		}
	}
}

func TestNoticeDropdownSharesSpacePickerAnchor(t *testing.T) {
	for _, size := range [][2]int{{24, 9}, {40, 12}, {140, 40}} {
		for _, palette := range []bool{false, true} {
			m := newTestModel(t, size[0], size[1])
			m.focusPalette = palette
			m.postNotice(prettylog.KindWarn, "anchored detail")
			n := m.toastLayout()
			m.spacePicker = &spacePicker{names: []string{"fixture"}}
			s := m.spacePickerOverlay()
			if n.y != m.tabTop()+s.y || n.x+n.w != s.x+s.w || n.y != 1 {
				t.Fatalf("size%v palette%v: notice%+v space%+v", size, palette, n, s)
			}
			if n.x < 0 || n.x+n.w > m.width || n.y+n.h > m.height || len(n.rows) != n.h {
				t.Fatalf("notice exceeds dropdown bounds: %+v", n)
			}
			for _, b := range n.buttons {
				if !n.contains(b.x, b.y) {
					t.Fatalf("button outside dropdown: %+v", b)
				}
			}
		}
	}
}
