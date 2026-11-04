package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

var updateMenu = flag.Bool("update-menu", false, "rewrite the pane menu golden")

// shownNotice is the live notice of the open menu or the inert popup.
func shownNotice(m *model) string {
	if p := m.prefix; p != nil {
		return liveNotice(p.menu.notice, p.menu.noticeUntil)
	}
	if n := m.activeNotice(); n != nil {
		return n.text
	}
	return ""
}

// fakeClock pins the prefix clock; the returned func moves it on.
func fakeClock(t *testing.T) func(time.Duration) {
	now := time.Unix(1000, 0)
	old := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = old })
	return func(d time.Duration) { now = now.Add(d) }
}

// menuScreenRow is the screen row and column of a menu row's label.
func menuScreenRow(t *testing.T, m *model, label string) (int, int) {
	t.Helper()
	o := m.activeOverlay()
	if o == nil {
		t.Fatal("no menu")
	}
	_, top, _ := o.frame.layout(o.w, o.h)
	for i, l := range o.frame.Lines {
		if strings.HasPrefix(l, label) {
			return o.x + 2, m.tabTop() + o.y + top + i
		}
	}
	t.Fatalf("no row %q in %q", label, o.frame.Lines)
	return 0, 0
}

// The menu opens at the focused pane's top-left, one cell in, without
// changing palette focus or inserting palette rows; the host cursor hides.
func TestPaneMenuOpensAtThePaneTopLeft(t *testing.T) {
	m := newTestModel(t, 120, 40)
	tab := splitTab(t, m, paneSession(t, 38, 120, "sleep", "30"), paneSession(t, 38, 120, "sleep", "30"))
	right := tab.Layout.Panes()[1]
	tab.Layout.SetFocus(right)
	_, _ = m.Update(ctrlQ)
	o := m.activeOverlay()
	if r := right.Rect(); o == nil || o.x != r.X+1 || o.y != r.Y+1 {
		t.Fatalf("the menu sits one cell inside the focused pane: %+v vs %+v", o, r)
	}
	if m.focusPalette || m.hostCursor() != nil || m.tabTop() != m.chromeRows() {
		t.Fatal("opening the menu keeps focus and palette rows; the cursor hides")
	}
	view := strings.Split(ansi.Strip(m.View().Content), "\n")
	if row := view[m.tabTop()+o.y+1]; !strings.Contains(row, "Split right") || strings.Index(row, "Split right") < o.x {
		t.Fatalf("the first row is drawn in the pane: %q", row)
	}
	if !strings.Contains(strings.Join(view, "\n"), menuHint) {
		t.Fatal("the footer hint shows")
	}
}

