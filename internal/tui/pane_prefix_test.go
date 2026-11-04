package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

func TestParseChord(t *testing.T) {
	for _, c := range []struct {
		in   string
		want chord
		ok   bool
	}{
		{"ctrl+q", chord{tea.ModCtrl, 'q'}, true},
		{" Ctrl+Q ", chord{tea.ModCtrl, 'q'}, true},
		{"shift+ctrl+a", chord{tea.ModCtrl | tea.ModShift, 'a'}, true},
		{"ctrl+^", chord{tea.ModCtrl, '^'}, true},
		{"alt++", chord{tea.ModAlt, '+'}, true},
		{"q", chord{}, false},
		{"shift+q", chord{}, false}, // it would take a character from typing
		{"", chord{}, false},
		{"ctrl+", chord{}, false},
		{"ctrl+ctrl+q", chord{}, false},
		{"cmd+q", chord{}, false},
		{"ctrl+space", chord{}, false},
	} {
		got, ok := parseChord(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseChord(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// R2's config fallback: an unparsable pane_prefix, or one equal to the
// palette key, takes the first of Ctrl+Q and Ctrl+^ that differs from the
// palette key; conflicts compare normalized chords, never strings.
func TestResolvePanePrefix(t *testing.T) {
	for _, c := range []struct{ palette, prefix, want, notice string }{
		{"", "", "^Q", ""},
		{"", "ctrl+b", "^B", ""},
		{"", "Ctrl+B", "^B", ""},
		{"", "q", "^Q", "is not a key chord"},
		{"", "ctrl+y", "^Q", "is the palette key"},
		{"ctrl+q", "", "^^", "is the palette key"},
		{"ctrl+q", "bogus", "^^", "is not a key chord"},
		{"CTRL+Q", "ctrl+q", "^^", "is the palette key"},
		{"ctrl+b", "", "^Q", ""},
	} {
		got, notice := resolvePanePrefix(&config.TUI{PaletteKey: c.palette, PanePrefix: c.prefix})
		if got.String() != c.want || c.notice == "" && notice != "" || !strings.Contains(notice, c.notice) {
			t.Errorf("palette %q prefix %q: %s %q; want %s %q", c.palette, c.prefix, got, notice, c.want, c.notice)
		}
		if c.notice != "" && !strings.Contains(notice, "using "+c.want) {
			t.Errorf("the notice names the fallback: %q", notice)
		}
	}
}

// The prefix matches across host representations: lock-state bits, the raw
// control byte (a legacy host, default or custom), a non-US layout's base
// key, and Kitty's Ctrl+Shift+6 for Ctrl+^.
func TestChordMatchesHostForms(t *testing.T) {
	q, _ := parseChord("ctrl+q")
	b, _ := parseChord("ctrl+b")
	caret, _ := parseChord("ctrl+^")
	for _, c := range []struct {
		chord chord
		key   tea.Key
		want  bool
	}{
		{q, tea.Key{Code: 'q', Mod: tea.ModCtrl}, true},
		{q, tea.Key{Code: 'Q', Mod: tea.ModCtrl | tea.ModCapsLock | tea.ModNumLock}, true},
		{q, tea.Key{Code: 0x11}, true},
		{q, tea.Key{Code: 'й', BaseCode: 'q', Mod: tea.ModCtrl}, true},
		{q, tea.Key{Code: 'q', Mod: tea.ModCtrl | tea.ModAlt}, false},
		{q, tea.Key{Code: 'q', Text: "q"}, false},
		{b, tea.Key{Code: 0x02}, true},
		{b, tea.Key{Code: 'b', Mod: tea.ModCtrl}, true},
		{caret, tea.Key{Code: '^', Mod: tea.ModCtrl}, true},
		{caret, tea.Key{Code: '6', ShiftedCode: '^', Mod: tea.ModCtrl | tea.ModShift}, true},
		{caret, tea.Key{Code: 0x1e}, true},
		{caret, tea.Key{Code: '6', Mod: tea.ModCtrl}, false},
	} {
		if got := c.chord.matches(c.key); got != c.want {
			t.Errorf("%s matches %+v = %v, want %v", c.chord, c.key, got, c.want)
		}
	}
}

// The prefix, its pane key, Esc and an unmapped key are consumed with their
// releases: a child using the Kitty protocol receives none of them, and
// modifier reports while armed neither disarm nor leak.
func TestPrefixNeverLeaksToAKittyChild(t *testing.T) {
	m := newTestModel(t, 60, 12)
	_, capture := mouseCapture(t, m, "\x1b[>3u")
	release := func(k tea.KeyPressMsg) { _, _ = m.Update(tea.KeyReleaseMsg(k)) }
	_, _ = m.Update(ctrlQ)
	release(ctrlQ)
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift, Mod: tea.ModShift})
	if m.prefix == nil {
		t.Fatal("a modifier report does not disarm the prefix")
	}
	_, _ = m.Update(key('o', 0, "o"))
	release(key('o', 0, "o"))
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	release(key(tea.KeyEscape, 0, ""))
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('k', 0, "k"))
	release(key('k', 0, "k"))
	if m.prefix == nil || !strings.Contains(ansi.Strip(m.View().Content), "no such pane key") {
		t.Fatal("an unmapped key keeps the menu open with a notice")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	release(key(tea.KeyEscape, 0, ""))
	_, _ = m.Update(key('x', 0, "x"))
	if got := capture(); got != "x" {
		t.Fatalf("child input %q, want only the ordinary key", got)
	}
}

// K4: a double prefix goes to the input surface captured at arming, with a
// paired release: a live child (Kitty or legacy, default or custom prefix by
// byte), a busy console; the palette editor ignores it; an exited pane
// refuses it; a surface gone between the taps consumes it.
func TestDoublePrefixGoesToTheCapturedSurface(t *testing.T) {
	t.Run("kitty child", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		_, capture := mouseCapture(t, m, "\x1b[>3u")
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(tea.KeyReleaseMsg(ctrlQ)) // the second tap's own release
		if m.prefix != nil {
			t.Fatal("a double prefix disarms")
		}
		if got, want := capture(), "\x1b[113;5u\x1b[113;5:3u"; got != want {
			t.Fatalf("child input %q, want %q", got, want)
		}
	})
	t.Run("custom prefix by byte, legacy child", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		m.cfg.PanePrefix = "ctrl+b"
		_, capture := mouseCapture(t, m, "")
		raw := tea.KeyPressMsg{Code: 0x02}
		_, _ = m.Update(raw)
		if m.prefix == nil {
			t.Fatal("the raw byte arms a custom Ctrl prefix")
		}
		_, _ = m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
		_, _ = m.Update(ctrlQ) // the default is no longer the prefix
		if got := capture(); got != "\x02\x11" {
			t.Fatalf("child input %q", got)
		}
	})
	t.Run("busy console", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		path := t.TempDir() + "/input"
		if started, err := m.console.Run(nil, "$ test", "python3", "-c", captureMouseScript, path); err != nil || !started {
			t.Fatalf("console start = %v, %v", started, err)
		}
		waitFor(t, func() bool { return strings.Contains(m.console.GridText(), "READY") })
		m.focusPalette = true
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(key('a', 0, "a"))
		if err := m.console.SendInput([]byte("\x00YOLOCTL_MOUSE_END\x00")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return !m.console.Busy() })
		if got := readFile(t, path); got != "\x11a" {
			t.Fatalf("console input %q", got)
		}
	})
	t.Run("palette editor", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		m.pal.Insert("abc")
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(ctrlQ)
		if line, _ := m.pal.Line(); line != "abc" || m.prefix != nil || m.status != "" {
			t.Fatalf("the editor ignores a double prefix: %q %q", line, m.status)
		}
	})
	t.Run("exited pane", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		addExitedTab(t, m, "done")
		m.focusPalette = false
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(ctrlQ)
		if shownNotice(m) != "nothing takes input here" || m.prefix == nil {
			t.Fatalf("notice %q", shownNotice(m))
		}
	})
	t.Run("vanished child", func(t *testing.T) {
		m := newTestModel(t, 80, 12)
		_, left := mouseCapture(t, m, "")
		right, _ := paneCapture(t, m, "", true)
		m.focusPalette = false
		_, _ = m.Update(ctrlQ)
		m.detachPane(m.tabs.Current(), right)
		_, _ = m.Update(ctrlQ)
		if m.prefix != nil || m.status != "" {
			t.Fatal("the second tap is consumed")
		}
		if got := left(); got != "" {
			t.Fatalf("the other pane received %q", got)
		}
	})
}

