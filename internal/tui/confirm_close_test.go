package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// Every live route is classified from its launch argv: an editor loses
// unsaved changes, a follow ends, a stdin command's input ends, and every
// other route (including one this table does not name) asks with an unknown
// remote outcome. Only space view and chat close at once among running
// panes; a holder-backed view is instant once established (below).
func TestCloseClassificationWalksEveryLiveRoute(t *testing.T) {
	s := paneSession(t, 5, 40, "sleep", "30")
	for _, c := range []struct{ line, want string }{
		{"chat", ""}, {"chat --space dev", ""},
		{"space view", ""}, {"sp view --space dev", ""},
		{"attach coder", unknownOutcome}, // not yet a holder-backed view
		{"box attach coder", unknownOutcome},
		{"box create coder --agent claude", unknownOutcome},
		{"box copy a b", unknownOutcome}, {"box start coder", unknownOutcome},
		{"box restart coder", unknownOutcome}, {"box recreate coder", unknownOutcome},
		{"box exec coder -- make test", unknownOutcome},
		{"space shell", unknownOutcome}, {"sp ssh", unknownOutcome},
		{"box edit coder", "unsaved changes are lost"}, {"space edit", "unsaved changes are lost"},
		{"account edit work", "unsaved changes are lost"}, {"agent edit claude", "unsaved changes are lost"},
		{"feature edit go", "unsaved changes are lost"}, {"capability edit net", "unsaved changes are lost"},
		{"box logs coder -f", "the follow ends"}, {"space logs --follow", "the follow ends"},
		{"box use coder --stdin", "the input ends"}, {"account secret set work TOKEN", "the input ends"},
	} {
		args := strings.Fields(c.line)
		if !opensLiveSurface(args) {
			t.Fatalf("%q is not a live route", c.line)
		}
		for _, cmd := range []string{c.line, "yoloctl " + c.line} {
			if got := closeEffect(&panes.Pane{Session: s, Command: cmd}); got != c.want {
				t.Errorf("%q: %q, want %q", cmd, got, c.want)
			}
		}
	}
	if got := closeEffect(&panes.Pane{Session: s, Command: "some future route"}); got != unknownOutcome {
		t.Fatalf("an unlisted live route asks by default: %q", got)
	}
	for _, p := range []*panes.Pane{{Session: s, Command: "box exec coder", Exited: true}, {Command: "box exec coder"}} {
		if closeEffect(p) != "" {
			t.Fatal("an exited pane and a pane without a session close at once")
		}
	}
}

// An established holder-backed view closes at once: its session keeps
// running under its holder.
func TestHolderBackedViewDetachesAtOnce(t *testing.T) {
	sock, _ := startManagedHolder(t)
	view := managedView(t, sock, 20, 80)
	waitFor(t, view.HolderView)
	m := newTestModel(t, 100, 26)
	m.tabs.Add(testTab(view, "box attach coder --space dev", false))
	m.layoutTab(m.tabs.Current())
	_, _ = m.Update(key('w', tea.ModCtrl, "")) // from the palette
	if m.confirm != nil || len(m.tabs.Tabs) != 0 {
		t.Fatal("a holder-backed view detaches without asking")
	}
}

func confirmModel(t *testing.T) (*model, *tabs.Tab, *panes.Pane, *panes.Pane) {
	t.Helper()
	m := newTestModel(t, 100, 26)
	tab := splitTab(t, m, paneSession(t, 20, 80, "sleep", "30"), paneSession(t, 20, 80, "sleep", "30"))
	ps := tab.Layout.Panes()
	ps[0].Command, ps[1].Command = "box exec coder -- make test", "box edit coder"
	m.focusPalette = true // Ctrl+W is the palette's
	return m, tab, ps[0], ps[1]
}

