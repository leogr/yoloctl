package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestNoticeDialogApprovedLayout(t *testing.T) {
	m := newTestModel(t, 140, 40)
	m.ctxSpace = "qa"
	m.postNotice(prettylog.KindWarn, "a clear detail message with ordinary words that should wrap without splitting any normal words")
	m.postNotice(prettylog.KindWarn, "a clear detail message with ordinary words that should wrap without splitting any normal words")
	o := m.toastLayout()
	text := ansi.Strip(strings.Join(o.rows, "\n"))
	if o.w != 98 || o.x != (140-o.w)/2 || o.y <= m.chromeRows() {
		t.Fatalf("not centered70%%: %+v", o)
	}
	for _, part := range []string{"Notice 1 of 1 · ×2", "Detail", "When", "Dismiss", "‹ Previous", "Next ›", "click or prefix+[ to focus · Enter · Esc"} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %q:\n%s", part, text)
		}
	}
	if strings.Contains(text, "Go to tab") {
		t.Fatal("global notice gained a tab action")
	}
	for _, row := range o.rows {
		if ansi.StringWidth(row) != o.w {
			t.Errorf("row width %d not %d", ansi.StringWidth(row), o.w)
		}
	}
}

func TestNoticeDialogEnterDismissesGlobalButDoesNotTypeIntoChild(t *testing.T) {
	m := newTestModel(t, 100, 28)
	_, capture := mouseCapture(t, m, "\x1b[>11u")
	m.postNotice(prettylog.KindWarn, "global")
	prefixed(m, key('[', 0, "["))
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEnter})
	if len(m.notices) != 0 || capture() != "" {
		t.Fatalf("Enter did not own first action: notices%d input%q", len(m.notices), capture())
	}
}

func TestNoticeDialogVisitKeepsQueueAndExactOwner(t *testing.T) {
	m := newTestModel(t, 120, 35)
	a, _ := sleepTab(t, m, 1)
	b, _ := sleepTab(t, m, 1)
	m.postTabNotice(a, prettylog.KindWarn, "background problem")
	prefixed(m, key('[', 0, "["))
	text := ansi.Strip(strings.Join(m.toastLayout().rows, "\n"))
	if !strings.Contains(text, "Go to tab") {
		t.Fatal(text)
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.tabs.Current() != a || a.NoticeAttention || len(m.notices) != 1 {
		t.Fatal("visit altered queue or target")
	}
	// Closing the captured owner removes its action; a replacement must not
	// inherit the old action or badge, even if it uses the same tab position.
	m.tabs.CloseTab(a)
	m.tabs.Select(m.tabs.Index(b))
	if strings.Contains(ansi.Strip(strings.Join(m.toastLayout().rows, "\n")), "Go to tab") {
		t.Fatal("closed owner still actionable")
	}
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEnter})
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if len(m.notices) != 0 || m.tabs.Current() != b {
		t.Fatal("replacement tab adopted old action")
	}
}

func TestNoticeDialogWordWrapAndFieldIndent(t *testing.T) {
	m := newTestModel(t, 100, 36)
	detail := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron"
	m.postNotice(prettylog.KindWarn, detail)
	var chunks []string
	copying := false
	for _, row := range m.toastLayout().rows {
		row = ansi.Strip(row)
		if strings.Contains(row, "When") {
			break
		}
		if strings.Contains(row, "Detail") {
			copying = true
		}
		if copying {
			// Two frame/inset cells plus ten label cells: continuation values
			// must stay in this column, and each word must remain intact.
			chunks = append(chunks, strings.TrimSpace(ansi.Cut(row, 12, ansi.StringWidth(row)-2)))
		}
	}
	if strings.Join(chunks, " ") != detail {
		t.Fatalf("field wrap lost/split words: %q", chunks)
	}
}

func TestNoticeDialogRetainsAlreadyOwnedEnterRelease(t *testing.T) {
	m := newTestModel(t, 100, 28)
	_, capture := mouseCapture(t, m, "\x1b[>11u")
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	m.postNotice(prettylog.KindWarn, "arrived during key")
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEnter})
	if len(m.notices) != 1 || !strings.Contains(capture(), ";1:3u") {
		t.Fatal("notice stole a held child key")
	}
}

func TestNoticeDialogWrappedButtonsUsePaintedCoordinates(t *testing.T) {
	m := newTestModel(t, 32, 24)
	m.postNotice(prettylog.KindWarn, "first")
	m.postNotice(prettylog.KindWarn, "second")
	o := m.toastLayout()
	for _, button := range o.buttons {
		if button.action == "prev" {
			_, _ = m.Update(click(button.x, button.y))
			_, _ = m.Update(releaseAt(button.x, button.y))
		}
	}
	if m.noticeIndex != 0 {
		t.Fatal("wrapped previous button missed")
	}
	o = m.toastLayout()
	for _, button := range o.buttons {
		if button.action == "next" {
			_, _ = m.Update(click(button.x, button.y))
			_, _ = m.Update(releaseAt(button.x, button.y))
		}
	}
	if m.noticeIndex != 1 {
		t.Fatal("wrapped next button missed")
	}
}