// A pane that cannot hold 20×6, an undivided tab's small pane and no tab at
// all center the menu over the tab area; a tiny host and a host resize keep
// it inside the screen.
func TestPaneMenuCenteredFallbacks(t *testing.T) {
	m := newTestModel(t, 60, 30)
	_, _ = m.Update(ctrlQ) // no tab: the welcome surface
	aw, ah := m.tabArea()
	if o := m.activeOverlay(); o == nil || o.x != (aw-o.w)/2 || o.y != (ah-o.h)/2 || !strings.Contains(ansi.Strip(m.View().Content), "Split right") {
		t.Fatalf("no tab: centered %+v", o)
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	narrow := newTestModel(t, 44, 30)
	tab := splitTab(t, narrow, paneSession(t, 28, 44, "sleep", "30"), paneSession(t, 28, 44, "sleep", "30"))
	if r := tab.Focused().Rect(); r.W-2 >= overlayMinW {
		t.Fatalf("fixture: a narrow pane, %+v", r)
	}
	_, _ = narrow.Update(ctrlQ)
	aw, _ = narrow.tabArea()
	if o := narrow.activeOverlay(); o == nil || o.x != (aw-o.w)/2 {
		t.Fatalf("a narrow pane centers it: %+v", o)
	}
	for _, size := range [][2]int{{10, 4}, {30, 9}} {
		_, _ = narrow.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		o := narrow.activeOverlay()
		aw, ah := narrow.tabArea()
		if o == nil || o.x < 0 || o.y < 0 || o.x+o.w > aw || o.y+o.h > ah {
			t.Fatalf("%v: the menu stays in the tab area: %+v in %dx%d", size, o, aw, ah)
		}
		for i, row := range strings.Split(narrow.View().Content, "\n")[narrow.tabTop():] {
			if w := ansi.StringWidth(row); w > size[0] { // the tab-area rows the menu draws on
				t.Fatalf("%v: row %d is %d wide", size, i, w)
			}
		}
	}
}

// The undivided tab path draws the menu too, in its single pane.
func TestPaneMenuOverTheUndividedTab(t *testing.T) {
	m := newTestModel(t, 100, 30)
	addExitedTab(t, m, "only")
	m.focusPalette = false
	_, _ = m.Update(ctrlQ)
	p := m.tabs.Focused()
	if o := m.activeOverlay(); o == nil || o.x != p.Rect().X+1 || o.y != p.Rect().Y+1 {
		t.Fatalf("the single pane's top-left: %+v", o)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Split right") {
		t.Fatal("the menu is drawn over the undivided tab")
	}
}

// A bound key acts exactly as before and closes the menu; Esc closes it;
// Enter does nothing; the prefix again forwards to the captured surface.
func TestPaneMenuKeysActAndClose(t *testing.T) {
	m := newTestModel(t, 120, 40)
	tab := splitTab(t, m, paneSession(t, 38, 120, "sleep", "30"), paneSession(t, 38, 120, "sleep", "30"))
	prefixed(m, key('z', 0, "z"))
	if tab.Layout.Zoomed() == nil || m.prefix != nil {
		t.Fatal("z zooms and closes the menu")
	}
	prefixed(m, key('z', 0, "z"))
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.prefix == nil || shownNotice(m) != "" {
		t.Fatal("Enter does nothing")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if m.prefix != nil {
		t.Fatal("Esc closes")
	}
	tab.Layout.SetFocus(tab.Layout.Panes()[0])
	before := tab.Focused()
	prefixed(m, key(tea.KeyRight, 0, ""))
	if tab.Focused() == before || m.prefix != nil {
		t.Fatal("a plain arrow still focuses the pane that way")
	}
	prefixed(m, key('?', tea.ModShift, "?"))
	if m.prefix == nil {
		t.Fatal("? keeps the menu open")
	}
}

// A press on an action row runs it and closes the menu; an information row
// or a separator does nothing; a press outside closes it; every press is
// consumed through its release; the wheel scrolls a clipped menu.
func TestPaneMenuMouse(t *testing.T) {
	m := newTestModel(t, 120, 40)
	tab := splitTab(t, m, paneSession(t, 38, 120, "sleep", "30"), paneSession(t, 38, 120, "sleep", "30"))
	_, _ = m.Update(ctrlQ)
	x, y := menuScreenRow(t, m, "Focus  ")
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if m.prefix == nil {
		t.Fatal("an information row is not selectable")
	}
	x, y = menuScreenRow(t, m, "Zoom")
	_, _ = m.Update(click(x, y))
	if tab.Layout.Zoomed() == nil || m.prefix != nil {
		t.Fatal("a click on Zoom zooms and closes")
	}
	if owner, ok := m.mouseOwners[tea.MouseLeft]; !ok || owner != (mouseOwner{}) {
		t.Fatal("the press stays consumed through its release")
	}
	_, _ = m.Update(releaseAt(x, y))
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(click(m.width-1, m.height-1))
	_, _ = m.Update(releaseAt(m.width-1, m.height-1))
	if m.prefix != nil || tab.Layout.Zoomed() == nil {
		t.Fatal("a press outside closes it and acts on nothing")
	}

	short := newTestModel(t, 120, 16)
	splitTab(t, short, paneSession(t, 14, 120, "sleep", "30"), paneSession(t, 14, 120, "sleep", "30"))
	_, _ = short.Update(ctrlQ)
	o := short.activeOverlay()
	if o.h > short.viewRows() || len(o.frame.Lines) <= 1 {
		t.Fatalf("fixture: a clipped menu %+v", o)
	}
	_, _ = short.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: o.x + 1, Y: short.tabTop() + o.y + 2})
	if short.prefix.menu.scroll != 1 || short.activeOverlay().frame.Lines[0] == o.frame.Lines[0] {
		t.Fatal("the wheel scrolls the body")
	}
	// Hit tests use the scrolled rows.
	x, y = menuScreenRow(t, short, "Zoom")
	_, _ = short.Update(click(x, y))
	if short.tabs.Current().Layout.Zoomed() == nil {
		t.Fatal("a scrolled row still maps to its action")
	}
}

