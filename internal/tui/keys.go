package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/config"
)

// isPaletteKey matches the palette toggle (tui.yaml palette_key,
// default ctrl+y) across input representations. Spike finding H4:
// NEVER match hotkeys with strict modifier equality —
// kitty-keyboard-protocol terminals report lock state (caps/num) as
// extra modifier bits alongside ctrl. Match the canonical keystroke
// string; for the default key also bitmask the modifiers and accept
// the raw EM byte as final fallbacks (a custom key matches by
// keystroke string only — bubbletea's canonical form is already
// lock-state-free).
func isPaletteKey(msg tea.KeyPressMsg, key string) bool {
	if msg.Keystroke() == key {
		return true
	}
	if key != "ctrl+y" {
		return false
	}
	k := msg.Key()
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	if k.Code == 0x19 && k.Text == "" && mod == 0 {
		return true
	}
	return mod == tea.ModCtrl && (k.Code == 'y' || k.Code == 'Y')
}

// isInterruptKey matches Ctrl+C (and the raw ETX byte), the overlay's
// interrupt for a running command. Bitmasked like isPaletteKey so
// kitty-protocol lock-state bits don't defeat the match.
func isInterruptKey(k tea.Key) bool {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	if k.Code == 0x03 && k.Text == "" && mod == 0 {
		return true
	}
	return mod == tea.ModCtrl && (k.Code == 'c' || k.Code == 'C')
}

// Alternate-scroll wheel events use the child's cursor-key mode.
func cursorKey(final byte, application bool) []byte {
	prefix := byte('[')
	if application {
		prefix = 'O'
	}
	return []byte{0x1b, prefix, final}
}

// chord is a parsed keystroke: its modifiers, lock state excluded, and its
// key, lower case. Two spellings of one keystroke parse to the same chord,
// so configured keys compare by chord, never by string.
type chord struct {
	mod  tea.KeyMod
	code rune
}

var chordMods = map[string]tea.KeyMod{
	"ctrl": tea.ModCtrl, "alt": tea.ModAlt, "shift": tea.ModShift,
	"meta": tea.ModMeta, "hyper": tea.ModHyper, "super": tea.ModSuper,
}

// parseChord reads a keystroke in Bubble Tea's canonical form, such as
// "ctrl+q" or "ctrl+^": modifiers in any order, then one printable key. A
// key that would type text (no modifier but Shift) is refused, since a
// prefix must never take a character from typing.
func parseChord(s string) (chord, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	parts := strings.Split(s, "+")
	if strings.HasSuffix(s, "++") { // "ctrl++": the key is the plus sign
		parts = append(parts[:len(parts)-2], "+")
	}
	var c chord
	for _, part := range parts[:len(parts)-1] {
		mod, ok := chordMods[part]
		if !ok || c.mod&mod != 0 {
			return chord{}, false
		}
		c.mod |= mod
	}
	key := []rune(parts[len(parts)-1])
	if len(key) != 1 || !unicode.IsPrint(key[0]) || key[0] == ' ' || c.mod&^tea.ModShift == 0 {
		return chord{}, false
	}
	c.code = key[0]
	return c, true
}

// matches reports whether a host key is this chord. Lock-state bits are
// masked (spike finding H4); a layout's base key and a Kitty report of a
// shifted symbol (Ctrl+Shift+6 for Ctrl+^) match too, and so does the raw
// control byte a legacy host or a synthesized event carries.
func (c chord) matches(k tea.Key) bool {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	if mod == c.mod && (unicode.ToLower(k.Code) == c.code || k.BaseCode != 0 && unicode.ToLower(k.BaseCode) == c.code) {
		return true
	}
	if c.mod&tea.ModShift == 0 && k.ShiftedCode == c.code && mod == c.mod|tea.ModShift {
		return true
	}
	b, ok := c.controlByte()
	return ok && k.Code == rune(b) && k.Text == "" && mod == 0
}

// controlByte is the C0 byte a legacy terminal sends for a Ctrl chord.
func (c chord) controlByte() (byte, bool) {
	if c.mod != tea.ModCtrl {
		return 0, false
	}
	switch {
	case c.code >= 'a' && c.code <= 'z':
		return byte(c.code - 'a' + 1), true
	case c.code == '\\' || c.code == ']' || c.code == '^' || c.code == '_':
		return byte(c.code - 0x40), true
	}
	return 0, false
}

// String is the chord as the legend shows it: ^Q for Ctrl and a letter,
// otherwise the modifiers spelled out.
func (c chord) String() string {
	if c.mod == tea.ModCtrl {
		return "^" + strings.ToUpper(string(c.code))
	}
	var b strings.Builder
	for _, m := range []struct {
		mod  tea.KeyMod
		name string
	}{{tea.ModCtrl, "Ctrl+"}, {tea.ModAlt, "Alt+"}, {tea.ModShift, "Shift+"}, {tea.ModMeta, "Meta+"}, {tea.ModHyper, "Hyper+"}, {tea.ModSuper, "Super+"}} {
		if c.mod&m.mod != 0 {
			b.WriteString(m.name)
		}
	}
	b.WriteString(strings.ToUpper(string(c.code)))
	return b.String()
}

// prefixFallbacks are tried in order when pane_prefix does not parse or
// equals the palette key; the first that differs from the palette key wins.
var prefixFallbacks = []string{config.DefaultPanePrefix, "ctrl+^"}

// resolvePanePrefix validates tui.yaml pane_prefix against the palette key:
// an unparsable prefix, or one equal to the palette key, is replaced by the
// first fallback that differs from it, with a notice for the status line.
func resolvePanePrefix(cfg *config.TUI) (prefix chord, notice string) {
	want := cfg.GetPanePrefix()
	palette, paletteOK := parseChord(cfg.GetPaletteKey())
	prefix, ok := parseChord(want)
	switch {
	case !ok:
		notice = fmt.Sprintf("pane_prefix %q is not a key chord", want)
	case paletteOK && prefix == palette:
		notice = fmt.Sprintf("pane_prefix %q is the palette key", want)
	default:
		return prefix, ""
	}
	for _, name := range prefixFallbacks {
		if c, _ := parseChord(name); !paletteOK || c != palette {
			prefix = c
			break
		}
	}
	return prefix, notice + "; using " + prefix.String() + " as the pane prefix"
}