func TestNoticeDialogCapturedFieldsAndPreview(t *testing.T) {
	m := newTestModel(t, 140, 40)
	m.ctxSpace = "wrong-default"
	args := []string{"box", "start", "copy-dst"}
	s := lifecycleTargetSession(t, args, "failed-attach")
	p := &panes.Pane{Session: s, Args: args, Command: "sandbox@821d33b833bd: /workdir/Users/leonardo.grasso/Code/github.com/leogr"}
	tab := tabs.New(p)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	for range 3 {
		m.postSurfaceNotice(s, prettylog.KindWarn, "connection to holder closed without an exit status")
	}
	m.notices[0].when = time.Date(2026, 11, 16, 15, 12, 4, 0, time.UTC)
	m.postNotice(prettylog.KindNotice, "another notice")
	m.noticeIndex = 0
	out := ansi.Strip(strings.Join(m.toastLayout().rows, "\n"))
	for _, part := range []string{"Notice 1 of 2 · ×3", "Box", "copy-dst   (space resolved)", "Tab", "Detail", "When", "15:12:04", "Go to tab"} {
		if !strings.Contains(out, part) {
			t.Fatalf("missing captured field %q:\n%s", part, out)
		}
	}
	if strings.Contains(out, "wrong-default") {
		t.Fatal("box acquired a mutable default space")
	}
	m.ctxSpace = "changed"
	if got := ansi.Strip(strings.Join(m.toastLayout().rows, "\n")); got != out {
		t.Fatal("later context relabeled captured notice")
	}
	t.Logf("Rendered notice fixture:\n%s", out)
}

func TestNoticeDialogShortTabOwnedGeometry(t *testing.T) {
	for _, size := range [][2]int{{24, 9}, {24, 10}, {32, 9}, {32, 10}, {40, 9}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := newTestModel(t, size[0], size[1])
			tab, _ := sleepTab(t, m, 1)
			m.postTabNotice(tab, prettylog.KindWarn, "short-body")
			o := m.toastLayout()
			if o.capacity < 1 || len(o.rows) != o.h || o.y+o.h > m.height {
				t.Fatalf("unusable paint geometry: %+v", o)
			}
			for _, b := range o.buttons {
				if !o.contains(b.x, b.y) || !o.contains(b.x+b.w-1, b.y) {
					t.Fatalf("button outside rectangle: %+v %+v", b, o)
				}
			}
			found := false
			for scroll := 0; scroll <= o.maxScroll; scroll++ {
				m.notices[0].scroll = scroll
				if strings.Contains(ansi.Strip(strings.Join(m.toastLayout().rows, "\n")), "short-body") {
					found = true
				}
			}
			if !found {
				t.Fatal("scrolling never reveals the notice detail")
			}
		})
	}
}

func TestNoticeDialogUnfocusedKeysStayWithAgent(t *testing.T) {
	for _, k := range []tea.Key{{Code: tea.KeyEnter}, {Code: tea.KeyEscape}, {Code: tea.KeyLeft}, {Code: tea.KeyRight}, {Code: 'a', Text: "a"}} {
		t.Run(fmt.Sprintf("key%d", k.Code), func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			_, capture := mouseCapture(t, m, "\x1b[>11u")
			m.postNotice(prettylog.KindWarn, "first")
			m.postNotice(prettylog.KindWarn, "second")
			_, _ = m.Update(tea.KeyPressMsg(k))
			if m.noticeFocused || len(m.notices) != 2 || m.noticeIndex != 1 || capture() == "" {
				t.Fatal("unfocused notice stole the agent key")
			}
		})
	}
}

func TestNoticeDialogClickFocusAndReturn(t *testing.T) {
	m := newTestModel(t, 100, 28)
	_, capture := mouseCapture(t, m, "\x1b[>11u")
	m.postNotice(prettylog.KindWarn, "first")
	o := m.toastLayout()
	before := o.rows[0]
	_, _ = m.Update(click(o.x+3, o.y+1))
	_, _ = m.Update(releaseAt(o.x+3, o.y+1))
	if !m.noticeFocused || len(m.notices) != 1 || m.toastLayout().rows[0] == before {
		t.Fatal("click did not focus/highlight without dismissal")
	}
	_, _ = m.Update(click(1, m.height-1))
	_, _ = m.Update(releaseAt(1, m.height-1))
	if m.noticeFocused {
		t.Fatal("pane click did not return focus")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEscape})
	if len(m.notices) != 1 || capture() == "" {
		t.Fatal("unfocused Escape did not reach agent")
	}
	prefixed(m, key('[', 0, "["))
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if len(m.notices) != 0 || m.noticeFocused {
		t.Fatal("focused Escape did not dismiss and return focus")
	}
	m.postNotice(prettylog.KindWarn, "new arrival")
	if m.noticeFocused {
		t.Fatal("new arrival inherited notice focus")
	}
}