// MUST-3: a mouse press, a tab switch, host focus loss and a paste each
// disarm, and the event then takes its normal route; Esc cancels.
func TestPrefixDisarmEvents(t *testing.T) {
	m := newTestModel(t, 80, 14)
	tab := splitTab(t, m, paneSession(t, 12, 80, "sleep", "30"), paneSession(t, 12, 80, "sleep", "30"))
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	m.focusPalette = true
	zoomed := func() bool { _, _ = m.Update(key('z', 0, "z")); return tab.Layout.Zoomed() != nil }

	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if m.prefix != nil || !m.focusPalette || zoomed() {
		t.Fatal("Esc cancels, consumed: the palette stays open")
	}
	m.pal.Submit()

	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(tea.PasteMsg{Content: "box"})
	if line, _ := m.pal.Line(); m.prefix != nil || line != "box" {
		t.Fatalf("a paste disarms and reaches the palette: %q", line)
	}
	m.pal.Submit()

	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(tea.BlurMsg{})
	if m.prefix != nil {
		t.Fatal("host focus loss disarms")
	}
	_, _ = m.Update(tea.FocusMsg{})

	_, _ = m.Update(ctrlQ)
	palette := m.focusPalette
	_, _ = m.Update(click(m.width-1, m.height-1))
	_, _ = m.Update(releaseAt(m.width-1, m.height-1))
	if m.prefix != nil || m.focusPalette != palette {
		t.Fatal("a press outside the menu closes it and is consumed")
	}

	_, _ = m.Update(ctrlQ)
	m.tabs.Select(1)
	_, _ = m.Update(wakeMsg{})
	if m.prefix != nil {
		t.Fatal("a tab switch disarms")
	}
	m.tabs.Select(0)
	m.focusPalette = true
	if zoomed() {
		t.Fatal("no disarmed prefix acts later")
	}
}

