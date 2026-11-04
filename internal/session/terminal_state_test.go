package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

func replayTestTerminal(t *testing.T) *terminal.Terminal {
	t.Helper()
	vt, err := terminal.New(4, 20, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vt.Close() })
	return vt
}

func writeReplayTerminal(t *testing.T, vt *terminal.Terminal, data string) {
	t.Helper()
	if _, err := vt.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

func checkReplayInput(t *testing.T, live, replay *terminal.Terminal) {
	t.Helper()
	// Compatibility replay restores an unspecified preference as selection
	// override; explicit capture must retain the same effective behavior.
	if (live.MouseMode().ShiftCapture == terminal.MouseShiftReport) != (replay.MouseMode().ShiftCapture == terminal.MouseShiftReport) {
		t.Fatal("Shift-mouse capture differs after reattach")
	}
	for _, field := range []uint32{6, 8, 14, 15} {
		if got, want := replay.Get(field), live.Get(field); got != want {
			t.Fatalf("field %d after reattach = %d, uninterrupted = %d", field, got, want)
		}
	}
	for _, mode := range []uint16{1, 9, 47, 66, 67, 1000, 1002, 1003, 1004, 1005, 1006, 1007, 1015, 1016, 1035, 1036, 1039, 1047, 1049, 2004} {
		if replay.Mode(mode) != live.Mode(mode) {
			t.Fatalf("mode %d differs after reattach", mode)
		}
	}
	for _, key := range []terminal.Key{
		{Name: "enter", Codepoint: '\r', Mods: 1, Action: 1},
		{Name: "arrow_up", Action: 1},
		{Name: "tab", Codepoint: '\t', Action: 1},
		{Name: "key_j", Codepoint: 'j', Mods: 2, Action: 1},
		{Name: "backspace", Action: 1},
		{Name: "key_a", Codepoint: 'a', Text: "a", Mods: 4, Action: 1},
		{Name: "numpad_1", Text: "1", Action: 1},
		{Name: "key_a", Codepoint: 'a', Text: "a", Action: 0},
	} {
		want, err := live.EncodeKey(key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := replay.EncodeKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("key %s: reattach %q, uninterrupted %q", key.Name, got, want)
		}
	}
}

// The same transcript drives an uninterrupted native terminal and the actual
// holder replay assembly. Vary both the ring's eviction boundary and PTY read
// fragmentation. Later pops expose incorrectly restored stack depth/order.
func TestTerminalInputReplayAcrossRingOverflow(t *testing.T) {
	for _, tc := range []struct{ name, setup, tail string }{
		{"kitty", "\x1b[>3u", ""},
		{"nested", "\x1b[>1u\x1b[>3u\x1b[>31u", "\x1b[<u"},
		{"set-or-remove", "\x1b[>1u\x1b[=2;2u\x1b[=1;3u", "\x1b[=5u"},
		{"wrapped-stack", "\x1b[>1u\x1b[>2u\x1b[>3u\x1b[>4u\x1b[>5u\x1b[>6u\x1b[>7u\x1b[>8u\x1b[>9u", ""},
		{"alternate-stack", "\x1b[>1u\x1b[?1049h\x1b[>3u\x1b[>31u", ""},
		{"primary-again", "\x1b[>1u\x1b[?1049h\x1b[>3u\x1b[?1049l", ""},
		{"reset", "\x1b[>31u\x1b[?2004h\x1b[?1049h\x1b[>3u\x1bc", ""},
		{"reset-in-tail", "\x1b[>31u\x1b[?2004h", "\x1bc"},
		{"legacy", "\x1b[?1;66;67;1003;1006;2004h\x1b[?1035;1036l\x1b[>4;2m", ""},
		{"saved-modes", "\x1b[?1;2004h\x1b[?1;2004s\x1b[?1;2004l", "\x1b[?1;2004r"},
		{"saved-screen", "\x1b[>1u\x1b[?1049h\x1b[>3u\x1b[?1049s\x1b[?1049l", "\x1b[?1049r"},
		{"mouse-reset", "\x1b[?1003;1006h\x1b[?1000h\x1b[?1003l", ""},
		{"shift-capture", "\x1b[?1003;1006h\x1b[>1s", ""},
		{"shift-capture-reset", "\x1b[>1s", "\x1b[>0s"},
		{"osc-payload", "\x1b[>3u\x1b]2;title \x1b[>31u\x1b[?2004h\x07", ""},
		{"dcs-payload", "\x1b[>3u\x1bPq\x1b[>31u\x1b[?2004h\x1b\\", ""},
	} {
		for _, capacity := range []int{1, 3, 7, 64, 4096} {
			for _, chunk := range []int{1, 17, 4096} {
				t.Run(fmt.Sprintf("%s/ring%d/chunk%d", tc.name, capacity, chunk), func(t *testing.T) {
					state := &terminalInputState{}
					defer state.Close()
					h := &holder{ring: NewRing(capacity), terminalInput: state, logf: t.Logf}
					live, replay := replayTestTerminal(t), replayTestTerminal(t)
					transcript := tc.setup + strings.Repeat("line\r\n", 25) + tc.tail
					writeReplayTerminal(t, live, transcript)
					for p := []byte(transcript); len(p) > 0; {
						n := min(chunk, len(p))
						h.retainReplay(p[:n])
						p = p[n:]
					}
					snapshot, err := h.replay()
					if err != nil {
						t.Fatal(err)
					}
					writeReplayTerminal(t, replay, string(snapshot))
					checkReplayInput(t, live, replay)
					for _, suffix := range []string{"\x1b[<u", "\x1b[<2u", "\x1b[?47l", "\x1b[<u", "\x1b[?47h", "\x1b[<u", "\x1b[>15u", "\x1b[<8u"} {
						writeReplayTerminal(t, live, suffix)
						writeReplayTerminal(t, replay, suffix)
						checkReplayInput(t, live, replay)
					}
				})
			}
		}
	}
}

func TestTerminalInputReplayEverySequenceBoundary(t *testing.T) {
	for _, sequence := range []string{
		"\x1b[>31u", "\x1b[?1049h", "\x1b[?1;2004h", "\x1b[<2u",
		"\x1b[=2;2u", "\x1b[?2004r", "\x1bc", "界😀",
		"\x1b]2;a title\x1b\\", "\x1bPqpayload\x1b\\",
		"\x1b[>1s", "\x1b[>0s",
	} {
		for cut := 1; cut < len(sequence); cut++ {
			t.Run(fmt.Sprintf("%q/cut%d", sequence, cut), func(t *testing.T) {
				// Evict in the middle of a sequence, attach while its remaining
				// prefix ends the ring, then supply the final live bytes.
				var state terminalInputState
				defer state.Close()
				h := &holder{ring: NewRing(1), terminalInput: &state, logf: t.Logf}
				live, replay := replayTestTerminal(t), replayTestTerminal(t)
				first := "\x1b[>3u\x1b[?2004h\x1b[?2004sxx" + sequence[:cut]
				writeReplayTerminal(t, live, first)
				h.retainReplay([]byte(first))
				prelude, err := h.replay()
				if err != nil {
					t.Fatal(err)
				}
				writeReplayTerminal(t, replay, string(prelude))
				suffix := sequence[cut:] + "\x1b[>1u\x1b[<u"
				writeReplayTerminal(t, live, suffix)
				writeReplayTerminal(t, replay, suffix)
				checkReplayInput(t, live, replay)
			})
		}
	}
}

func TestTerminalInputReplayFiltersClipboardBeforeTracking(t *testing.T) {
	var state terminalInputState
	defer state.Close()
	h := &holder{ring: NewRing(1), terminalInput: &state, logf: t.Logf}
	h.retainReplay([]byte("\x1b[>3u\x1b]52;c;\x1b[>31u\x1b[?2004h\aafter"))
	prelude, err := h.replay()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(prelude, []byte("]52;")) {
		t.Fatal("clipboard control replayed")
	}
	vt := replayTestTerminal(t)
	writeReplayTerminal(t, vt, string(prelude))
	if vt.Get(8) != 3 || vt.Mode(2004) {
		t.Fatal("clipboard payload changed input state")
	}
}

func TestTerminalInputStateNoOverflowAndClose(t *testing.T) {
	var state terminalInputState
	state.Feed(nil)
	if p, err := state.Replay(); err != nil || p != nil || state.vt != nil {
		t.Fatalf("unneeded tracker initialized: %q, %v", p, err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	if p, err := state.Replay(); err != nil || p != nil {
		t.Fatalf("initialized but unoverflowed replay = %q, %v", p, err)
	}
	state.Close()
	state.Feed([]byte("closed"))
	if _, err := state.Replay(); err == nil {
		t.Fatal("closed tracker accepted replay")
	}
}

func TestTerminalInputReplayOversizedContinuation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var state terminalInputState
			defer state.Close()
			h := &holder{ring: NewRing(64), terminalInput: &state, logf: t.Logf}
			h.retainReplay([]byte("\x1b[>3u\x1bPq" + strings.Repeat("x", 40*1024)))
			const tail = "\x1b\\visible\x1b[>31u"
			if complete {
				h.retainReplay([]byte(tail))
			} else {
				if _, err := h.replay(); err == nil {
					t.Fatal("unfinished oversized DCS accepted")
				}
				h.retainReplay([]byte(tail))
			}
			// The terminator is in the ring, not yet in the evicted prefix.
			// Reattach must recover immediately without needing further output.
			for range 2 {
				snapshot, err := h.replay()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasSuffix(snapshot, []byte("visible\x1b[>31u")) {
					t.Fatal("ground-state tail discarded during recovery")
				}
				vt := replayTestTerminal(t)
				writeReplayTerminal(t, vt, string(snapshot))
				if vt.Get(8) != 31 {
					t.Fatal("tail keyboard push lost")
				}
				writeReplayTerminal(t, vt, "\x1b[<u")
				if vt.Get(8) != 3 {
					t.Fatal("prior keyboard stack lost")
				}
			}
		})
	}
}