func TestNoticeDialogFocusedInputIsExclusive(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone%t", standalone), func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			m.standalone = standalone
			_, capture := mouseCapture(t, m, "\x1b[>11u")
			m.postNotice(prettylog.KindWarn, "focused")
			o := m.toastLayout()
			_, _ = m.Update(click(o.x+3, o.y+1))
			_, _ = m.Update(key('h', 0, "h")) // the notice owns this held click, not the child
			_, _ = m.Update(tea.KeyReleaseMsg{Code: 'h'})
			_, _ = m.Update(releaseAt(o.x+3, o.y+1))
			for _, k := range []tea.Key{{Code: 'a', Text: "a"}, {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyEnter, Mod: tea.ModShift}, {Code: tea.KeyEscape, Mod: tea.ModAlt}, {Code: tea.KeyLeftShift}, {Code: tea.KeyLeftAlt}, {Code: tea.KeyLeft, Mod: tea.ModCtrl}} {
				_, _ = m.Update(tea.KeyPressMsg(k))
				_, _ = m.Update(tea.KeyReleaseMsg(k))
			}
			_, _ = m.Update(tea.PasteMsg{Content: "private pasted text"})
			if len(m.notices) != 1 || !m.noticeFocused {
				t.Fatal("focused input changed the notice")
			}
			// A press owned by the notice stays owned through dismissal/new arrivals.
			_, _ = m.Update(key('z', 0, "z"))
			_, _ = m.Update(key(tea.KeyEscape, 0, ""))
			m.postNotice(prettylog.KindWarn, "new unfocused arrival")
			_, _ = m.Update(tea.KeyPressMsg{Code: 'z', Text: "z", IsRepeat: true})
			_, _ = m.Update(tea.KeyReleaseMsg{Code: 'z'})
			_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyEscape})
			if m.noticeFocused || len(m.notices) != 1 {
				t.Fatal("owned repeats/releases changed the new notice")
			}
			_, _ = m.Update(tea.PasteMsg{Content: "unfocused paste"})
			if got := capture(); got != "unfocused paste" {
				t.Fatalf("focused input escaped or unfocused paste was stolen: %q", got)
			}
		})
	}
}

func TestNoticeDialogKeepsChildOwnedInputAfterFocus(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone%t", standalone), func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			m.standalone = standalone
			_, capture := mouseCapture(t, m, "\x1b[>11u")
			_, _ = m.Update(key('a', 0, "a"))
			m.postNotice(prettylog.KindWarn, "arrived")
			o := m.toastLayout()
			_, _ = m.Update(click(o.x+3, o.y+1))
			_, _ = m.Update(releaseAt(o.x+3, o.y+1))
			_, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a", IsRepeat: true})
			_, _ = m.Update(tea.KeyReleaseMsg{Code: 'a'})
			if !strings.Contains(capture(), ";1:2u") || !strings.Contains(capture(), ";1:3u") {
				t.Fatalf("child-owned key was stolen: %q", capture())
			}
			if !m.noticeFocused {
				t.Fatal("held child completion changed notice focus")
			}
		})
	}
}

func TestNoticeDialogFocusedPrefixRetainsPrecedence(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone%t", standalone), func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			m.standalone = standalone
			mouseCapture(t, m, "\x1b[>11u")
			m.postNotice(prettylog.KindWarn, "first")
			m.postNotice(prettylog.KindWarn, "second")
			o := m.toastLayout()
			_, _ = m.Update(click(o.x+3, o.y+1))
			_, _ = m.Update(releaseAt(o.x+3, o.y+1))
			if standalone {
				_, _ = m.Update(key('y', tea.ModCtrl, ""))
				_, _ = m.Update(key('[', 0, "["))
			} else {
				prefixed(m, key('[', 0, "["))
			}
			if m.noticeIndex != 0 || !m.noticeFocused || m.attachPrefix != nil || m.prefix != nil {
				t.Fatal("focused notice swallowed the existing prefix route")
			}
		})
	}
}

func TestNoticeDialogKeepsMouseGestureButOwnsNewInput(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(map[bool]string{false: "dashboard", true: "standalone"}[standalone], func(t *testing.T) {
			m := newTestModel(t, 100, 28)
			m.standalone = standalone
			_, capture := mouseCapture(t, m, "\x1b[>11u\x1b[?1002h\x1b[?1006h")
			m.postNotice(prettylog.KindWarn, "notice")
			_, _ = m.Update(click(1, m.height-1))
			if standalone {
				_, _ = m.Update(key('y', tea.ModCtrl, ""))
				_, _ = m.Update(key('[', 0, "["))
			} else {
				prefixed(m, key('[', 0, "["))
			}
			if !m.noticeFocused {
				t.Fatal("did not focus")
			}
			_, _ = m.Update(key('z', 0, "z"))
			_, _ = m.Update(tea.KeyReleaseMsg{Code: 'z'})
			_, _ = m.Update(tea.PasteMsg{Content: "NEW-INPUT-LEAK"})
			_, _ = m.Update(releaseAt(1, m.height-1))
			got := capture()
			if strings.Contains(got, "NEW-INPUT-LEAK") || strings.Contains(got, "[122u") {
				t.Fatalf("new focused input leaked while old child mouse held: %q", got)
			}
			if !strings.Contains(got, "M") || !strings.Contains(got, "m") {
				t.Fatalf("old mouse gesture did not complete: %q", got)
			}
		})
	}
}