// Unavailable rows are dimmed; pressing their key refuses in the footer for
// at least three seconds and the menu stays; a refused key replaces the
// notice; the hint returns after it expires.
func TestPaneMenuRefusalsStayInTheFooter(t *testing.T) {
	advance := fakeClock(t)
	m := newTestModel(t, 100, 40)
	addExitedTab(t, m, "only")
	m.focusPalette = false
	_, _ = m.Update(ctrlQ)
	o := m.activeOverlay()
	for i, r := range o.rows {
		dim := r.bind < 0 || m.bindingWhy(paneKeys[r.bind]) != ""
		if o.frame.Dimmed[i] != dim {
			t.Fatalf("row %q dimmed %v", r.label, o.frame.Dimmed[i])
		}
		switch r.label {
		case "Zoom", "Spread evenly", "Swap with next", "Pick a tab", "Bring a tab in":
			if !dim {
				t.Errorf("%s is unavailable on an undivided single tab", r.label)
			}
		case "Split right", "Detach pane", "New tab":
			if dim {
				t.Errorf("%s is available", r.label)
			}
		}
	}
	_, _ = m.Update(key('z', 0, "z"))
	if m.prefix == nil || shownNotice(m) != "one pane: nothing to zoom" || !strings.Contains(ansi.Strip(m.View().Content), "one pane: nothing to zoom") {
		t.Fatal("z refuses in the footer, the menu open")
	}
	advance(2900 * time.Millisecond)
	if shownNotice(m) == "" {
		t.Fatal("the notice shows at least 3 s")
	}
	_, _ = m.Update(key('k', 0, "k"))
	advance(2900 * time.Millisecond)
	if shownNotice(m) != "no such pane key" || m.prefix == nil {
		t.Fatal("a refused key replaces the notice and renews it")
	}
	advance(200 * time.Millisecond)
	if shownNotice(m) != "" || !strings.Contains(ansi.Strip(m.View().Content), menuHint) {
		t.Fatal("the hint returns once it expires")
	}
	_, _ = m.Update(key('k', 0, "k"))
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if m.prefix != nil || m.activeNotice() != nil {
		t.Fatal("Esc dismisses the menu and its notice together")
	}
}

// Host blur, a tab switch and a paste close the menu; a live notice is never
// erased by them: it moves to the inert notice popup, which acts on nothing
// and goes on any key (consumed) or press.
func TestPaneMenuNoticeOutlivesItsMenu(t *testing.T) {
	advance := fakeClock(t)
	m := newTestModel(t, 100, 40)
	_, capture := mouseCapture(t, m, "")
	addExitedTab(t, m, "second")
	m.tabs.Select(0)
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('z', 0, "z"))
	_, _ = m.Update(tea.BlurMsg{})
	if m.prefix != nil || m.activeNotice() == nil || !strings.Contains(ansi.Strip(m.View().Content), "one pane: nothing to zoom") {
		t.Fatal("blur closes the menu; its notice stays, inert")
	}
	_, _ = m.Update(tea.FocusMsg{})
	_, _ = m.Update(key('a', 0, "a"))
	if m.activeNotice() != nil || m.prefix != nil {
		t.Fatal("any key dismisses the inert notice")
	}
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('z', 0, "z"))
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	if m.prefix != nil || m.activeNotice() == nil {
		t.Fatal("a tab switch closes the menu; the notice stays")
	}
	_, _ = m.Update(click(1, m.tabTop()+1))
	_, _ = m.Update(releaseAt(1, m.tabTop()+1))
	if m.activeNotice() != nil {
		t.Fatal("a press dismisses it")
	}
	m.tabs.Select(0)
	_, _ = m.Update(wakeMsg{})
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('z', 0, "z"))
	_, _ = m.Update(tea.PasteMsg{Content: "p"})
	advance(noticeTime)
	if m.prefix != nil || m.activeNotice() != nil {
		t.Fatal("a paste closes the menu; the notice ends with its time")
	}
	waitFor(t, func() bool { return strings.Contains(capture(), "p") })
	if got := capture(); strings.ContainsAny(got, "az") {
		t.Fatalf("no menu or notice key reached the child: %q", got)
	}
}

