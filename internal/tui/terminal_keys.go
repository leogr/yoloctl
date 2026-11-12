package tui

import (
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

type keyTerminal interface {
	KeyboardFlags() int
	EncodeKey(terminal.Key) ([]byte, error)
	SendInput([]byte) error
}

// UV's CSI-u decoder swaps the XTerm Meta/Super bit positions for Kitty;
// its CSI ~ / ABCDFHPQS paths currently omit that conversion. Apply it only
// after a host Kitty support reply, and only to those functional key forms.
// Remove this workaround when the upstream regression is fixed.
func normalizeHostKey(k tea.Key, kitty bool) tea.Key {
	_, final := kittyCode(k.Code)
	if kitty && final != 'u' {
		meta, super := k.Mod&tea.ModMeta != 0, k.Mod&tea.ModSuper != 0
		k.Mod &^= tea.ModMeta | tea.ModSuper
		if meta {
			k.Mod |= tea.ModSuper
		}
		if super {
			k.Mod |= tea.ModMeta
		}
	}
	return k
}

func (m *model) sendKey(target keyTerminal, k tea.Key, release bool) {
	target = m.imageKey(target, k, release)
	b, err := childKey(target, k, release)
	if err != nil {
		m.setStatus(prettylog.KindError, err.Error())
		return
	}
	if len(b) > 0 {
		if err := target.SendInput(b); err != nil {
			m.setStatus(prettylog.KindError, err.Error())
			return
		}
		_, imageOwner := target.(*imagePasteKey)
		if !release && k.Code != tea.KeyExtended && (target.KeyboardFlags()&2 != 0 || imageOwner) {
			if m.keyOwners == nil {
				m.keyOwners = make(map[rune]keyTerminal)
			}
			if len(m.keyOwners) < 1024 {
				m.keyOwners[keyIdentity(k)] = target
			}
		}
	}
}

// The host has already applied keyboard layout/IME processing. Preserve those
// logical codepoints, including all Kitty modifiers, rather than reconstructing
// text from a US keymap. The native encoder handles the legacy terminal modes.
func childKey(target keyTerminal, k tea.Key, release bool) ([]byte, error) {
	flags := target.KeyboardFlags()
	if flags != 0 {
		return kittyKey(k, release, flags), nil
	}
	if release {
		return nil, nil
	}
	if k.Mod&(tea.ModHyper|tea.ModMeta|tea.ModSuper) != 0 {
		return kittyKey(k, false, 1), nil
	}
	action := uint32(1)
	if k.IsRepeat {
		action = 2
	}
	mods := uint16(0)
	for i, bit := range []tea.KeyMod{tea.ModShift, tea.ModCtrl, tea.ModAlt, tea.ModSuper, tea.ModCapsLock, tea.ModNumLock} {
		if k.Mod&bit != 0 {
			mods |= 1 << i
		}
	}
	code := k.Code
	text := k.Text
	if text == "" && unicode.IsPrint(code) {
		text = string(code)
		if k.ShiftedCode != 0 {
			text = string(k.ShiftedCode)
		}
	}
	// The native encoder expects printable layout text even for Control keys;
	// it chooses C0, CSI-u or modifyOtherKeys from the terminal's modes. A host
	// which already decoded an uppercase Control letter may omit its base code.
	if k.Mod&tea.ModCtrl != 0 && code >= 'A' && code <= 'Z' {
		code += 'a' - 'A'
	}
	// Legacy raw control messages have no separate base key; retain their bytes.
	if code >= 0 && code < 32 && code != tea.KeyEnter && code != tea.KeyTab && code != tea.KeyEscape && k.Mod == 0 {
		return []byte{byte(code)}, nil
	}
	physical := code
	if k.BaseCode != 0 {
		physical = k.BaseCode
	}
	name := physicalKey(physical)
	if name == "unidentified" && code > unicode.MaxRune {
		return kittyKey(k, false, 1), nil
	}
	if code > unicode.MaxRune {
		code = 0
	}
	return target.EncodeKey(terminal.Key{Name: name, Text: text, Codepoint: code, Mods: mods, Action: action})
}

func keyIdentity(k tea.Key) rune {
	if k.BaseCode != 0 {
		return k.BaseCode
	}
	return unicode.ToLower(k.Code)
}

func physicalKey(code rune) string {
	lower := unicode.ToLower(code)
	if lower >= 'a' && lower <= 'z' {
		return "key_" + string(lower)
	}
	if code >= '0' && code <= '9' {
		return "digit_" + string(code)
	}
	if code >= tea.KeyF1 && code <= tea.KeyF25 {
		return "f" + strconv.Itoa(int(code-tea.KeyF1+1))
	}
	if code >= tea.KeyKp0 && code <= tea.KeyKp9 {
		return "numpad_" + strconv.Itoa(int(code-tea.KeyKp0))
	}
	names := map[rune]string{
		' ': "space", '`': "backquote", '\\': "backslash", '[': "bracket_left", ']': "bracket_right", ',': "comma", '.': "period", '=': "equal", '-': "minus", '\'': "quote", ';': "semicolon", '/': "slash",
		tea.KeyEnter: "enter", tea.KeyTab: "tab", tea.KeyEscape: "escape", tea.KeyBackspace: "backspace",
		tea.KeyUp: "arrow_up", tea.KeyDown: "arrow_down", tea.KeyLeft: "arrow_left", tea.KeyRight: "arrow_right",
		tea.KeyHome: "home", tea.KeyEnd: "end", tea.KeyPgUp: "page_up", tea.KeyPgDown: "page_down", tea.KeyInsert: "insert", tea.KeyDelete: "delete",
		tea.KeyCapsLock: "caps_lock", tea.KeyNumLock: "num_lock", tea.KeyScrollLock: "scroll_lock", tea.KeyPause: "pause", tea.KeyPrintScreen: "print_screen",
		tea.KeyKpEnter: "numpad_enter", tea.KeyKpEqual: "numpad_equal", tea.KeyKpMultiply: "numpad_multiply", tea.KeyKpPlus: "numpad_add", tea.KeyKpMinus: "numpad_subtract", tea.KeyKpDecimal: "numpad_decimal", tea.KeyKpDivide: "numpad_divide", tea.KeyKpComma: "numpad_comma",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return "unidentified"
}

// kittyKey implements the host-event portion of the published Kitty protocol.
// The core owns negotiation, including screen stacks. We cannot manufacture
// events or alternate keycodes which the outer terminal never reported.
func kittyKey(k tea.Key, release bool, flags int) []byte {
	all := flags&8 != 0
	if release && (flags&2 == 0 || (!all && (k.Code == tea.KeyEnter || k.Code == tea.KeyTab || k.Code == tea.KeyBackspace))) {
		return nil
	}
	if !all && k.Code >= tea.KeyLeftShift && k.Code <= tea.KeyIsoLevel5Shift {
		return nil
	}
	mods := 0
	for i, bit := range []tea.KeyMod{tea.ModShift, tea.ModAlt, tea.ModCtrl, tea.ModSuper, tea.ModHyper, tea.ModMeta, tea.ModCapsLock, tea.ModNumLock} {
		if k.Mod&bit != 0 {
			mods |= 1 << i
		}
	}
	bindingMods := mods & 63
	if !all && !release {
		if bindingMods == 0 && (k.Code == tea.KeyEnter || k.Code == tea.KeyTab || k.Code == tea.KeyBackspace) {
			return []byte{byte(k.Code)}
		}
		if bindingMods&^1 == 0 && k.Text != "" {
			return []byte(k.Text)
		}
	}
	code, final := kittyCode(k.Code)
	// Text-only events have no physical key. Preserve their text and encode
	// key 0 when the child requested associated text; never invent Ctrl+Space.
	if k.Code == tea.KeyExtended && flags&24 == 24 && !release && k.Text != "" {
		code, final = 0, 'u'
	}
	if code < 0 {
		if !release && k.Text != "" {
			return []byte(k.Text)
		}
		return nil
	}
	b := fmt.Appendf(nil, "\x1b[%d", code)
	if flags&4 != 0 && final == 'u' && code >= 32 && code <= unicode.MaxRune {
		if k.ShiftedCode != 0 && k.ShiftedCode != k.Code {
			b = fmt.Appendf(b, ":%d", k.ShiftedCode)
		} else if k.BaseCode != 0 && k.BaseCode != k.Code {
			b = append(b, ':')
		}
		if k.BaseCode != 0 && k.BaseCode != k.Code {
			b = fmt.Appendf(b, ":%d", k.BaseCode)
		}
	}
	event := 1
	if flags&2 != 0 {
		if release {
			event = 3
		} else if k.IsRepeat {
			event = 2
		}
	}
	associated := flags&24 == 24 && !release && k.Text != ""
	// Text can contain multiple codepoints (IME/composition). Do not attach C0
	// controls, and never synthesize associated text for shortcut modifiers.
	if bindingMods&^1 != 0 {
		associated = false
	}
	for _, r := range k.Text {
		if unicode.IsControl(r) {
			associated = false
		}
	}
	if mods != 0 || event != 1 || associated {
		b = fmt.Appendf(b, ";%d", mods+1)
		if event != 1 {
			b = fmt.Appendf(b, ":%d", event)
		}
	}
	if associated {
		for i, r := range k.Text {
			sep := byte(':')
			if i == 0 {
				sep = ';'
			}
			b = append(b, sep)
			b = strconv.AppendInt(b, int64(r), 10)
		}
	}
	return append(b, final)
}

// Functional key codes from the Kitty specification. Navigation and F1–F12
// retain the specified legacy final bytes; F3 uses 13~ to avoid a CPR collision.
func kittyCode(code rune) (int, byte) {
	switch code {
	case tea.KeyInsert:
		return 2, '~'
	case tea.KeyDelete:
		return 3, '~'
	case tea.KeyUp:
		return 1, 'A'
	case tea.KeyDown:
		return 1, 'B'
	case tea.KeyRight:
		return 1, 'C'
	case tea.KeyLeft:
		return 1, 'D'
	case tea.KeyHome:
		return 1, 'H'
	case tea.KeyEnd:
		return 1, 'F'
	case tea.KeyPgUp:
		return 5, '~'
	case tea.KeyPgDown:
		return 6, '~'
	}
	if code >= tea.KeyF1 && code <= tea.KeyF12 {
		numbers := [...]int{1, 1, 13, 1, 15, 17, 18, 19, 20, 21, 23, 24}
		finals := [...]byte{'P', 'Q', '~', 'S', '~', '~', '~', '~', '~', '~', '~', '~'}
		return numbers[code-tea.KeyF1], finals[code-tea.KeyF1]
	}
	if code >= tea.KeyF13 && code <= tea.KeyF35 {
		return 57376 + int(code-tea.KeyF13), 'u'
	}
	if code >= tea.KeyCapsLock && code <= tea.KeyMenu {
		return 57358 + int(code-tea.KeyCapsLock), 'u'
	}
	if code >= tea.KeyKp0 && code <= tea.KeyKp9 {
		return 57399 + int(code-tea.KeyKp0), 'u'
	}
	if code >= tea.KeyMediaPlay && code <= tea.KeyMute {
		return 57428 + int(code-tea.KeyMediaPlay), 'u'
	}
	modKeys := []rune{tea.KeyLeftShift, tea.KeyLeftCtrl, tea.KeyLeftAlt, tea.KeyLeftSuper, tea.KeyLeftHyper, tea.KeyLeftMeta, tea.KeyRightShift, tea.KeyRightCtrl, tea.KeyRightAlt, tea.KeyRightSuper, tea.KeyRightHyper, tea.KeyRightMeta, tea.KeyIsoLevel3Shift, tea.KeyIsoLevel5Shift}
	for i, k := range modKeys {
		if code == k {
			return 57441 + i, 'u'
		}
	}
	kp := map[rune]int{tea.KeyKpDecimal: 57409, tea.KeyKpDivide: 57410, tea.KeyKpMultiply: 57411, tea.KeyKpMinus: 57412, tea.KeyKpPlus: 57413, tea.KeyKpEnter: 57414, tea.KeyKpEqual: 57415, tea.KeyKpSep: 57416, tea.KeyKpLeft: 57417, tea.KeyKpRight: 57418, tea.KeyKpUp: 57419, tea.KeyKpDown: 57420, tea.KeyKpPgUp: 57421, tea.KeyKpPgDown: 57422, tea.KeyKpHome: 57423, tea.KeyKpEnd: 57424, tea.KeyKpInsert: 57425, tea.KeyKpDelete: 57426, tea.KeyKpBegin: 57427}
	if v, ok := kp[code]; ok {
		return v, 'u'
	}
	if code >= 0 && code <= utf8.MaxRune {
		return int(code), 'u'
	}
	return -1, 0
}