// The resize keys move the focused pane's border, then for a second Ctrl
// and Alt arrows resize again without the prefix, also where Ctrl+← would
// switch tabs; repeats resize; any other key ends the window and takes its
// normal route; at expiry nothing is replayed.
func TestResizeRepeatWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	old := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = old })
	m := newTestModel(t, 100, 14)
	tab := splitTab(t, m, paneSession(t, 12, 100, "sleep", "30"), paneSession(t, 12, 100, "sleep", "30"))
	addExitedTab(t, m, "other")
	m.tabs.Select(0)
	right := tab.Layout.Panes()[1]
	x := right.Rect().X
	m.focusPalette = true
	prefixed(m, key(tea.KeyLeft, tea.ModCtrl, ""))
	if right.Rect().X != x-1 || !m.resizing() || strings.Contains(ansi.Strip(m.inputLine()), "resizing") {
		t.Fatalf("prefix Ctrl+← moves the border and opens the window: %+v", right.Rect())
	}
	m.focusPalette = true // where Ctrl+← would otherwise switch tabs
	_, _ = m.Update(key(tea.KeyLeft, tea.ModCtrl, ""))
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeftCtrl, Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl, IsRepeat: true})
	if right.Rect().X != x-3 || m.tabs.Active != 0 {
		t.Fatalf("Ctrl+← and its repeat resize inside the window: %+v, tab %d", right.Rect(), m.tabs.Active)
	}
	now = now.Add(900 * time.Millisecond)
	_, _ = m.Update(key(tea.KeyRight, tea.ModAlt, ""))
	if right.Rect().X != x+2 {
		t.Fatalf("Alt+→ moves five cells and renews the window: %+v", right.Rect())
	}
	now = now.Add(900 * time.Millisecond)
	m.focusPalette = true
	_, _ = m.Update(key('a', 0, "a"))
	if line, _ := m.pal.Line(); m.resizing() || line != "a" {
		t.Fatalf("another key ends the window and takes its route: %q", line)
	}
	_, _ = m.Update(key(tea.KeyLeft, tea.ModCtrl, ""))
	if m.tabs.Active != 1 || right.Rect().X != x+2 {
		t.Fatal("after the window Ctrl+← switches tabs again")
	}
	m.tabs.Select(0)
	m.pal.Submit()
	prefixed(m, key(tea.KeyRight, tea.ModCtrl, ""))
	now = now.Add(resizeWindow)
	_, _ = m.Update(prefixTickMsg{})
	m.focusPalette = true
	if m.resizing() || m.tabs.Active != 0 || strings.Contains(ansi.Strip(m.inputLine()), "resizing") {
		t.Fatal("the window expires with nothing replayed")
	}
	_, _ = m.Update(key(tea.KeyLeft, tea.ModCtrl, ""))
	if m.tabs.Active != 1 {
		t.Fatal("an expired window does not take Ctrl+←")
	}
}

// The key table is the one mapping: each row's key matches exactly one row,
// and the menu shows it: an action row maps to exactly one binding (R1);
// the arrows, resize keys, digits and ? are information or menu keys.
func TestPaneKeyTableIsTheMapping(t *testing.T) {
	m := newTestModel(t, 200, 60)
	_, _ = m.Update(ctrlQ)
	menu := ansi.Strip(m.View().Content)
	rows := map[string]int{}
	for _, r := range menuRows("^Q") {
		if r.bind >= 0 {
			rows[paneKeys[r.bind].text]++
		}
		if r.label != "" && !strings.Contains(menu, r.label) {
			t.Errorf("the menu lacks %q", r.label)
		}
	}
	for _, b := range paneKeys {
		switch info := b.text == "" || b.text == "0…9" || b.text == "?"; {
		case info && rows[b.text] != 0, !info && rows[b.text] != 1:
			t.Errorf("%s has %d action rows", b.key, rows[b.text])
		}
		k := tea.Key{Code: b.code, Mod: b.mod, Text: b.text}
		if b.text == "0…9" {
			k = tea.Key{Code: '7', Text: "7"}
		}
		n := 0
		for _, other := range paneKeys {
			if other.match(k) {
				n++
			}
		}
		if n != 1 || !b.match(k) {
			t.Errorf("%s matches %d rows", b.key, n)
		}
	}
	if !strings.Contains(menu, "Send ^Q") || !strings.Contains(menu, menuHint) {
		t.Fatalf("menu:\n%s", menu)
	}
	// R2's "Not mapped" keys; j and ! are P3's and in the table.
	for _, k := range []tea.Key{{Code: '&', Text: "&"}, {Code: ':', Text: ":"}, {Code: 'q', Text: "q"}, {Code: ' ', Text: " "}} {
		for _, b := range paneKeys {
			if b.match(k) {
				t.Errorf("%q is not a pane key but matches %s", k.Text, b.key)
			}
		}
	}
	for _, k := range []tea.Key{{Code: 'j', Text: "j"}, {Code: '1', Text: "!", Mod: tea.ModShift, ShiftedCode: '!'}} {
		if n := slices.IndexFunc(paneKeys, func(b paneBinding) bool { return b.match(k) }); n < 0 || paneKeys[n].key != k.Text {
			t.Errorf("%q is a P3 pane key", k.Text)
		}
	}
}