// Keys: Enter takes the highlighted button (Keep by default); ←/→/Tab
// move it; y closes; n and Esc keep; every other key, Ctrl+D and paste
// included, is consumed while it is open.
func TestConfirmPopupKeys(t *testing.T) {
	m, tab, a, b := confirmModel(t)
	tab.Layout.SetFocus(b)
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	if m.confirm == nil || m.confirm.pane != b || !m.focusPalette {
		t.Fatal("Ctrl+W on an editor asks")
	}
	_, cmd := m.Update(key('d', tea.ModCtrl, ""))
	_, _ = m.Update(tea.PasteMsg{Content: "x"})
	_, _ = m.Update(key('q', 0, "q"))
	if cmd != nil || m.confirm == nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("Ctrl+D, paste and other keys are consumed")
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.confirm != nil || len(tab.Layout.Panes()) != 2 || !m.focusPalette || tab.Focused() != b {
		t.Fatal("a bare Enter keeps, and focus is as it was")
	}
	for _, keep := range []tea.KeyPressMsg{key('n', 0, "n"), key(tea.KeyEscape, 0, "")} {
		_, _ = m.Update(key('w', tea.ModCtrl, ""))
		_, _ = m.Update(keep)
		if m.confirm != nil || len(tab.Layout.Panes()) != 2 {
			t.Fatal("n and Esc keep")
		}
	}
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	_, _ = m.Update(key(tea.KeyRight, 0, ""))
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.confirm != nil || len(tab.Layout.Panes()) != 1 || tab.Focused() != a {
		t.Fatal("→ then Enter closes")
	}
}

// One popup for a tab lists every running command; it is drawn in the pane
// overlay at the focused pane's top-left, without changing the palette
// rows or focus.
func TestConfirmPopupForATabAndItsPlace(t *testing.T) {
	m, tab, a, b := confirmModel(t)
	strip := m.tabStripRow()
	m.requestDetachTab(tab)
	raw, plain := overlayRows(m)
	nospace := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	body := overlayBody(m)
	if !strings.HasPrefix(plain[0], "╭─ Close tab? ─") ||
		!strings.Contains(body, nospace("box exec coder -- make test · its client ends; the remote side's outcome is unknown")) ||
		!strings.Contains(body, nospace("box edit coder · unsaved changes are lost")) ||
		!strings.Contains(plain[len(plain)-2], "[ Keep ]   [ Close ]") {
		t.Fatalf("popup:\n%s", strings.Join(plain, "\n"))
	}
	if !strings.Contains(raw[len(raw)-2], "\x1b[7m[ Keep ]\x1b[27m") {
		t.Fatalf("Keep is highlighted: %q", raw[len(raw)-2])
	}
	o, f := m.activeOverlay(), tab.Focused().Rect()
	view := strings.Split(ansi.Strip(m.View().Content), "\n")
	if o.x != f.X+1 || o.y != f.Y+1 || m.tabStripRow() != strip || !strings.Contains(view[m.tabTop()+o.y], "╭─ Close tab?") || !m.focusPalette {
		t.Fatalf("the popup sits in the focused pane; the palette rows stay:\n%s", strings.Join(view, "\n"))
	}
	_, _ = m.Update(key('y', 0, "y"))
	if len(m.tabs.Tabs) != 0 || m.confirm != nil || !m.focusPalette {
		t.Fatal("y closes the whole tab")
	}
	for _, p := range []*panes.Pane{a, b} {
		select {
		case <-p.Session.Done():
		default:
			t.Fatal("every listed client ended")
		}
	}
}

// A press on Close closes, on Keep or anywhere else keeps; the press is
// chrome-owned through its release.
func TestConfirmPopupMouse(t *testing.T) {
	m, tab, _, b := confirmModel(t)
	tab.Layout.SetFocus(b)
	at := func(button string) (int, int) { return overlayFind(t, m, button) }
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	x, y := at(confirmKeep)
	_, _ = m.Update(click(x, y))
	if m.confirm != nil || len(tab.Layout.Panes()) != 2 || m.mouseOwners[tea.MouseLeft].surface != nil {
		t.Fatal("Keep keeps and the press stays consumed")
	}
	_, _ = m.Update(releaseAt(x, y))
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	_, _ = m.Update(click(1, m.height-1))
	if m.confirm != nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("a press outside keeps")
	}
	_, _ = m.Update(releaseAt(1, m.height-1))
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	x, y = at(confirmClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if len(tab.Layout.Panes()) != 1 {
		t.Fatal("Close closes")
	}
}

