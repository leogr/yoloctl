package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestToastQueuePersistentNavigationDismissal(t *testing.T) {
	m := newTestModel(t, 90, 20)
	m.postNotice(prettylog.KindWarn, "first")
	m.postNotice(prettylog.KindError, "second")
	m.postNotice(prettylog.KindNotice, "third")
	m.moveToast(99)
	if m.noticeText() != "third" || !strings.Contains(ansi.Strip(m.View().Content), "3/3") {
		t.Fatal("newest/endpoint/counter lost")
	}
	m.moveToast(-1)
	m.dismissToast()
	if m.noticeText() != "third" || len(m.notices) != 2 {
		t.Fatal("middle dismiss did not select following")
	}
	m.moveToast(-99)
	if m.noticeText() != "first" {
		t.Fatal("previous endpoint")
	}
	_, _ = m.Update(key('a', 0, "a"))
	_, _ = m.Update(tea.PasteMsg{Content: "typing"})
	_, _ = m.Update(prefixTickMsg{})
	m.openSpacePicker()
	m.spacePicker = nil
	if len(m.notices) != 2 || m.noticeText() != "first" {
		t.Fatal("typing/tick/modal discarded a notice")
	}
	m.dismissToast()
	m.dismissToast()
	if m.currentToast() != nil || m.toastLayout().w != 0 {
		t.Fatal("last dismiss left an overlay")
	}
}

func TestToastOriginBadgeIdentityAndExplicitVisits(t *testing.T) {
	m := newTestModel(t, 100, 24)
	a, ap := sleepTab(t, m, 1)
	b, bp := sleepTab(t, m, 1)
	m.ctxSpace = "old-space"
	// Real key forwarding to a closed background session rejects its input.
	ap[0].Session.Close()
	m.sendKey(&imagePasteKey{Session: ap[0].Session}, tea.Key{Code: 'x', Text: "x"}, false)
	if !a.NoticeAttention || b.NoticeAttention || len(m.notices) != 1 {
		t.Fatal("wrapped background error marked the focused tab")
	}
	origin := m.notices[0].origin
	m.ctxSpace = "new-space"
	// A new active-tab notice still needs an explicit visit.
	m.postSurfaceNotice(bp[0].Session, prettylog.KindWarn, "active")
	if !b.NoticeAttention {
		t.Fatal("active notice did not badge")
	}
	for range 3 {
		_ = m.View()
	}
	if !b.NoticeAttention {
		t.Fatal("View cleared attention")
	}
	m.tabs.Select(m.tabs.Index(b))
	if b.NoticeAttention || len(m.notices) != 2 {
		t.Fatal("visit must clear only attention")
	}
	b.Badge = true
	m.postTabNotice(b, prettylog.KindWarn, "again")
	if label := strings.Join(m.tabStripLabels(), " "); !strings.Contains(label, " !") || strings.Contains(label, " •") {
		t.Fatal("notice did not take precedence over bell dot")
	}
	m.dismissToast()
	if !b.NoticeAttention {
		t.Fatal("dismiss cleared unvisited badge")
	}
	m.tabs.CloseTab(a)
	replacement, _ := sleepTab(t, m, 1)
	if replacement.NoticeAttention || m.notices[0].origin != origin || !strings.Contains(origin, "old-space") {
		t.Fatal("closed tab/space relabeled a retained notice")
	}
}

func TestToastInvalidRequestBadgesRequestingTab(t *testing.T) {
	m := newTestModel(t, 100, 24)
	a, ap := sleepTab(t, m, 1)
	b, _ := sleepTab(t, m, 1)
	_, _ = m.Update(tabRequestMsg{from: ap[0].Session, chat: true, req: termio.TabRequest{}})
	if !a.NoticeAttention || b.NoticeAttention || m.tabs.Current() != b {
		t.Fatal("invalid request lost source ownership")
	}
	if !strings.Contains(m.noticeText(), "invalid attach") {
		t.Fatal(m.noticeText())
	}
	b.NoticeAttention = false
	m.postNotice(prettylog.KindError, "global")
	if b.NoticeAttention {
		t.Fatal("global error badged current tab")
	}
}

func TestToastEscapeNavigationAndOwnedKeys(t *testing.T) {
	m := newTestModel(t, 90, 20)
	_, capture := mouseCapture(t, m, "\x1b[>11u")
	m.postNotice(prettylog.KindWarn, "first")
	m.postNotice(prettylog.KindWarn, "second")
	prefixed(m, key('[', 0, "["))
	if m.noticeText() != "first" || m.prefix != nil {
		t.Fatal("prefix [ did not navigate and close")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	// Holding Escape must not dismiss the next notice, even if another UI opens.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, IsRepeat: true})
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEscape})
	if len(m.notices) != 1 || m.noticeText() != "second" {
		t.Fatal("toast key lifecycle leaked or dismissed twice")
	}
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if len(m.notices) != 1 || m.prefix != nil {
		t.Fatal("menu Escape dismissed toast")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEscape})
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if got := capture(); got != "\x1b[27u" {
		t.Fatal("Escape with no toast failed to reach child")
	}
}