// A refusal while an action runs (its key looked available) reopens the
// menu with the notice; with no menu open, a refusal opens the inert popup.
func TestPaneMenuRefusalDuringAnAction(t *testing.T) {
	fakeClock(t)
	m := newTestModel(t, 100, 40)
	addExitedTab(t, m, "only")
	addExitedTab(t, m, "two")
	m.tabs.Select(0)
	m.focusPalette = false
	prefixed(m, key('1', tea.ModShift, "!"))
	if m.prefix == nil || shownNotice(m) != "already its own tab" {
		t.Fatalf("! on an undivided tab refuses in the menu: %q", shownNotice(m))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	m.paneNotice("no tab 7")
	if m.notice == nil || shownNotice(m) != "no tab 7" || !strings.Contains(ansi.Strip(m.View().Content), "no tab 7") || m.status != "" {
		t.Fatal("no popup open: the inert notice popup, never the palette line")
	}
}

// A golden of the menu over two panes, as plain text.
func TestPaneMenuGolden(t *testing.T) {
	m := newTestModel(t, 80, 30)
	splitTab(t, m, paneSession(t, 28, 80, "sleep", "30"), paneSession(t, 28, 80, "sleep", "30"))
	_, _ = m.Update(ctrlQ)
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	got := strings.Join(rows[m.tabTop():], "\n") + "\n"
	path := filepath.Join("testdata", "pane-menu.golden")
	if *updateMenu {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the menu differs from its golden:\n%s", got)
	}
}

// assertMenuOnScreen checks that the open menu, its footer included, lies in
// the visible tab area and that every row a press can hit is drawn there.
func assertMenuOnScreen(t *testing.T, m *model, what string) {
	t.Helper()
	o := m.activeOverlay()
	aw, ah := m.tabArea()
	if o == nil || o.x < 0 || o.y < 0 || o.x+o.w > aw || o.y+o.h > ah {
		t.Fatalf("%s: the menu %+v leaves the visible %dx%d tab area", what, o, aw, ah)
	}
	view := strings.Split(ansi.Strip(m.View().Content), "\n")
	if footer := m.tabTop() + o.y + o.h - 2; footer >= len(view) || !strings.Contains(view[footer], menuHint) {
		t.Fatalf("%s: the footer is on screen:\n%s", what, strings.Join(view, "\n"))
	}
	_, top, capacity := o.frame.layout(o.w, o.h)
	for i := range min(capacity, len(o.frame.Lines)) {
		if y := m.tabTop() + o.y + top + i; y >= len(view) || !strings.Contains(view[y], strings.Fields(o.frame.Lines[i])[0]) {
			t.Fatalf("%s: actionable row %d is not drawn", what, i)
		}
	}
}

// Review r1 R1: with the palette open (and growing while the menu is open)
// the menu fits the visible part of the pane, or centers in the visible tab
// area when that part is too small or off screen; the panes keep their size.
func TestPaneMenuFitsAnOpenPalette(t *testing.T) {
	m := newTestModel(t, 100, 24)
	m.cfg.PaletteMinHeight = "12"
	tab := splitTab(t, m, paneSession(t, 22, 100, "sleep", "30"), paneSession(t, 22, 100, "sleep", "30"))
	m.focusPalette = true
	m.resizeSurfaces()
	rows := tab.Focused().Rect().H
	_, _ = m.Update(ctrlQ)
	if m.prefix == nil || !m.focusPalette || tab.Focused().Rect().H != rows {
		t.Fatal("the menu opens with the palette focused and the panes unchanged")
	}
	assertMenuOnScreen(t, m, "open palette")
	// The palette grows while the menu is open: it still fits.
	m.cfg.PaletteMinHeight = "16"
	assertMenuOnScreen(t, m, "grown palette")
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))

	// A focused lower pane wholly below the visible rows: centered.
	low := newTestModel(t, 100, 30)
	low.cfg.PaletteMinHeight = "14"
	lt := splitTab(t, low, paneSession(t, 28, 100, "sleep", "30"))
	if err := lt.Layout.Split(false, &panes.Pane{Session: paneSession(t, 13, 100, "sleep", "30"), Command: "lower"}); err != nil {
		t.Fatal(err)
	}
	low.layoutTab(lt)
	lt.Layout.SetFocus(lt.Layout.Panes()[1])
	low.focusPalette = true
	_, _ = low.Update(ctrlQ)
	_, ah := low.tabArea()
	if r := lt.Focused().Rect(); r.Y+3 < ah {
		t.Fatalf("fixture: the lower pane is mostly hidden (%+v, %d visible rows)", r, ah)
	}
	assertMenuOnScreen(t, low, "lower pane")
}

