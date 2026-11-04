package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// Decode the sequences the host actually sends, then re-encode them for the
// child. This catches modifier loss across both sides of the TUI boundary.
func TestEncodeKeyLegacyRoundTrip(t *testing.T) {
	target := newKeyTerminal(t)
	sequences := []string{"\x1b\x7f", "\x1b\x01", "\x1bé", "\x1bA", "\x1b[Z", "\x1bOP", "\x1bOQ", "\x1bOR", "\x1bOS"}
	for _, final := range []byte("ABCDHFPQRS") {
		for mod := 2; mod <= 8; mod++ {
			sequences = append(sequences, fmt.Sprintf("\x1b[1;%d%c", mod, final))
		}
	}
	for _, n := range []int{2, 3, 5, 6, 15, 17, 18, 19, 20, 21, 23, 24} {
		sequences = append(sequences, fmt.Sprintf("\x1b[%d~", n))
		for mod := 2; mod <= 8; mod++ {
			sequences = append(sequences, fmt.Sprintf("\x1b[%d;%d~", n, mod))
		}
	}
	for _, seq := range sequences {
		t.Run(fmt.Sprintf("%q", seq), func(t *testing.T) {
			var decoder uv.EventDecoder
			n, event := decoder.Decode([]byte(seq))
			// Modified F3 shares its sequence with a cursor-position reply.
			if events, ok := event.(uv.MultiEvent); ok {
				event = events[0]
			}
			key, ok := event.(uv.KeyPressEvent)
			if !ok || n != len(seq) {
				t.Fatalf("decode %q: %d, %#v", seq, n, event)
			}
			got, err := childKey(target, tea.Key(key), false)
			if err != nil {
				t.Fatal(err)
			}
			want := seq
			if key.Code == tea.KeyF3 && key.Mod != 0 {
				// Ghostty uses the unambiguous xterm tilde form for modified
				// F3; CSI 1;modifier R also denotes a cursor-position reply.
				want = "\x1b[13" + seq[3:len(seq)-1] + "~"
			}
			if string(got) != want {
				t.Errorf("%s: forwarded %q, want %q", key.Keystroke(), got, want)
			}
		})
	}
}

func TestEncodeKeyTextAndControl(t *testing.T) {
	target := newKeyTerminal(t)
	for _, tc := range []struct {
		key  tea.Key
		want string
	}{
		{tea.Key{Code: 'a', Text: "A", Mod: tea.ModCapsLock}, "A"},
		{tea.Key{Code: 'a', Text: "a", Mod: tea.ModNumLock}, "a"},
		{tea.Key{Code: tea.KeyTab, Mod: tea.ModShift | tea.ModCapsLock}, "\x1b[Z"},
		{tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModAlt | tea.ModShift}, "\x1bA"},
		{tea.Key{Code: 'c', Mod: tea.ModCtrl}, "\x03"},
		// These distinguishable host events follow Ghostty's native legacy
		// profile (key_encode.zig: ctrl+shift+letter / fixterms tests).
		{tea.Key{Code: 'C', Mod: tea.ModCtrl | tea.ModShift}, "\x1b[99;6u"},
		{tea.Key{Code: '[', Mod: tea.ModCtrl}, "\x1b[91;5u"},
		{tea.Key{Code: '\\', Mod: tea.ModCtrl}, "\x1c"},
		{tea.Key{Code: ' ', Mod: tea.ModCtrl | tea.ModAlt}, "\x1b\x00"},
		{tea.Key{Code: 'x', Mod: tea.ModSuper}, "\x1b[120;9u"},
		{tea.Key{Code: 0x19}, "\x19"},
	} {
		got, err := childKey(target, tc.key, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%#v: %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestReservedKeysDoNotConsumeExtraModifiers(t *testing.T) {
	for _, extra := range []tea.KeyMod{tea.ModAlt, tea.ModShift, tea.ModSuper} {
		if isPaletteKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl | extra}, "ctrl+y") {
			t.Errorf("palette intercepted ctrl+y with modifier %v", extra)
		}
		if isInterruptKey(tea.Key{Code: 'c', Mod: tea.ModCtrl | extra}) {
			t.Errorf("interrupt intercepted ctrl+c with modifier %v", extra)
		}
	}
	if !isPaletteKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl | tea.ModCapsLock}, "ctrl+y") {
		t.Error("Caps Lock defeated palette key")
	}
}

func TestEncodeApplicationCursorKeys(t *testing.T) {
	target := newKeyTerminal(t)
	_, _ = target.Write([]byte("\x1b[?1h"))
	for _, tc := range []struct {
		key  tea.Key
		want string
	}{
		{tea.Key{Code: tea.KeyUp}, "\x1bOA"},
		{tea.Key{Code: tea.KeyLeft}, "\x1bOD"},
		{tea.Key{Code: tea.KeyHome}, "\x1bOH"},
		{tea.Key{Code: tea.KeyUp, Mod: tea.ModAlt}, "\x1b[1;3A"},
		{tea.Key{Code: tea.KeyPgUp}, "\x1b[5~"},
	} {
		got, err := childKey(target, tc.key, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%#v: %q, want %q", tc.key, got, tc.want)
		}
	}
	if got := string(alternateScrollKey(tea.MouseWheelUp, true)); got != "\x1bOA" {
		t.Errorf("wheel in application mode: %q", got)
	}
}
