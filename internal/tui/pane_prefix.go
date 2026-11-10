package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/palette"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// The pane prefix (tui.yaml pane_prefix, default Ctrl+Q) reaches pane and
// tab actions from a live child, a busy console, the palette, or an exited
// pane alike. Key precedence, first match wins: a browser prompt, the close
// popup, an open tab picker, the inert notice popup, an armed prefix (its
// menu is open; the next key is a pane key), a resize repeat window, the
// prefix key itself, then the routes of handleKey. There is no timeout; a
// tab switch, host focus loss or a paste closes the menu and is then handled
// normally; a press goes to the menu first (pane_menu.go).

// prefixSurface is what a double prefix goes to: the input surface that was
// focused when the prefix armed.
type prefixSurface int

const (
	surfaceNone    prefixSurface = iota // an exited pane: nothing takes input
	surfaceChild                        // the focused pane's live child
	surfaceConsole                      // a busy palette command
	surfaceEditor                       // the palette editor: the prefix is not text
)

// prefixState is an armed prefix.
type prefixState struct {
	tab     *tabs.Tab // the current tab when it armed; a switch disarms
	surface prefixSurface
	session *term.Session // the child, for surfaceChild
	menu    menuState     // the menu the armed prefix shows
}

// resizeWindow is the repeat window after a resize key: until its deadline,
// Ctrl and Alt arrows move borders again without the prefix.
const resizeWindow = time.Second

// clock is the time source of the prefix's windows; a seam for tests.
var clock = time.Now

type prefixTickMsg struct{}

// prefixTick repaints when a window or notice may have expired.
func prefixTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return prefixTickMsg{} })
}

// newTabIntent is prefix c: the next submitted live command opens a new tab,
// bypassing an empty pane, which stays. The palette draft it set aside comes back when
// the intent ends.
type newTabIntent struct {
	draft         palette.Draft
	tab           *tabs.Tab
	returnPalette bool
}

// panePrefix is the resolved prefix chord; Run reports a fallback once.
func (m *model) panePrefix() chord {
	if !m.prefixResolved {
		m.prefixChord, _ = resolvePanePrefix(m.cfg)
		m.prefixResolved = true
	}
	return m.prefixChord
}

// prefixLayer routes a key press through the prefix layers above handleKey.
// It reports whether the key was taken. A taken physical key stays taken
// through its repeats and its release, after the state that took it has
// ended; a key a child already owns keeps its owner.
func (m *model) prefixLayer(k tea.Key) (tea.Cmd, bool) {
	id := keyIdentity(k)
	if !k.IsRepeat {
		delete(m.consumedKeys, id) // a fresh press starts a new lifecycle
	}
	modifier := k.Code >= tea.KeyLeftShift && k.Code <= tea.KeyIsoLevel5Shift
	cmd, taken := m.prefixRoute(k, modifier)
	if taken && !modifier && m.keyOwners[id] == nil {
		if m.consumedKeys == nil {
			m.consumedKeys = make(map[rune]bool)
		}
		m.consumedKeys[id] = true
	}
	return cmd, taken
}

// takeConsumedRelease reports whether a release ends a taken key's
// lifecycle; such a release reaches no child.
func (m *model) takeConsumedRelease(k tea.Key) bool {
	id := keyIdentity(k)
	if !m.consumedKeys[id] {
		return false
	}
	delete(m.consumedKeys, id)
	return true
}

func (m *model) prefixRoute(k tea.Key, modifier bool) (tea.Cmd, bool) {
	switch {
	case m.picker != nil:
		return m.pickerKey(k), true
	case m.activeNotice() != nil:
		if !modifier && !k.IsRepeat {
			m.notice = nil // any key dismisses the inert notice, and only that
		}
		return nil, true
	}
	if modifier && (m.prefix != nil || m.resizing()) {
		return nil, true // a modifier report never ends a prefix state
	}
	if m.prefix != nil {
		if k.IsRepeat {
			return nil, true // a held key's repeat is not a pane key
		}
		return m.prefixedKey(k), true
	}
	if m.resizing() {
		if d, cells, ok := resizeKey(k); ok {
			m.resizeBorder(d, cells)
			return prefixTick(resizeWindow), true
		}
		m.endResize() // any other key leaves the window and takes its route
	}
	if k.IsRepeat && m.consumedKeys[keyIdentity(k)] {
		return nil, true
	}
	if m.panePrefix().matches(k) && !k.IsRepeat {
		m.armPrefix()
		return nil, true
	}
	return nil, false
}