// o, { and } walk and reorder the panes; Alt+arrows move a border five
// cells; each returns focus to the pane.
func TestPrefixFocusNextSwapAndResize(t *testing.T) {
	m := newTestModel(t, 100, 26)
	s := func() *panes.Pane { return &panes.Pane{Session: paneSession(t, 24, 100, "sleep", "30")} }
	a, b, c := s(), s(), s()
	tab := tabs.New(a)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	_ = tab.Layout.Split(true, b)
	_ = tab.Layout.Split(false, c)
	m.layoutTab(tab)
	tab.Layout.SetFocus(a)
	m.focusPalette = true
	prefixed(m, key('o', 0, "o"))
	if tab.Focused() != b || m.focusPalette {
		t.Fatal("o focuses the next pane and returns to it")
	}
	rb, rc := b.Rect(), c.Rect()
	prefixed(m, key('}', tea.ModShift, "}"))
	if b.Rect() != rc || c.Rect() != rb || tab.Focused() != b || b.Session.LocalGeometry().Rows != rc.H {
		t.Fatal("} swaps with the next pane, focus moves with it, sessions resize")
	}
	prefixed(m, key('{', tea.ModShift, "{"))
	if b.Rect() != rb {
		t.Fatal("{ swaps back")
	}
	tab.Layout.SetFocus(a)
	w := a.Rect().W
	prefixed(m, key(tea.KeyRight, tea.ModAlt, ""))
	if a.Rect().W != w+5 || a.Session.LocalGeometry().Cols != w+5 {
		t.Fatalf("Alt+→ moves the border five cells: %d", a.Rect().W)
	}
}

// Tab keys after the prefix: n, p, a digit (0 = tab 10), a missing number
// refused, d detaching the whole tab (asking first for a running local
// command).
func TestPrefixTabKeys(t *testing.T) {
	m := newTestModel(t, 120, 14)
	for i := range 10 {
		addExitedTab(t, m, "t"+string(rune('a'+i)))
	}
	m.tabs.Select(0)
	m.focusPalette = false
	prefixed(m, key('n', 0, "n"))
	prefixed(m, key('n', 0, "n"))
	prefixed(m, key('p', 0, "p"))
	if m.tabs.Active != 1 {
		t.Fatalf("n n p: tab %d", m.tabs.Active)
	}
	prefixed(m, key('0', 0, "0"))
	if m.tabs.Active != 9 {
		t.Fatal("0 is tab 10")
	}
	prefixed(m, key('3', 0, "3"))
	if m.tabs.Active != 2 {
		t.Fatal("3 is tab 3")
	}
	m.tabs.CloseTab(m.tabs.Tabs[9])
	prefixed(m, key('0', 0, "0"))
	if m.tabs.Active != 2 || shownNotice(m) != "no tab 10" {
		t.Fatalf("a missing number is refused: %q", shownNotice(m))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, "")) // the refusal kept the menu open
	gone := m.tabs.Current()
	prefixed(m, key('d', 0, "d"))
	if m.tabs.Index(gone) >= 0 || m.confirm != nil {
		t.Fatal("d detaches a tab of exited panes at once")
	}
	running := tabs.New(&panes.Pane{Session: paneSession(t, 12, 120, "sleep", "30"), Command: "yoloctl box exec coder -- make"})
	m.tabs.Add(running)
	m.layoutTab(running)
	prefixed(m, key('d', 0, "d"))
	if m.confirm == nil || m.confirm.pane != nil || m.confirm.tab != running {
		t.Fatal("d on a running local command asks for the whole tab")
	}
}