// The popup binds to what it listed: a running command added to the tab
// refreshes the list instead of closing; a listed command exiting turns
// its line `exited` and keeps Keep highlighted; a tab switch or a browser
// prompt cancels it.
func TestConfirmPopupBindsToItsList(t *testing.T) {
	m, tab, a, _ := confirmModel(t)
	m.requestDetachTab(tab)
	if err := tab.Layout.Split(true, &panes.Pane{Session: paneSession(t, 10, 40, "sleep", "30"), Command: "space shell"}); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(key('y', 0, "y"))
	if len(m.tabs.Tabs) != 1 || m.confirm == nil || len(m.confirm.listed) != 3 {
		t.Fatal("an added running command refreshes the list and closes nothing")
	}
	m.confirm.close = true
	a.Exited = true
	_, _ = m.Update(wakeMsg{})
	if m.confirm == nil || m.confirm.close || !strings.Contains(overlayBody(m), "maketest·exited") {
		t.Fatalf("an exit keeps the popup open with Keep highlighted: %v %+v\n%s", m.confirm != nil, m.confirm, overlayText(m))
	}
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	m.requestDetachTab(tab)
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	if m.confirm != nil || len(m.tabs.Tabs) != 2 {
		t.Fatal("a tab switch cancels")
	}
	m.tabs.Select(0)
	m.requestDetachTab(tab)
	s := tab.Layout.Panes()[1].Session
	m.browserPrompts = map[*term.Session]browserPrompt{s: {surface: s, request: session.BrowserRequest{ID: "1", URL: "https://example.test"}, done: make(chan struct{})}}
	m.showBrowserDialog()
	if m.confirm != nil || m.browserDialog == nil || len(m.tabs.Tabs) != 2 {
		t.Fatal("a browser prompt cancels the popup (Keep) and shows instead")
	}
}

// The list is bounded by the rows the overlay has and scrolls by row with
// ↑/↓, so even one long wrapped entry's warning is reachable; Keep and
// Close stay visible.
func TestConfirmPopupListScrolls(t *testing.T) {
	m := newTestModel(t, 100, 26)
	s := paneSession(t, 5, 40, "sleep", "30")
	tab := testTab(s, "box exec coder -- one", false)
	m.tabs.Add(tab)
	var listed []*panes.Pane
	for i := range 30 {
		listed = append(listed, &panes.Pane{Session: s, Command: fmt.Sprintf("box exec coder -- job%02d", i)})
	}
	m.confirm = &closeConfirm{tab: tab, current: tab, listed: listed, exited: map[*panes.Pane]bool{}}
	view := overlayText
	text := view(m)
	if o := m.activeOverlay(); o.h > m.viewRows() || !strings.Contains(text, "[ Keep ]   [ Close ]") || !strings.Contains(text, "job00") || strings.Contains(text, "job29") {
		t.Fatalf("bounded, buttons visible:\n%s", text)
	}
	_, _ = m.Update(key(tea.KeyDown, 0, ""))
	_, _ = m.Update(key(tea.KeyDown, 0, ""))
	if text = view(m); strings.Contains(text, "job01") || !strings.Contains(text, "job02") || !strings.Contains(text, "[ Keep ]") {
		t.Fatalf("↓ scrolls the list by row:\n%s", text)
	}
	_, _ = m.Update(key(tea.KeyUp, 0, ""))
	if text = view(m); !strings.Contains(text, "job01") {
		t.Fatalf("↑ scrolls back:\n%s", text)
	}
	for range 100 {
		_, _ = m.Update(key(tea.KeyDown, 0, ""))
	}
	if text = view(m); !strings.Contains(text, "job29") || !strings.Contains(text, "[ Close ]") {
		t.Fatalf("the last entry is reachable:\n%s", text)
	}

	// One entry longer than the list area: its warning is reachable.
	long := &panes.Pane{Session: s, Command: "box edit coder " + strings.Repeat("--flag ", 300)}
	m.confirm = &closeConfirm{tab: tab, current: tab, listed: []*panes.Pane{long}, exited: map[*panes.Pane]bool{}}
	if strings.Contains(view(m), "unsaved changes are lost") {
		t.Fatal("precondition: the warning is below the visible rows")
	}
	for range 200 {
		_, _ = m.Update(key(tea.KeyDown, 0, ""))
	}
	if text = view(m); !strings.Contains(text, "unsaved changes are lost") || !strings.Contains(text, "[ Keep ]") {
		t.Fatalf("a long entry's warning is reachable by row:\n%s", text)
	}
}

