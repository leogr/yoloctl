package tui

import (
	"bytes"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/terminal"
)

type keyTestTerminal struct {
	*terminal.Terminal
	sent bytes.Buffer
}

func (s *keyTestTerminal) KeyboardFlags() int       { return s.Get(8) }
func (s *keyTestTerminal) SendInput(p []byte) error { _, err := s.sent.Write(p); return err }
func newKeyTerminal(t *testing.T) *keyTestTerminal {
	t.Helper()
	core, err := terminal.New(4, 40, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	return &keyTestTerminal{Terminal: core}
}

func TestChildKeyNegotiation(t *testing.T) {
	s := newKeyTerminal(t)
	for _, tc := range []struct {
		seq     string
		key     tea.Key
		release bool
		want    string
	}{
		{"", tea.Key{Code: tea.KeyEnter}, false, "\r"},
		{"", tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}, false, "\x1b\r"},
		{"", tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, false, "\x1b[27;2;13~"},
		{"", tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}, false, "\x1b[27;5;13~"},
		{"", tea.Key{Code: 'j', Mod: tea.ModCtrl}, false, "\n"},
		{"\x1b[?1h", tea.Key{Code: tea.KeyUp}, false, "\x1bOA"},
		{"\x1b[>1u", tea.Key{Code: 'c', Mod: tea.ModCtrl}, false, "\x1b[99;5u"},
		{"", tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, false, "\x1b[13;2u"},
		{"", tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}, false, "\x1b[13;5u"},
		{"", tea.Key{Code: 'j', Mod: tea.ModCtrl}, false, "\x1b[106;5u"},
		{"", tea.Key{Code: 'a', Text: "a"}, true, ""},
		{"\x1b[>3u", tea.Key{Code: tea.KeyF5}, true, "\x1b[15;1:3~"},
		{"", tea.Key{Code: tea.KeyF3, IsRepeat: true}, false, "\x1b[13;1:2~"},
		{"", tea.Key{Code: tea.KeyEnter}, true, ""},
		{"\x1b[>31u", tea.Key{Code: tea.KeyEnter}, true, "\x1b[13;1:3u"},
		{"", tea.Key{Code: 'a', Text: "A", ShiftedCode: 'A', Mod: tea.ModShift}, false, "\x1b[97:65;2;65u"},
		{"", tea.Key{Code: 'я', Text: "Я", ShiftedCode: 'Я', BaseCode: 'z', Mod: tea.ModShift}, false, "\x1b[1103:1071:122;2;1071u"},
		{"\x1bc", tea.Key{Code: 'a', Text: "a"}, false, "a"},
	} {
		_, _ = s.Write([]byte(tc.seq))
		got, err := childKey(s, tc.key, tc.release)
		if err != nil || string(got) != tc.want {
			t.Errorf("seq=%q key=%#v release=%t: %q (%v), want %q", tc.seq, tc.key, tc.release, got, err, tc.want)
		}
	}
}

