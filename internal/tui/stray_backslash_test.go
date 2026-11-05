package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// decodeAll decodes host bytes the way the TUI's input reader does.
func decodeAll(t *testing.T, input []byte) []tea.Key {
	t.Helper()
	var d uv.EventDecoder
	var keys []tea.Key
	for len(input) > 0 {
		n, ev := d.Decode(input)
		if n == 0 {
			t.Fatalf("no progress decoding %q", input)
		}
		switch ev := ev.(type) {
		case uv.KeyPressEvent:
			keys = append(keys, tea.Key(ev))
		case uv.MultiEvent:
			for _, e := range ev {
				if k, ok := e.(uv.KeyPressEvent); ok {
					keys = append(keys, tea.Key(k))
				}
			}
		}
		input = input[n:]
	}
	return keys
}

func textOf(keys []tea.Key) string {
	var b strings.Builder
	for _, k := range keys {
		if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModMeta|tea.ModHyper) == 0 {
			b.WriteString(k.Text)
		}
	}
	return b.String()
}

// Hypothesis B: no modified key other than the backslash key itself reaches a
// child as printable "\" text, in legacy or Kitty child modes.
func TestNoModifiedKeyBecomesBackslashText(t *testing.T) {
	for _, mode := range []string{"", "\x1b[>1u", "\x1b[>3u", "\x1b[>31u"} {
		s := newKeyTerminal(t)
		_, _ = s.Write([]byte(mode))
		for code := rune(32); code < 127; code++ {
			if code == '\\' {
				continue
			}
			for _, mod := range []tea.KeyMod{0, tea.ModShift, tea.ModCtrl, tea.ModAlt, tea.ModCtrl | tea.ModShift, tea.ModAlt | tea.ModShift, tea.ModCtrl | tea.ModAlt} {
				for _, base := range []rune{0, '\\'} {
					k := tea.Key{Code: code, Mod: mod, BaseCode: base}
					if mod&^tea.ModShift == 0 {
						k.Text = string(code)
					}
					got, err := childKey(s, k, false)
					if err != nil {
						t.Fatal(err)
					}
					if txt := textOf(decodeAll(t, got)); strings.Contains(txt, `\`) {
						t.Errorf("mode=%q key=%#v -> %q -> text %q", mode, k, got, txt)
					}
				}
			}
		}
		// Enter variants.
		for _, mod := range []tea.KeyMod{0, tea.ModShift, tea.ModCtrl, tea.ModAlt} {
			got, err := childKey(s, tea.Key{Code: tea.KeyEnter, Mod: mod}, false)
			if err != nil {
				t.Fatal(err)
			}
			if txt := textOf(decodeAll(t, got)); strings.Contains(txt, `\`) {
				t.Errorf("mode=%q enter mod=%d -> %q -> text %q", mode, mod, got, txt)
			}
		}
	}
}

// Hypothesis A: a host binding that types "\" + newline (the Claude Code
// line-continuation idiom, e.g. Ghostty `keybind = shift+enter=text:\\\r` or
// `text:\\\n`) reaches yoloctl as a literal backslash key followed by Enter
// or Ctrl+J; nothing turns it into one newline.
func TestHostBackslashNewlineBindingReachesChildLiterally(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"\\\r", "\\ enter"},
		{"\\\n", "\\ ctrl+j"},
	} {
		keys := decodeAll(t, []byte(tc.host))
		var got []string
		for _, k := range keys {
			got = append(got, tea.KeyPressMsg(k).String())
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("host %q decoded %q, want %q", tc.host, got, tc.want)
		}
		for _, mode := range []string{"", "\x1b[>3u"} {
			s := newKeyTerminal(t)
			_, _ = s.Write([]byte(mode))
			var out []byte
			for _, k := range keys {
				b, err := childKey(s, k, false)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, b...)
			}
			t.Logf("host %q child mode %q -> child bytes %s", tc.host, mode, fmt.Sprintf("%q", out))
			if !strings.HasPrefix(string(out), `\`) {
				t.Errorf("host %q child mode %q: %q, want a leading literal backslash", tc.host, mode, out)
			}
		}
	}
}

// streamEvents feeds chunks, one read each, to the real UV terminal reader
// (Bubble Tea's input path) and returns every event it emits after the escape
// timeout has expired.
func streamEvents(t *testing.T, chunks ...string) []string {
	t.Helper()
	r, w := io.Pipe()
	reader := uv.NewTerminalReader(r, "xterm-ghostty")
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan uv.Event, 4096)
	done := make(chan struct{})
	go func() { _ = reader.StreamEvents(ctx, events); close(done) }()
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * uv.DefaultEscTimeout)
	cancel()
	_ = w.Close()
	<-done
	close(events)
	var got []string
	for ev := range events {
		got = append(got, fmt.Sprintf("%T %#v", ev, ev))
	}
	return got
}

// A host reply terminated by a 7-bit ST (ESC \\) may be split by a read at
// any byte, including between its ESC and "\\" (fix-stray-backslash-input:
// the reply was dropped or cut short and "\\" became a key press). Every split
// of every ST-capable family yields the unsplit events: the reply with its
// value, then the backslash and "a" the user really typed.
func TestSTReplySplitAtEveryPoint(t *testing.T) {
	for _, tc := range []struct{ name, reply, kind string }{
		{"osc4", "\x1b]4;7;rgb:c0c0/c0c0/c0c0\x1b\\", "uv.UnknownOscEvent"},
		{"osc11", "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\", "uv.BackgroundColorEvent"},
		{"dcs", "\x1bP>|ghostty 1.2.3\x1b\\", "uv.TerminalVersionEvent"},
		{"apc", "\x1b_Gi=31;OK\x1b\\", "uv.KittyGraphicsEvent"},
	} {
		typed := tc.reply + `\a`
		want := streamEvents(t, typed)
		if len(want) != 3 || !strings.HasPrefix(want[0], tc.kind) || !strings.Contains(want[1], `Text:"\\"`) || !strings.Contains(want[2], `Text:"a"`) {
			t.Fatalf("%s unsplit: %q", tc.name, want)
		}
		for cut := 1; cut <= len(tc.reply); cut++ {
			if got := streamEvents(t, typed[:cut], typed[cut:]); !slices.Equal(got, want) {
				t.Errorf("%s cut %d (%q|%q): %q, want %q", tc.name, cut, typed[:cut], typed[cut:], got, want)
			}
		}
	}
}

func keyEvent(k uv.KeyPressEvent) string { return fmt.Sprintf("%T %#v", k, k) }

// Short escapes, the timeout and cancellation keep their previous outcomes.
func TestSTSplitWaitKeepsEscapeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"lone esc", []string{"\x1b"}, []string{keyEvent(uv.KeyPressEvent{Code: uv.KeyEscape})}},
		{"alt+]", []string{"\x1b]"}, []string{keyEvent(uv.KeyPressEvent{Code: ']', Mod: uv.ModAlt})}},
		{"unterminated reply expires silently", []string{"\x1b]4;7;rgb:c0c0/c0c0/c0c0\x1b"}, nil},
		{"cancelled reply then key", []string{"\x1b]4;7;rgb:c0c0/c0c0/c0c0\x1b", "x"}, []string{keyEvent(uv.KeyPressEvent{Code: 'x', Text: "x"})}},
	} {
		if got := streamEvents(t, tc.chunks...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