// Review r1 R2: a notice handed to the inert popup keeps the menu's origin
// across a tab switch to an unequal layout, and shows all of its wrapped
// text; its deadline is not renewed.
func TestInertNoticeKeepsOriginAndText(t *testing.T) {
	advance := fakeClock(t)
	m := newTestModel(t, 120, 40)
	tab, _, right := twoPanes(t, m)
	addExitedTab(t, m, "undivided")
	m.tabs.Select(0)
	tab.Layout.SetFocus(right)
	_, _ = m.Update(ctrlQ)
	m.paneNotice("fill or close the empty pane first, then split this pane again")
	o := m.activeOverlay()
	advance(time.Second)
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	n := m.activeOverlay()
	if m.prefix != nil || m.notice == nil || n.x != o.x || n.y != o.y {
		t.Fatalf("the inert notice keeps the menu's origin: %+v vs %+v", n, o)
	}
	if body := overlayBody(m); !strings.Contains(body, "fillorclosetheemptypanefirst,thensplitthispaneagain") {
		t.Fatalf("all of its text shows:\n%s", overlayText(m))
	}
	advance(noticeTime - time.Second - time.Millisecond)
	if m.activeNotice() == nil {
		t.Fatal("it keeps its remaining time")
	}
	advance(time.Millisecond)
	if m.activeNotice() != nil {
		t.Fatal("not renewed")
	}

	// A narrow pane: the wrapped notice is fully readable after blur.
	narrow := newTestModel(t, 60, 30)
	ntab, _, nright := twoPanes(t, narrow)
	ntab.Layout.SetFocus(nright)
	_, _ = narrow.Update(ctrlQ)
	narrow.paneNotice("fill or close the empty pane first")
	_, _ = narrow.Update(tea.BlurMsg{})
	if body := overlayBody(narrow); narrow.notice == nil || !strings.Contains(body, "fillorclosetheemptypanefirst") {
		t.Fatalf("wrapped text survives the hand-off:\n%s", overlayText(narrow))
	}
}

// overlayText is the active overlay's plain text.
func overlayText(m *model) string {
	o := m.activeOverlay()
	if o == nil {
		return ""
	}
	return ansi.Strip(strings.Join(o.frame.render(o.w, o.h, ""), "\n"))
}

// overlayBody is the overlay's text without frame or spaces, so wrapped
// rows read as one.
func overlayBody(m *model) string {
	return strings.Join(strings.FieldsFunc(overlayText(m), func(r rune) bool { return r == ' ' || r == '│' || r == '\n' }), "")
}

// overlayRows is the active pane overlay as rendered, raw and plain.
func overlayRows(m *model) (raw, plain []string) {
	o := m.activeOverlay()
	if o == nil {
		return nil, nil
	}
	raw = o.frame.render(o.w, o.h, "")
	for _, r := range raw {
		plain = append(plain, ansi.Strip(r))
	}
	return raw, plain
}

// overlayFind is the screen cell two columns into the first overlay row
// containing text.
func overlayFind(t *testing.T, m *model, text string) (int, int) {
	t.Helper()
	o := m.activeOverlay()
	_, plain := overlayRows(m)
	for y, line := range plain {
		if i := strings.Index(line, text); i >= 0 {
			return o.x + ansi.StringWidth(line[:i]) + 2, m.tabTop() + o.y + y
		}
	}
	t.Fatalf("no %q in the overlay:\n%s", text, strings.Join(plain, "\n"))
	return 0, 0
}