// armPrefix captures the surface a double prefix would go to.
func (m *model) armPrefix() {
	m.endNewTab()
	s := &prefixState{tab: m.tabs.Current()}
	p := m.tabs.Focused()
	switch {
	case m.focusPalette && m.console.Busy():
		s.surface = surfaceConsole
	case m.focusPalette || p == nil:
		s.surface = surfaceEditor
	case !p.Exited && p.Session != nil:
		s.surface, s.session = surfaceChild, p.Session
	}
	m.prefix = s
}

// disarmPrefix closes the menu and a resize window: host focus loss, a
// paste or a browser prompt, each then handled normally; a press ends a
// resize window. A live menu notice moves to the inert notice popup.
func (m *model) disarmPrefix() {
	m.keepNotice(m.prefix)
	m.prefix = nil
	m.endResize()
}

// checkPrefix ends the prefix states whose tab is no longer current.
func (m *model) checkPrefix() {
	cur := m.tabs.Current()
	if m.prefix != nil && m.prefix.tab != cur {
		m.keepNotice(m.prefix)
		m.prefix = nil
	}
	if m.resizing() && m.resizeTab != cur {
		m.endResize()
	}
	if m.newTab != nil && m.newTab.tab != cur {
		m.endNewTab()
	}
}

// prefixedKey is a key while the menu is open: a pane key acts and closes
// it, the prefix again goes to the captured surface, Esc closes it, Enter
// does nothing, and a refused or unknown key leaves it open with the
// refusal in its footer. Repeats do nothing.
func (m *model) prefixedKey(k tea.Key) tea.Cmd {
	p := m.prefix
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	switch {
	case m.panePrefix().matches(k):
		if p.surface == surfaceNone {
			m.paneNotice("nothing takes input here")
			return prefixTick(noticeTime)
		}
		m.prefix = nil
		m.forwardPrefix(p, k)
	case k.Code == tea.KeyEscape && mod == 0:
		m.prefix = nil
	case k.Code == tea.KeyEnter && mod == 0:
	default:
		for _, b := range paneKeys {
			if b.match(k) {
				return m.runBinding(b, k)
			}
		}
		m.paneNotice("no such pane key")
		return prefixTick(noticeTime)
	}
	return nil
}

// forwardPrefix sends a double prefix to the surface captured at arming,
// as a press and its release; a surface gone since then consumes it.
func (m *model) forwardPrefix(p *prefixState, k tea.Key) {
	var target keyTerminal
	switch p.surface {
	case surfaceNone:
		return // refused in prefixedKey, with the menu open
	case surfaceEditor:
		return // the prefix is not text
	case surfaceConsole:
		if !m.console.Busy() {
			return
		}
		target = m.console
	case surfaceChild:
		_, pane := m.tabs.Owner(func(pane *panes.Pane) bool { return pane.Session == p.session })
		if pane == nil || pane.Exited {
			return
		}
		target = p.session
	}
	m.scrollToLive()
	m.sendKey(target, k, false)
	m.sendKey(target, k, true)
	delete(m.keyOwners, keyIdentity(k))
}

func (m *model) resizing() bool {
	return m.resizeTab != nil && clock().Before(m.resizeUntil)
}

func (m *model) endResize() { m.resizeTab = nil }

func paneDir(code rune) (panes.Dir, bool) {
	switch code {
	case tea.KeyLeft:
		return panes.Left, true
	case tea.KeyRight:
		return panes.Right, true
	case tea.KeyUp:
		return panes.Up, true
	case tea.KeyDown:
		return panes.Down, true
	}
	return 0, false
}

// resizeKey reads a resize key: Ctrl+arrow moves a border one cell, Alt+arrow
// five.
func resizeKey(k tea.Key) (panes.Dir, int, bool) {
	d, ok := paneDir(k.Code)
	switch mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock); {
	case !ok:
	case mod == tea.ModCtrl:
		return d, 1, true
	case mod == tea.ModAlt:
		return d, 5, true
	}
	return 0, 0, false
}

// resizeBorder moves the focused pane's border and opens (or renews) the
// repeat window, even at the minimum, so the next Ctrl+arrow never
// switches tabs instead.
func (m *model) resizeBorder(d panes.Dir, cells int) {
	t := m.tabs.Current()
	if t == nil {
		return
	}
	if m.laidOut(t).ResizeFocused(d, cells) {
		m.layoutTab(t)
	}
	m.resizeTab, m.resizeUntil = t, clock().Add(resizeWindow)
	m.returnToPane()
}

// returnToPane gives focus back to the focused pane after a pane key, as
// the palette's pane keys always did. A busy command keeps running.
func (m *model) returnToPane() {
	if m.focusPalette && m.tabs.Current() != nil && m.confirm == nil {
		m.leavePaletteForTab()
	}
}