// Other panes keep receiving and rendering output while the popup is open.
func TestConfirmPopupLeavesOtherPanesRunning(t *testing.T) {
	m := newTestModel(t, 100, 26)
	printer := paneSession(t, 20, 80, "sh", "-c", "sleep 0.3; echo LATE-OUTPUT; sleep 30")
	tab := splitTab(t, m, printer, paneSession(t, 20, 80, "sleep", "30"))
	editor := tab.Layout.Panes()[1]
	editor.Command = "box edit coder"
	m.requestDetachPane(tab, editor)
	if m.confirm == nil {
		t.Fatal("precondition: the popup is open")
	}
	waitFor(t, func() bool { return strings.Contains(ansi.Strip(m.View().Content), "LATE-OUTPUT") })
	if m.confirm == nil {
		t.Fatal("output does not close the popup")
	}
}

// A child gesture in flight when the popup opens finishes at its owner: the
// release reaches the child that had the press and nothing else.
func TestConfirmPopupLetsAnInFlightGestureFinish(t *testing.T) {
	m, _, right, _, rightInput := twoCapturePanes(t)
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(click(r.X+1, top+1))
	m.tabs.Current().Layout.SetFocus(right)
	m.focusPalette = true
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	if m.confirm == nil {
		t.Fatal("precondition: the popup is open")
	}
	_, _ = m.Update(releaseAt(r.X+1, top+1))
	if m.confirm == nil {
		t.Fatal("the in-flight release does not answer the popup")
	}
	if _, ok := m.mouseOwners[tea.MouseLeft]; ok {
		t.Fatal("the gesture ended at its owner")
	}
	m.cancelConfirm()
	// The release maps through the pane's current origin, which the popup
	// pushed down; it is the press's pair either way.
	if got := rightInput(); !strings.HasPrefix(got, "\x1b[<0;2;2M\x1b[<0;2;") || !strings.HasSuffix(got, "m") || strings.Count(got, "\x1b") != 2 {
		t.Fatalf("the child got %q, want its press and one release", got)
	}
}

// The list shows the immutable launch command, never a title the child
// sets, and only the buttons' row acts: "[ Close ]" in a command's text is
// not a button.
func TestConfirmPopupListTextNeverActs(t *testing.T) {
	m, tab, _, b := confirmModel(t)
	b.Command = "box exec coder -- echo '[ Close ]'"
	_, _ = b.Session.Write([]byte("\x1b]2;[ Close ] spoofed title\x07"))
	waitFor(t, func() bool { return strings.Contains(b.Title(), "spoofed") })
	tab.Layout.SetFocus(b)
	_, _ = m.Update(key('w', tea.ModCtrl, ""))
	text := overlayText(m)
	if strings.Contains(text, "spoofed") || !strings.Contains(text, "echo '[ Close ]'") {
		t.Fatalf("the list shows the launch command:\n%s", text)
	}
	x, y := overlayFind(t, m, "echo '[ Close ]'")
	x += ansi.StringWidth("echo '")
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if len(tab.Layout.Panes()) != 2 {
		t.Fatal("a click on list text keeps")
	}
}