func TestHostEnterAndControlJAfterKeyboardSnapshot(t *testing.T) {
	source := newKeyTerminal(t)
	_, _ = source.Write([]byte("\x1b[>3u"))
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	core, err := terminal.RestoreSnapshot(t.Context(), snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close() }()
	restored := &keyTestTerminal{Terminal: core}
	if restored.KeyboardFlags() != 3 {
		t.Fatal("keyboard negotiation was not restored")
	}
	// A PTY test driver must send CR for the Enter key. LF is Ctrl+J,
	// which has a different wire encoding when the child requests Kitty.
	for _, tc := range []struct{ input, want string }{
		{"\r", "\r"},
		{"\n", "\x1b[106;5u"},
		{"\x1b[13u", "\r"},
		{"\x1b[106;5u", "\x1b[106;5u"},
	} {
		var decoder uv.EventDecoder
		n, event := decoder.Decode([]byte(tc.input))
		key, ok := event.(uv.KeyPressEvent)
		if !ok || n != len(tc.input) {
			t.Fatalf("input %q decoded as %T (%d bytes)", tc.input, event, n)
		}
		got, err := childKey(restored, tea.Key(key), false)
		if err != nil || string(got) != tc.want {
			t.Errorf("input %q reached child as %q (%v), want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestKittyHostDecodeChildEncodeRoundTrip(t *testing.T) {
	for _, code := range []string{"97", "1103:1071:122", "13", "15~", "13~", "1P", "1A", "57376", "57442", "57443", "57452"} {
		for mod := 1; mod <= 256; mod++ {
			for _, event := range []int{1, 2, 3} {
				final := byte('u')
				number := code
				if last := code[len(code)-1]; last < '0' || last > '9' {
					final = last
					number = code[:len(code)-1]
				}
				input := fmt.Sprintf("\x1b[%s;%d:%d%c", number, mod, event, final)
				var decoder uv.EventDecoder
				n, ev := decoder.Decode([]byte(input))
				var key tea.Key
				switch ev := ev.(type) {
				case uv.KeyPressEvent:
					key = tea.Key(ev)
				case uv.KeyReleaseEvent:
					key = tea.Key(ev)
				default:
					t.Fatalf("decode %q: %T", input, ev)
				}
				if n != len(input) {
					t.Fatalf("partial decode %q", input)
				}
				key = normalizeHostKey(key, true)
				output := kittyKey(key, event == 3, 31)
				_, round := decoder.Decode(output)
				var back tea.Key
				switch v := round.(type) {
				case uv.KeyPressEvent:
					back = tea.Key(v)
				case uv.KeyReleaseEvent:
					back = tea.Key(v)
				default:
					t.Fatalf("round trip %q -> %q: %T", input, output, round)
				}
				back = normalizeHostKey(back, true)
				if back.Code != key.Code || back.Mod != key.Mod || back.ShiftedCode != key.ShiftedCode || back.BaseCode != key.BaseCode || back.IsRepeat != key.IsRepeat {
					t.Fatalf("%q -> %q: %#v, want %#v", input, output, back, key)
				}
			}
		}
	}
}

func TestKeyReleaseFollowsOriginalOwner(t *testing.T) {
	first, second := newKeyTerminal(t), newKeyTerminal(t)
	_, _ = first.Write([]byte("\x1b[>31u"))
	_, _ = second.Write([]byte("\x1b[>31u"))
	m := newTestModel(t, 80, 24)
	m.sendKey(first, tea.Key{Code: 'a', Text: "a"}, false)
	m.focusPalette = true
	_, _ = m.Update(tea.KeyReleaseMsg{Code: 'a'})
	if got := first.sent.String(); got != "\x1b[97;1;97u\x1b[97;1:3u" {
		t.Fatalf("press/release owner got %q", got)
	}
	if second.sent.Len() != 0 {
		t.Fatal("release sent to another surface")
	}
	// A consumed palette press has no owner and cannot leak a bare release.
	_, _ = m.Update(tea.KeyReleaseMsg{Code: 'y', Mod: tea.ModCtrl})
	if len(m.keyOwners) != 0 {
		t.Fatal("released owner retained")
	}
}

func TestHostKittyFunctionalModifierNormalization(t *testing.T) {
	for _, tc := range []struct {
		seq  string
		want tea.KeyMod
	}{
		{"\x1b[15;9:3~", tea.ModSuper}, {"\x1b[1;33:2A", tea.ModMeta}, {"\x1b[97;9u", tea.ModSuper}, {"\x1b[97;33u", tea.ModMeta},
	} {
		var d uv.EventDecoder
		_, event := d.Decode([]byte(tc.seq))
		var key tea.Key
		switch e := event.(type) {
		case uv.KeyPressEvent:
			key = tea.Key(e)
		case uv.KeyReleaseEvent:
			key = tea.Key(e)
		default:
			t.Fatalf("%q: %T", tc.seq, event)
		}
		if got := normalizeHostKey(key, true); got.Mod != tc.want {
			t.Fatalf("%q: modifier %d, want %d", tc.seq, got.Mod, tc.want)
		}
		if got := normalizeHostKey(key, false); got != key {
			t.Fatal("changed a legacy host event")
		}
	}
}

// Exercise the decoder and actual palette routing together. Lock-state bits
// are terminal reports, not shortcut modifiers, and must not disable Tab.
func TestPaletteTabFromHostReports(t *testing.T) {
	for _, input := range []string{"\t", "\x1b[9u", "\x1b[9;1u", "\x1b[9;65u", "\x1b[9;129u", "\x1b[9;193u"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			m := newTestModel(t, 80, 24)
			m.hostKitty = true
			m.pal.Insert("box a")
			m.sugg = candidates("apply", "attach")
			m.suggSel = 1
			var decoder uv.EventDecoder
			n, ev := decoder.Decode([]byte(input))
			key, ok := ev.(uv.KeyPressEvent)
			if !ok || n != len(input) {
				t.Fatalf("decode: %d %#v", n, ev)
			}
			_, _ = m.Update(tea.KeyPressMsg(key))
			if line, _ := m.pal.Line(); line != "box attach " {
				t.Fatalf("palette = %q, want selected completion; decoded %#v", line, key)
			}
			_, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyTab, Mod: key.Mod})
			if line, _ := m.pal.Line(); line != "box attach " {
				t.Fatalf("release changed completion: %q", line)
			}
		})
	}
}