// startNewTab is prefix c: the palette opens on an empty line for a command
// that opens a new tab; the draft it held is set aside.
func (m *model) startNewTab() {
	if m.console.Busy() {
		m.paneNotice("a palette command is still running — wait for it")
		return
	}
	m.newTab = &newTabIntent{draft: m.pal.Stash(), tab: m.tabs.Current(), returnPalette: m.focusPalette}
	m.clearSuggestions()
	m.enterPalette()
}

// cancelNewTab is Esc during prefix c: the draft comes back and focus
// returns where it was.
func (m *model) cancelNewTab() {
	n := m.newTab
	m.newTab = nil
	m.pal.Restore(n.draft)
	m.clearSuggestions()
	if !n.returnPalette && m.tabs.Current() != nil {
		m.leavePaletteForTab()
	}
}

// endNewTab ends prefix c without a submission (a tab switch, the prefix
// again): an empty line takes the draft back; typed text stays, and the
// draft goes to history, so nothing typed is lost.
func (m *model) endNewTab() {
	n := m.newTab
	if n == nil {
		return
	}
	m.newTab = nil
	if line, _ := m.pal.Line(); line == "" {
		m.pal.Restore(n.draft)
	} else {
		m.pal.Keep(n.draft)
	}
}

// selectTabNumber selects the tab with strip number n (1…10).
func (m *model) selectTabNumber(n int) {
	if n < 1 || n > len(m.tabs.Tabs) {
		m.paneNotice(fmt.Sprintf("no tab %d", n))
		return
	}
	m.tabs.Select(n - 1)
	m.scrollToLive()
}

// digitNumber reads a tab digit: 1…9, and 0 for tab 10.
func digitNumber(k tea.Key) (int, bool) {
	if k.Mod&^(tea.ModShift|tea.ModCapsLock|tea.ModNumLock) != 0 || len(k.Text) != 1 || k.Text[0] < '0' || k.Text[0] > '9' {
		return 0, false
	}
	if k.Text == "0" {
		return 10, true
	}
	return int(k.Text[0] - '0'), true
}

// paneBinding is one row of the key table: the one authoritative mapping
// that dispatch, the pane menu and the key tests derive from.
type paneBinding struct {
	key  string // as the menu shows it
	help string // what it does
	text string // the key's text, when it is matched by text
	code rune   // otherwise its code, with mod
	mod  tea.KeyMod
	act  func(m *model, k tea.Key) tea.Cmd
}

func (b paneBinding) match(k tea.Key) bool {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	if b.text != "" {
		if b.text == "0…9" {
			_, ok := digitNumber(k)
			return ok
		}
		return k.Text == b.text && mod&^tea.ModShift == 0
	}
	return k.Code == b.code && mod == b.mod
}

// paneAct wraps an action on the current tab's layout that returns focus to
// the pane and lays the tab out again.
func paneAct(f func(m *model, t *tabs.Tab)) func(*model, tea.Key) tea.Cmd {
	return func(m *model, _ tea.Key) tea.Cmd {
		t := m.tabs.Current()
		if t == nil {
			return nil
		}
		m.laidOut(t)
		f(m, t)
		if m.tabs.Index(t) >= 0 {
			m.layoutTab(t)
		}
		m.returnToPane()
		return nil
	}
}

// splitAct opens an empty pane beside the focused one, which takes focus
// and opens the palette for it; a refusal stays in the menu.
func splitAct(vertical bool) func(*model, tea.Key) tea.Cmd {
	return func(m *model, _ tea.Key) tea.Cmd {
		if t := m.tabs.Current(); t != nil {
			m.openPlaceholder(t, vertical)
		}
		m.returnToPane()
		return nil
	}
}

func focusAct(d panes.Dir) func(*model, tea.Key) tea.Cmd {
	return paneAct(func(_ *model, t *tabs.Tab) { t.Layout.MoveFocus(d) })
}

func resizeAct(d panes.Dir, cells int) func(*model, tea.Key) tea.Cmd {
	return func(m *model, _ tea.Key) tea.Cmd {
		m.resizeBorder(d, cells)
		return prefixTick(resizeWindow)
	}
}