// K2: prefix c opens the palette for a new tab: a live command opens a tab
// even with a split armed, which it does not use; the draft comes back after the
// submission and on Esc; an ordinary command opens no tab; a failed start
// keeps the draft; a busy console refuses c.
func TestPrefixNewTabEntry(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return "/bin/true", nil }
	m := newTestModel(t, 100, 14)
	m.tabs.Add(testTab(paneSession(t, 12, 100, "sleep", "30"), "yoloctl space view", false))
	m.layoutTab(m.tabs.Current())
	first := m.tabs.Current()
	m.focusPalette = true
	m.pal.Insert("box ls --dra")
	prefixed(m, key('|', tea.ModShift, "|"))
	if first.Layout.Empty() == nil || !m.focusPalette {
		t.Fatal("setup: an empty pane is open, with the palette for it")
	}
	m.focusPalette = true
	prefixed(m, key('c', 0, "c"))
	if line, _ := m.pal.Line(); line != "" || !m.focusPalette || !strings.Contains(ansi.Strip(m.inputLine()), "new tab: type a command") {
		t.Fatalf("c opens an empty line with its placeholder: %q", line)
	}
	for _, r := range "box attach api" {
		_, _ = m.Update(key(r, 0, string(r)))
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	// The empty pane was bypassed, not filled, and stays through the switch
	// to the new tab (K2: it stays untouched).
	if len(m.tabs.Tabs) != 2 || first.Layout.Empty() == nil || len(first.Layout.Panes()) != 2 || m.tabs.Current().Focused().Command != "yoloctl box attach api" || m.newTab != nil {
		t.Fatalf("the live command opened a new tab beside the empty pane: %d tabs", len(m.tabs.Tabs))
	}
	if line, _ := m.pal.Line(); line != "box ls --dra" {
		t.Fatalf("the draft is back after the submission: %q", line)
	}

	m.focusPalette = false
	prefixed(m, key('c', 0, "c"))
	_, _ = m.Update(key('x', 0, "x"))
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if line, _ := m.pal.Line(); line != "box ls --dra" || m.newTab != nil || m.focusPalette {
		t.Fatalf("Esc restores the draft and returns to the pane: %q", line)
	}

	executable = func() (string, error) { return "/bin/echo", nil }
	prefixed(m, key('c', 0, "c"))
	for _, r := range "space ls" {
		_, _ = m.Update(key(r, 0, string(r)))
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	waitFor(t, func() bool { return !m.console.Busy() })
	if len(m.tabs.Tabs) != 2 || !strings.Contains(m.console.GridText(), "space ls") {
		t.Fatal("an ordinary command runs in the overlay and opens no tab")
	}
	if line, _ := m.pal.Line(); line != "box ls --dra" {
		t.Fatalf("draft after an ordinary command: %q", line)
	}

	executable = func() (string, error) { return "", errors.New("gone") }
	prefixed(m, key('c', 0, "c"))
	for _, r := range "box attach api" {
		_, _ = m.Update(key(r, 0, string(r)))
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if line, _ := m.pal.Line(); len(m.tabs.Tabs) != 2 || !strings.Contains(m.status, "cannot locate") || line != "box ls --dra" || m.newTab != nil {
		t.Fatalf("a failed start keeps the draft: %q %q", line, m.status)
	}
	m.status = ""

	if started, err := m.console.Run(nil, "$ wait", "sh", "-c", "read answer"); err != nil || !started {
		t.Fatal("console start")
	}
	m.focusPalette = false
	prefixed(m, key('c', 0, "c"))
	if m.newTab != nil || shownNotice(m) != "a palette command is still running — wait for it" {
		t.Fatalf("a busy console refuses c: %q", shownNotice(m))
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, "")) // the refusal kept the menu open
	m.console.Kill()
}

// prefix c then a tab switch or the prefix again: an empty line takes the
// draft back; typed text stays and the draft goes to history.
func TestNewTabEntryEndsWithoutLosingText(t *testing.T) {
	m := newTestModel(t, 100, 14)
	addExitedTab(t, m, "a")
	addExitedTab(t, m, "b")
	m.focusPalette = true
	m.pal.Insert("draft")
	prefixed(m, key('c', 0, "c"))
	_, _ = m.Update(key(tea.KeyLeft, tea.ModCtrl, "")) // a tab switch in the palette
	if line, _ := m.pal.Line(); line != "draft" || m.newTab != nil {
		t.Fatalf("a tab switch ends c and restores the empty line's draft: %q", line)
	}
	prefixed(m, key('c', 0, "c"))
	_, _ = m.Update(key('y', 0, "y"))
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if line, _ := m.pal.Line(); line != "y" || m.newTab != nil {
		t.Fatalf("the prefix again ends c and keeps typed text: %q", line)
	}
	_, _ = m.Update(key(tea.KeyUp, 0, ""))
	if line, _ := m.pal.Line(); line != "draft" {
		t.Fatalf("the draft is in history: %q", line)
	}
}

// K3, the tab picker: arrows move, Enter switches, a digit switches at once,
// a missing number refuses and stays open, Esc closes keeping the draft and
// focus; the highlight follows its tab through closes and renumbering; a
// short terminal scrolls to every tab; paste is consumed, blur leaves it,
// a browser prompt cancels it, an outside click closes it and is consumed.
func TestTabPicker(t *testing.T) {
	m := newTestModel(t, 80, 10)
	for i := range 12 {
		addExitedTab(t, m, "tab"+string(rune('a'+i)))
	}
	m.tabs.Select(1)
	m.focusPalette = true
	m.pal.Insert("draft")
	prefixed(m, key('w', 0, "w"))
	if m.picker == nil || m.picker.highlight != m.tabs.Tabs[1] || m.hostCursor() != nil {
		t.Fatal("w opens the picker on the current tab, with no cursor")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "switch to a tab") || !strings.Contains(view, "❷ tabb · current") {
		t.Fatalf("picker view:\n%s", view)
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	if line, _ := m.pal.Line(); m.picker != nil || line != "draft" || !m.focusPalette || m.tabs.Active != 1 {
		t.Fatal("Esc closes, keeping the draft and focus")
	}

	m.focusPalette = false
	prefixed(m, key('w', 0, "w"))
	for range 11 {
		_, _ = m.Update(key(tea.KeyDown, 0, ""))
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "⓬ tabl") {
		t.Fatal("a short terminal scrolls to the last tab")
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.picker != nil || m.tabs.Active != 11 || m.focusPalette {
		t.Fatal("Enter switches to the highlighted tab and returns focus")
	}

	prefixed(m, key('w', 0, "w"))
	_, _ = m.Update(key(tea.KeyUp, 0, ""))
	h := m.picker.highlight
	m.tabs.CloseTab(m.tabs.Tabs[0]) // renumbers: h moves up a row
	_, _ = m.Update(wakeMsg{})
	if m.picker.highlight != h || m.picker.row != m.tabs.Index(h) {
		t.Fatal("the highlight stays on its tab through renumbering")
	}
	m.tabs.CloseTab(h)
	_, _ = m.Update(wakeMsg{})
	if m.picker.highlight == h || m.tabs.Index(m.picker.highlight) < 0 {
		t.Fatal("a closed highlight moves to the tab on its row")
	}
	_, _ = m.Update(tea.PasteMsg{Content: "x"})
	_, _ = m.Update(tea.BlurMsg{})
	_, _ = m.Update(key('9', 0, "9"))
	if m.picker != nil || m.tabs.Active != 8 {
		t.Fatal("paste is consumed, blur leaves it open, a digit switches at once")
	}
	_, _ = m.Update(tea.FocusMsg{})

	m.tabs.CloseTab(m.tabs.Tabs[9]) // nine tabs left: 0 names none
	prefixed(m, key('w', 0, "w"))
	_, _ = m.Update(key('0', 0, "0"))
	if m.picker == nil || !strings.Contains(ansi.Strip(m.View().Content), "no tab 10") || m.status != "" {
		t.Fatal("a missing number is refused in the picker's footer; it stays open")
	}
	_, _ = m.Update(key('k', 0, "k"))
	if m.picker == nil || !strings.Contains(overlayText(m), "no tab 10") || m.tabs.Active != 8 {
		t.Fatal("other keys are ignored; the refusal stays its time")
	}
	// An outside click on the strip closes it; the press and its release
	// are consumed and never reach a tab.
	strip := m.tabStripRow()
	x := tabClickX(m, 0)
	_, _ = m.Update(click(x, strip))
	_, _ = m.Update(releaseAt(x, strip))
	if m.picker != nil || m.tabs.Active != 8 || m.chrome != nil {
		t.Fatal("an outside click closes the picker and is consumed")
	}
	// A click on a row switches to its tab.
	prefixed(m, key('w', 0, "w"))
	want := m.picker.scroll // the first row shown
	o := m.activeOverlay()
	_, _ = m.Update(click(o.x+4, m.tabTop()+o.y+1))
	_, _ = m.Update(releaseAt(o.x+4, m.tabTop()+o.y+1))
	if m.picker != nil || m.tabs.Active != want || m.chrome != nil {
		t.Fatalf("a row click switches: active %d, want %d", m.tabs.Active, want)
	}
}

// A browser prompt arriving cancels the picker and closes the menu.
func TestBrowserPromptCancelsThePicker(t *testing.T) {
	m := newTestModel(t, 80, 12)
	m.cfg.Seams.Browser = "ask"
	s := paneSession(t, 10, 80, "sleep", "30")
	m.tabs.Add(testTab(s, "yoloctl box attach coder", false))
	m.focusPalette = false
	for _, open := range []tea.KeyPressMsg{key('w', 0, "w"), key('?', tea.ModShift, "?"), {}} {
		if open.Code != 0 {
			prefixed(m, open)
		} else {
			_, _ = m.Update(ctrlQ) // an armed prefix is disarmed too
		}
		done := make(chan struct{})
		_ = m.browserRequest(browserRequestMsg{surface: s, request: session.BrowserRequest{ID: "1", URL: "https://example.org"}, done: done})
		if m.picker != nil || m.prefix != nil || m.browserDialog == nil {
			t.Fatal("the browser prompt replaces the overlay and disarms")
		}
		close(done)
		m.browserDialog = nil
		delete(m.browserPrompts, s)
		m.focusPalette = false
	}
}

// The idle legend names the prefix; armed, the input line keeps its
// ordinary content and the menu names a configured prefix.
func TestPrefixLegend(t *testing.T) {
	m := newTestModel(t, 200, 40)
	m.cfg.PanePrefix = "ctrl+b"
	if !strings.Contains(ansi.Strip(m.inputLine()), "^B panes") {
		t.Fatal("the idle legend names the prefix")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if line := ansi.Strip(m.inputLine()); !strings.Contains(line, "^B panes") || strings.Contains(line, "split") {
		t.Fatalf("armed input line %q", line)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Send ^B") {
		t.Fatalf("the menu names the prefix:\n%s", view)
	}
}

// Standalone attachment keeps its own prefix: the pane prefix goes to the
// child as any other key.
func TestStandaloneIgnoresThePanePrefix(t *testing.T) {
	m := newTestModel(t, 40, 8)
	m.standalone = true
	m.focusPalette = false
	_, capture := mouseCapture(t, m, "")
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('z', 0, "z"))
	if m.prefix != nil {
		t.Fatal("standalone never arms")
	}
	if got := capture(); got != "\x11z" {
		t.Fatalf("child input %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// R1 (review r1): a key the prefix layers took stays taken through its
// repeats and its release, after the state that took it has ended and
// across a focus change; a resize repeat still resizes; a double prefix
// reaches the child as one pair; a key a child already owns keeps its
// release.
func TestConsumedKeysStayConsumedThroughRepeatAndRelease(t *testing.T) {
	repeat := func(k tea.KeyPressMsg) tea.KeyPressMsg { k.IsRepeat = true; return k }
	t.Run("held action", func(t *testing.T) {
		m := newTestModel(t, 80, 18)
		_, capture := mouseCapture(t, m, "\x1b[>11u")
		z := key('z', 0, "z")
		prefixed(m, z)
		_, _ = m.Update(repeat(z))
		_, _ = m.Update(tea.KeyReleaseMsg(z))
		if got := capture(); got != "" {
			t.Fatalf("a taken action leaked %q", got)
		}
	})
	t.Run("focus moves", func(t *testing.T) {
		m := newTestModel(t, 80, 12)
		_, left := mouseCapture(t, m, "\x1b[>11u")
		_, right := paneCapture(t, m, "\x1b[>11u", true)
		m.tabs.Current().Layout.SetFocus(m.tabs.Current().Layout.Panes()[0])
		m.focusPalette = false
		o := key('o', 0, "o")
		prefixed(m, o)
		_, _ = m.Update(repeat(o))
		_, _ = m.Update(tea.KeyReleaseMsg(o))
		_, _ = m.Update(key('x', 0, "x"))
		if l, r := left(), right(); l != "" || r != "\x1b[120u" {
			t.Fatalf("left %q right %q: o's repeat and release reach nobody", l, r)
		}
	})
	t.Run("resize repeat", func(t *testing.T) {
		now := time.Unix(1000, 0)
		old := clock
		clock = func() time.Time { return now }
		t.Cleanup(func() { clock = old })
		m := newTestModel(t, 100, 12)
		_, left := mouseCapture(t, m, "\x1b[>11u")
		right, _ := paneCapture(t, m, "", true)
		addExitedTab(t, m, "other")
		m.tabs.Select(0)
		m.focusPalette = false
		x := right.Rect().X
		ctrlLeft := key(tea.KeyLeft, tea.ModCtrl, "")
		prefixed(m, ctrlLeft)
		_, _ = m.Update(repeat(ctrlLeft))
		now = now.Add(2 * resizeWindow)
		_, _ = m.Update(repeat(ctrlLeft)) // the window is over: still held, still taken
		_, _ = m.Update(tea.KeyReleaseMsg(ctrlLeft))
		if right.Rect().X != x-2 || m.tabs.Active != 0 {
			t.Fatalf("the repeat resizes inside the window only: %+v tab %d", right.Rect(), m.tabs.Active)
		}
		m.tabs.Current().Layout.SetFocus(m.tabs.Current().Layout.Panes()[0])
		if got := left(); got != "" {
			t.Fatalf("the left child got %q", got)
		}
	})
	t.Run("double prefix", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		_, capture := mouseCapture(t, m, "\x1b[>3u")
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(repeat(ctrlQ))
		_, _ = m.Update(tea.KeyReleaseMsg(ctrlQ))
		if got, want := capture(), "\x1b[113;5u\x1b[113;5:3u"; got != want {
			t.Fatalf("child %q, want one pair %q", got, want)
		}
	})
	t.Run("child's own key", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		_, capture := mouseCapture(t, m, "\x1b[>3u")
		a := key('a', 0, "a")
		_, _ = m.Update(a) // the child owns a until its release
		_, _ = m.Update(ctrlQ)
		_, _ = m.Update(repeat(a)) // held through the prefix: not a pane key
		_, _ = m.Update(key(tea.KeyEscape, 0, ""))
		_, _ = m.Update(tea.KeyReleaseMsg(a))
		if got, want := capture(), "a\x1b[97;1:3u"; got != want {
			t.Fatalf("child %q, want its press and release %q", got, want)
		}
	})
	t.Run("picker Enter", func(t *testing.T) {
		m := newTestModel(t, 60, 12)
		addExitedTab(t, m, "first")
		_, capture := mouseCapture(t, m, "\x1b[>11u")
		m.tabs.Select(0)
		prefixed(m, key('w', 0, "w"))
		enter := key(tea.KeyEnter, 0, "")
		_, _ = m.Update(key(tea.KeyDown, 0, ""))
		_, _ = m.Update(enter)
		_, _ = m.Update(repeat(enter))
		_, _ = m.Update(tea.KeyReleaseMsg(enter))
		if m.tabs.Active != 1 {
			t.Fatal("Enter switched")
		}
		if got := capture(); got != "" {
			t.Fatalf("the picker's Enter leaked %q to the tab it opened", got)
		}
	})
}

// R2 (review r1): the picker scrolls and hit-tests by the popup's own
// layout: a wrapped footer, a host resize and a tiny unframed popup keep the
// highlight visible, and no footer, frame or blank cell selects a tab.
func TestOverlaysFollowTheRenderedLayout(t *testing.T) {
	tabsOf := func(t *testing.T, w, h int) *model {
		m := newTestModel(t, w, h)
		for i := range 12 {
			addExitedTab(t, m, fmt.Sprintf("TAB-%02d", i+1))
		}
		m.tabs.Select(0)
		return m
	}
	shown := overlayText
	// at maps a picker overlay cell to the screen.
	at := func(m *model, x, y int) (int, int) {
		o := m.activeOverlay()
		return o.x + x, m.tabTop() + o.y + y
	}
	m := tabsOf(t, 24, 12)
	prefixed(m, key('w', 0, "w"))
	for range 11 {
		_, _ = m.Update(key(tea.KeyDown, 0, ""))
	}
	if !strings.Contains(shown(m), "TAB-12") {
		t.Fatalf("the highlight stays visible beside a wrapped footer:\n%s", shown(m))
	}
	_, rows := overlayRows(m)
	for y, plain := range rows {
		if strings.Contains(plain, "TAB-") {
			continue
		}
		for _, x := range []int{0, 4, m.activeOverlay().w - 1} {
			sx, sy := at(m, x, y)
			_, _ = m.Update(click(sx, sy))
			_, _ = m.Update(releaseAt(sx, sy))
			if m.picker != nil || m.tabs.Active != 0 {
				t.Fatalf("a press at %d,%d (%q) closes without selecting: active %d", x, y, plain, m.tabs.Active)
			}
			prefixed(m, key('w', 0, "w"))
		}
	}
	// Each shown row selects exactly its tab.
	for y, row := range rows {
		if i := strings.Index(row, "TAB-"); i >= 0 {
			want := 0
			_, _ = fmt.Sscanf(row[i:], "TAB-%d", &want)
			m.tabs.Select(0)
			prefixed(m, key('w', 0, "w"))
			for range 11 {
				_, _ = m.Update(key(tea.KeyDown, 0, ""))
			}
			sx, sy := at(m, 4, y)
			_, _ = m.Update(click(sx, sy))
			_, _ = m.Update(releaseAt(sx, sy))
			if m.tabs.Active != want-1 {
				t.Fatalf("row %d shows TAB-%02d but selected %d", y, want, m.tabs.Active+1)
			}
		}
	}
	// A host resize to a narrower, shorter terminal keeps the highlight shown.
	wide := tabsOf(t, 100, 30)
	prefixed(wide, key('w', 0, "w"))
	for range 11 {
		_, _ = wide.Update(key(tea.KeyDown, 0, ""))
	}
	_, _ = wide.Update(tea.WindowSizeMsg{Width: 22, Height: 11})
	if !strings.Contains(shown(wide), "TAB-12") {
		t.Fatalf("after a host resize:\n%s", shown(wide))
	}
	// A tiny popup has no frame: its rows start at the top and span the
	// width; the footer below them never selects.
	tiny := tabsOf(t, 4, 40)
	prefixed(tiny, key('w', 0, "w"))
	inner, top, capacity := tiny.pickerLayout()
	if inner != 4 || top != 0 || capacity < 1 {
		t.Fatalf("unframed layout: inner %d top %d capacity %d", inner, top, capacity)
	}
	fx, fy := at(tiny, 3, capacity) // the first footer row
	_, _ = tiny.Update(click(fx, fy))
	if tiny.picker != nil || tiny.tabs.Active != 0 {
		t.Fatal("an unframed footer press closes without selecting")
	}
	prefixed(tiny, key('w', 0, "w"))
	_, _ = tiny.Update(key(tea.KeyDown, 0, ""))
	want := tiny.picker.scroll + 1
	rx, ry := at(tiny, 3, 1)
	_, _ = tiny.Update(click(rx, ry))
	if tiny.tabs.Active != want {
		t.Fatalf("an unframed row selects its tab: %d, want %d", tiny.tabs.Active, want)
	}
}

// R3 (review r1): a refusal shows in the open picker's footer; an unrelated
// status stays its own.
func TestPickerRefusalIsVisible(t *testing.T) {
	m := newTestModel(t, 80, 18)
	addExitedTab(t, m, "only")
	addExitedTab(t, m, "other") // w needs another tab
	m.tabs.Select(0)
	m.status = "an earlier notice"
	prefixed(m, key('w', 0, "w"))
	_, _ = m.Update(key('0', 0, "0"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "no tab 10") || m.picker == nil || m.status != "an earlier notice" {
		t.Fatalf("the refusal shows in the picker:\n%s", view)
	}
	narrow := newTestModel(t, 24, 12)
	for range 12 {
		addExitedTab(t, narrow, "t")
	}
	prefixed(narrow, key('w', 0, "w"))
	for range 11 {
		_, _ = narrow.Update(key(tea.KeyDown, 0, ""))
	}
	narrow.paneNotice("no tab 10") // a longer footer still keeps the row shown
	narrow.scrollPicker()
	if view := overlayText(narrow); !strings.Contains(view, "⓬") || !strings.Contains(view, "no tab") {
		t.Fatalf("notice and highlight both shown:\n%s", view)
	}
}

// P4: while prefix c waits, the empty input shows a dim placeholder right
// after the prompt, where the command goes; the first character replaces
// it and a short dim legend keeps Esc's meaning.
func TestNewTabPlaceholder(t *testing.T) {
	m := newTestModel(t, 120, 14)
	addExitedTab(t, m, "a")
	m.focusPalette = false
	prefixed(m, key('c', 0, "c"))
	raw := m.inputLine()
	plain := ansi.Strip(raw)
	if !strings.HasPrefix(plain, brandPrefix()+newTabPlaceholder) || !strings.Contains(raw, cDim+newTabPlaceholder+cReset) || strings.Contains(raw, cOrange+"new tab") {
		t.Fatalf("a dim placeholder fills the empty input: %q", raw)
	}
	_, _ = m.Update(key('b', 0, "b"))
	raw = m.inputLine()
	if plain = ansi.Strip(raw); strings.Contains(plain, newTabPlaceholder) || !strings.HasPrefix(plain, brandPrefix()+"b") || !strings.Contains(raw, cDim+"new tab · Esc cancels"+cReset) {
		t.Fatalf("typing replaces the placeholder: %q", plain)
	}
}