// P3: the pickers and the close confirmation draw in the pane overlay: the
// picker at the focused pane, the confirmation at the pane being closed,
// also when the bar's ✕ of an unfocused pane asks; the palette rows and
// focus stay as they were and the input line keeps its legend.
func TestPopupsAnchorAtTheirPane(t *testing.T) {
	m := newTestModel(t, 120, 40)
	tab, left, right := twoPanes(t, m)
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	tab.Layout.SetFocus(right)
	strip := m.tabStripRow()
	prefixed(m, key('w', 0, "w"))
	o, r := m.activeOverlay(), right.Rect()
	if m.picker == nil || o.x != r.X+1 || o.y != r.Y+1 || m.focusPalette || m.tabStripRow() != strip ||
		strings.Contains(ansi.Strip(m.inputLine()), "switch to a tab") || m.hostCursor() != nil {
		t.Fatalf("the picker sits in the focused pane: %+v vs %+v", o, r)
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	x, y := barCell(t, m, tab, left, panes.BarClose)
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	o, l := m.activeOverlay(), left.Rect()
	if m.confirm == nil || m.confirm.pane != left || o.x != l.X+1 || o.y != l.Y+1 || m.focusPalette ||
		!strings.Contains(ansi.Strip(m.View().Content), "Close pane?") {
		t.Fatalf("the bar's ✕ asks at its own pane: %+v vs %+v", o, l)
	}
	bx, by := overlayFind(t, m, confirmKeep)
	_, _ = m.Update(click(bx, by))
	_, _ = m.Update(releaseAt(bx, by))
	if m.confirm != nil || len(tab.Layout.Panes()) != 2 {
		t.Fatal("the anchored Keep keeps")
	}
}

// D5/R3 for the popups: the confirmation's refusal shows above its buttons
// for at least 3 s and it stays open; when it then closes because its tab
// switched, the live notice moves to the inert popup.
func TestPopupNoticesOutliveTheirOwner(t *testing.T) {
	advance := fakeClock(t)
	m := newTestModel(t, 120, 40)
	tab, _, _ := twoPanes(t, m)
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	m.requestDetachTab(tab)
	if err := tab.Layout.Split(true, &panes.Pane{Session: paneSession(t, 10, 40, "sleep", "30"), Command: "space shell"}); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(key('y', 0, "y"))
	advance(2900 * time.Millisecond)
	if m.confirm == nil || !strings.Contains(overlayBody(m), "therunningcommandschanged") || !strings.Contains(overlayText(m), confirmClose) {
		t.Fatalf("the refusal shows above the buttons:\n%s", overlayText(m))
	}
	o := m.activeOverlay()
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	n := m.activeOverlay()
	if m.confirm != nil || m.activeNotice() == nil || !strings.Contains(overlayBody(m), "therunningcommandschanged") || n.x != o.x || n.y != o.y {
		t.Fatalf("its owner closed: the notice shows inert at its origin for its remaining time: %+v vs %+v", n, o)
	}
	advance(200 * time.Millisecond)
	if m.activeNotice() != nil {
		t.Fatal("then it goes")
	}
}

// P3 review r1: a popup's height follows its footer wrapped at the width it
// is granted, so a narrow, tall pane keeps the picker's tabs drawn and
// selectable beside a long refusal, also after a width change; the
// confirmation keeps its list, its whole notice and its buttons.
func TestNarrowPopupsKeepTheirBody(t *testing.T) {
	m := newTestModel(t, 49, 40)
	tab, left, _ := twoPanes(t, m)
	addExitedTab(t, m, "second")
	m.tabs.Select(0)
	tab.Layout.SetFocus(left)
	if w := left.Rect().W; w != 24 {
		t.Fatalf("fixture: a 24-column pane, got %d", w)
	}
	open := func() {
		prefixed(m, key('w', 0, "w"))
		_, _ = m.Update(key('0', 0, "0"))
	}
	drawn := func(what string) {
		t.Helper()
		body := overlayBody(m)
		if !strings.Contains(body, "❶") || !strings.Contains(body, "❷") || !strings.Contains(body, "notab10") {
			t.Fatalf("%s: both tabs and the whole refusal are drawn:\n%s", what, overlayText(m))
		}
	}
	open()
	drawn("24 columns")
	_, _ = m.Update(key(tea.KeyDown, 0, ""))
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.picker != nil || m.tabs.Active != 1 {
		t.Fatalf("↓ Enter switches to the drawn second row: active %d", m.tabs.Active)
	}
	m.tabs.Select(0)
	open()
	x, y := overlayFind(t, m, "❷")
	_, _ = m.Update(click(x, y))
	_, _ = m.Update(releaseAt(x, y))
	if m.tabs.Active != 1 {
		t.Fatal("a press on the drawn row hits that tab")
	}
	m.tabs.Select(0)
	open()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 45, Height: 40})
	drawn("after a width change")

	// The confirmation: a 26-column pane, a long listed command and the
	// changed-commands notice.
	c := newTestModel(t, 53, 40)
	ctab, cleft, _ := twoPanes(t, c)
	ctab.Layout.SetFocus(cleft)
	if w := cleft.Rect().W; w != 26 {
		t.Fatalf("fixture: a 26-column pane, got %d", w)
	}
	cleft.Command = "box exec coder -- make test-with-a-long-target-name"
	c.requestDetachPane(ctab, cleft)
	c.paneNotice("the running commands changed: review the list again")
	body := overlayBody(c)
	if !strings.Contains(body, "boxexeccoder") || !strings.Contains(body, "therunningcommandschanged:reviewthelistagain") ||
		!strings.Contains(overlayText(c), "[ Keep ]   [ Close ]") {
		t.Fatalf("the list, the whole notice and the buttons show:\n%s", overlayText(c))
	}
}