// paneKeys is PLAN-r2.3's R2 key table.
var paneKeys = []paneBinding{
	{key: "s", help: "switch this TUI space", text: "s", act: func(m *model, _ tea.Key) tea.Cmd { m.openSpacePicker(); return nil }},
	{key: "|", help: "split right: an empty pane opens beside the focused pane, for a command or a tab", text: "|", act: splitAct(true)},
	{key: "-", help: "split below", text: "-", act: splitAct(false)},
	{key: "←", help: "focus the pane that way", code: tea.KeyLeft, act: focusAct(panes.Left)},
	{key: "→", help: "focus the pane that way", code: tea.KeyRight, act: focusAct(panes.Right)},
	{key: "↑", help: "focus the pane that way", code: tea.KeyUp, act: focusAct(panes.Up)},
	{key: "↓", help: "focus the pane that way", code: tea.KeyDown, act: focusAct(panes.Down)},
	{key: "o", help: "focus the next pane", text: "o", act: paneAct(func(_ *model, t *tabs.Tab) { t.Layout.FocusNext() })},
	{key: "z", help: "zoom the focused pane, and back", text: "z", act: paneAct(func(_ *model, t *tabs.Tab) {
		if t.Layout.Bars() {
			t.Layout.ToggleZoom()
		}
	})},
	{key: "x", help: "detach the focused pane", text: "x", act: func(m *model, _ tea.Key) tea.Cmd {
		m.focusPalette = false // a pane key returns to the pane, after any popup
		m.closeFocusedPane()
		if m.confirm == nil && m.tabs.Current() == nil {
			m.focusPalette = true // no tab left: the palette keeps focus
		}
		return nil
	}},
	{key: "=", help: "spread the panes evenly", text: "=", act: paneAct(func(_ *model, t *tabs.Tab) {
		if t.Layout.Divided() {
			t.Layout.Equalize()
		}
	})},
	{key: "{", help: "swap the focused pane with the previous one", text: "{", act: paneAct(func(_ *model, t *tabs.Tab) { t.Layout.SwapFocused(false) })},
	{key: "}", help: "swap the focused pane with the next one", text: "}", act: paneAct(func(_ *model, t *tabs.Tab) { t.Layout.SwapFocused(true) })},
	{key: "Ctrl+←", help: "move the focused pane's border one cell that way; for a second, again without the prefix", code: tea.KeyLeft, mod: tea.ModCtrl, act: resizeAct(panes.Left, 1)},
	{key: "Ctrl+→", help: "move the focused pane's border one cell that way; for a second, again without the prefix", code: tea.KeyRight, mod: tea.ModCtrl, act: resizeAct(panes.Right, 1)},
	{key: "Ctrl+↑", help: "move the focused pane's border one cell that way; for a second, again without the prefix", code: tea.KeyUp, mod: tea.ModCtrl, act: resizeAct(panes.Up, 1)},
	{key: "Ctrl+↓", help: "move the focused pane's border one cell that way; for a second, again without the prefix", code: tea.KeyDown, mod: tea.ModCtrl, act: resizeAct(panes.Down, 1)},
	{key: "Alt+←", help: "five cells", code: tea.KeyLeft, mod: tea.ModAlt, act: resizeAct(panes.Left, 5)},
	{key: "Alt+→", help: "five cells", code: tea.KeyRight, mod: tea.ModAlt, act: resizeAct(panes.Right, 5)},
	{key: "Alt+↑", help: "five cells", code: tea.KeyUp, mod: tea.ModAlt, act: resizeAct(panes.Up, 5)},
	{key: "Alt+↓", help: "five cells", code: tea.KeyDown, mod: tea.ModAlt, act: resizeAct(panes.Down, 5)},
	{key: "!", help: "break the focused pane out into its own tab", text: "!", act: func(m *model, _ tea.Key) tea.Cmd {
		m.breakPane()
		return nil
	}},
	{key: "j", help: "bring a tab in: its whole layout lands below the focused pane, or in the empty pane; a digit picks at once", text: "j", act: func(m *model, _ tea.Key) tea.Cmd {
		m.openJoinPicker()
		return nil
	}},
	{key: "c", help: "new tab: type a command", text: "c", act: func(m *model, _ tea.Key) tea.Cmd {
		m.startNewTab()
		return nil
	}},
	{key: "n", help: "next tab", text: "n", act: func(m *model, _ tea.Key) tea.Cmd {
		m.tabs.Next()
		m.scrollToLive()
		return nil
	}},
	{key: "p", help: "previous tab", text: "p", act: func(m *model, _ tea.Key) tea.Cmd {
		m.tabs.Prev()
		m.scrollToLive()
		return nil
	}},
	{key: "1…9 0", help: "select tab N by its strip number (0 = tab 10)", text: "0…9", act: func(m *model, k tea.Key) tea.Cmd {
		n, _ := digitNumber(k)
		m.selectTabNumber(n)
		return nil
	}},
	{key: "w", help: "pick a tab from a list", text: "w", act: func(m *model, _ tea.Key) tea.Cmd {
		m.openPicker()
		return nil
	}},
	{key: "d", help: "detach the whole tab", text: "d", act: func(m *model, _ tea.Key) tea.Cmd {
		if t := m.tabs.Current(); t != nil {
			m.requestDetachTab(t)
		}
		return nil
	}},
	{key: "?", help: "this menu", text: "?", act: func(m *model, _ tea.Key) tea.Cmd {
		m.prefix = m.acting // the menu stays open
		return nil
	}},
}