func TestToastMouseGeometryOwnershipAndScroll(t *testing.T) {
	for _, pixels := range []bool{false, true} {
		t.Run(map[bool]string{false: "cells", true: "pixels"}[pixels], func(t *testing.T) {
			m := newTestModel(t, 90, 24)
			_, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
			m.hostMouseEncoding.pixels = pixels
			m.hostGeometry.width = 8
			m.hostGeometry.height = 16
			press := func(x, y int) tea.MouseClickMsg {
				if pixels {
					x = x * 8
					y = y * 16
				}
				return click(x, y)
			}
			release := func(x, y int) tea.MouseReleaseMsg {
				if pixels {
					x = x * 8
					y = y * 16
				}
				return releaseAt(x, y)
			}
			// A child-owned press before arrival retains its original release.
			_, _ = m.Update(press(1, barRows+15))
			m.postNotice(prettylog.KindWarn, strings.Repeat("long 界 text ", 200))
			o := m.toastLayout()
			_, _ = m.Update(release(o.x+2, o.y+2))

			m.postNotice(prettylog.KindWarn, "close this one")
			o = m.toastLayout()
			_, _ = m.Update(press(o.closeX, o.controlY))
			m.postNotice(prettylog.KindWarn, "new arrival")
			_, _ = m.Update(release(1, barRows+15))
			if got := capture(); strings.Count(got, "M") != 1 || strings.Count(got, "m") != 1 {
				t.Fatalf("gesture ownership: %q", got)
			}
			m.moveToast(-1)
			prefixed(m, key(tea.KeyPgDown, 0, ""))
			if m.currentToast().scroll == 0 {
				t.Fatal("prefix scroll did not scroll overflow")
			}
		})
	}
}

func TestToastModalSuppressionAndViewport(t *testing.T) {
	m := newTestModel(t, 90, 20)
	m.postNotice(prettylog.KindWarn, "persistent\x1b]52;c;bad\a界")
	original := m.noticeText()
	m.prefix = &prefixState{}
	if m.toastLayout().w != 0 || m.toastEscape(tea.Key{Code: tea.KeyEscape}) {
		t.Fatal("suppressed overlay retained input geometry")
	}
	m.prefix = nil
	for _, size := range [][2]int{{90, 20}, {25, 6}, {24, 6}, {10, 3}, {1, 1}, {0, 0}} {
		m.width, m.height = size[0], size[1]
		o := m.toastLayout()
		if o.x < 0 || o.y < 0 || o.x+o.w > m.width || o.w > 0 && o.y+o.h > m.height {
			t.Fatalf("layout outside viewport: %+v", o)
		}
		for _, row := range o.rows {
			if ansi.StringWidth(row) > o.w || strings.Contains(row, "\x1b]52;") {
				t.Fatal("unsafe/too-wide toast")
			}
		}
	}
	if m.noticeText() != original {
		t.Fatal("resize discarded notice")
	}
}

func TestStandaloneToastPreservesScreenPrefixAndStartupResult(t *testing.T) {
	m := newTestModel(t, 90, 15)
	m.standalone = true
	m.focusPalette = false
	s, capture := mouseCapture(t, m, "\x1b[>11u")
	_, _ = s.Write([]byte("\x1b[15;1HBOTTOM"))
	m.postNotice(prettylog.KindWarn, "first")
	m.postNotice(prettylog.KindError, "second")
	_, _ = m.Update(key('y', tea.ModCtrl, ""))
	_, _ = m.Update(key('[', 0, "["))
	if m.noticeText() != "first" {
		t.Fatal("standalone command leaked prefix")
	}
	_, _ = m.Update(key('a', 0, "a"))
	if len(m.notices) != 2 {
		t.Fatal("ordinary standalone typing dismissed notice")
	}
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(lines[14], "BOTTOM") {
		t.Fatal("last row overwritten")
	}
	failure := errors.New("startup failure")
	m.startNotice(failure)
	m.dismissToast()
	if !errors.Is(m.attachError, failure) {
		t.Fatal("dismiss erased startup error")
	}
	_, cmd := m.Update(key('y', tea.ModCtrl, ""))
	_ = cmd
	_, cmd = m.Update(key('d', 0, "d"))
	if cmd == nil {
		t.Fatal("toast intercepted standalone detach")
	}
	if got := capture(); got != "\x1b[97u" {
		t.Fatalf("standalone key routes: %q", got)
	}
}

func TestToastHeaderCursorAndControls(t *testing.T) {
	m := newTestModel(t, 90, 20)
	s, _ := mouseCapture(t, m, "")
	m.postNotice(prettylog.KindError, "HEADER-MUST-STAY-CLEAN")
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if strings.Contains(lines[0], "HEADER-MUST") || strings.Contains(lines[1], "HEADER-MUST") {
		t.Fatal("notice leaked into header/strip")
	}
	_, _ = s.Write([]byte("\x1b[1;1H"))
	if m.View().Cursor == nil {
		t.Fatal("toast hid an uncovered cursor")
	}
	o := m.toastLayout()
	_, _ = fmt.Fprintf(s, "\x1b[1;%dH", o.x+1)
	if m.View().Cursor != nil {
		t.Fatal("painted toast did not hide covered cursor")
	}
	m.postNotice(prettylog.KindWarn, "second")
	o = m.toastLayout()
	_, _ = m.Update(click(o.prevX, o.controlY))
	_, _ = m.Update(releaseAt(o.prevX, o.controlY))
	if m.noticeIndex != 0 {
		t.Fatal("previous mouse control missed")
	}
	o = m.toastLayout()
	_, _ = m.Update(click(o.nextX, o.controlY))
	_, _ = m.Update(releaseAt(o.nextX, o.controlY))
	if m.noticeIndex != 1 {
		t.Fatal("next mouse control missed")
	}
	before := len(m.notices)
	_, _ = m.Update(click(0, 0))
	_, _ = m.Update(releaseAt(0, 0))
	if len(m.notices) != before {
		t.Fatal("outside click dismissed notice")
	}
}
