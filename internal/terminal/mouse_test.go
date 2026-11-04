package terminal

import (
	"context"
	"testing"
)

// Independent byte expectations follow the xterm mouse protocol, including
// legacy +32/+33 fields, release button 3, and mode-specific filtering:
// https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h2-Mouse-Tracking
func TestMouseEncodingContracts(t *testing.T) {
	for _, tc := range []struct {
		name, modes string
		event       Mouse
		want        string
	}{
		{"disabled", "", Mouse{X: 4, Y: 2, Button: 1}, ""},
		{"legacy press", "\x1b[?1000h", Mouse{X: 4, Y: 2, Button: 1}, "\x1b[M %#"},
		{"legacy release", "\x1b[?1000h", Mouse{X: 4, Y: 2, Button: 1, Action: MouseRelease}, "\x1b[M#%#"},
		{"normal ignores drag", "\x1b[?1000h", Mouse{X: 4, Y: 2, Button: 1, Action: MouseMotion}, ""},
		{"X10 ignores modifiers", "\x1b[?9h", Mouse{X: 4, Y: 2, Button: 1, Mods: 7}, "\x1b[M %#"},
		{"X10 ignores release", "\x1b[?9h", Mouse{X: 4, Y: 2, Button: 1, Action: MouseRelease}, ""},
		{"legacy coordinate limit", "\x1b[?1000h", Mouse{X: 223, Y: 2, Button: 1}, ""},
		{"UTF8 coordinates", "\x1b[?1000h\x1b[?1005h", Mouse{X: 223, Y: 2, Button: 1}, "\x1b[M \u0100#"},
		{"UTF8 extra button", "\x1b[?1000h\x1b[?1005h", Mouse{X: 4, Y: 2, Button: 10}, "\x1b[M\u00a2%#"},
		{"urxvt release", "\x1b[?1000h\x1b[?1015h", Mouse{X: 223, Y: 2, Button: 1, Action: MouseRelease}, "\x1b[35;224;3M"},
		{"SGR extended button", "\x1b[?1000h\x1b[?1006h", Mouse{X: 4, Y: 2, Button: 8}, "\x1b[<128;5;3M"},
		{"SGR button 10", "\x1b[?1000h\x1b[?1006h", Mouse{X: 4, Y: 2, Button: 10}, "\x1b[<130;5;3M"},
		{"SGR button 11 release", "\x1b[?1000h\x1b[?1006h", Mouse{X: 4, Y: 2, Button: 11, Action: MouseRelease}, "\x1b[<131;5;3m"},
		{"legacy extra release", "\x1b[?1000h", Mouse{X: 4, Y: 2, Button: 11, Action: MouseRelease}, "\x1b[M#%#"},
		{"urxvt button 10", "\x1b[?1000h\x1b[?1015h", Mouse{X: 4, Y: 2, Button: 10}, "\x1b[162;5;3M"},
		{"X10 ignores extra button", "\x1b[?9h", Mouse{X: 4, Y: 2, Button: 10}, ""},
		{"SGR right release", "\x1b[?1000h\x1b[?1006h", Mouse{X: 4, Y: 2, Button: 2, Action: MouseRelease}, "\x1b[<2;5;3m"},
		{"drag ignores hover", "\x1b[?1002h\x1b[?1006h", Mouse{X: 4, Y: 2, Action: MouseMotion}, ""},
		{"all motion hover", "\x1b[?1003h\x1b[?1006h", Mouse{X: 4, Y: 2, Action: MouseMotion}, "\x1b[<35;5;3M"},
		{"last mode reset", "\x1b[?1002h\x1b[?1000h\x1b[?1000l\x1b[?1006h", Mouse{X: 4, Y: 2, Button: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vt := newTestTerminal(t, 10, 300, 0, nil)
			_, _ = vt.Write([]byte(tc.modes))
			got, err := vt.EncodeMouse(tc.event)
			if err != nil || string(got) != tc.want {
				t.Fatalf("mouse bytes = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestMousePixelsAreNotInvented(t *testing.T) {
	vt := newTestTerminal(t, 10, 20, 0, nil)
	_, _ = vt.Write([]byte("\x1b[?1000h\x1b[?1016h"))
	if p, err := vt.EncodeMouse(Mouse{X: 4, Y: 2, Button: 1}); err == nil || len(p) != 0 {
		t.Fatalf("cell coordinates presented as pixels: %q, %v", p, err)
	}
}

// XTSHIFTESCAPE is parsed native state, not a search through raw output. It
// must survive a managed snapshot as well as a compatibility replay prelude.
func TestMouseShiftCaptureState(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         int
	}{
		{"default", "", 0},
		{"capture", "\x1b[>1s", 2},
		{"selection", "\x1b[>1s\x1b[>0s", 1},
		{"omitted parameter", "\x1b[>1s\x1b[>s", 1},
		{"invalid ignored", "\x1b[>1s\x1b[>2s", 2},
		{"reset", "\x1b[>1s\x1bc", 0},
		{"ESC aborts OSC", "\x1b]0;\x1b[>1s\x07", 2},
		{"cancelled CSI", "\x1b[>1\x18s", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vt := newTestTerminal(t, 3, 20, 0, nil)
			for _, b := range []byte(tc.output) {
				_, _ = vt.Write([]byte{b})
			}
			if got := vt.Get(16); got != tc.want {
				t.Fatalf("native shift state = %d, want %d", got, tc.want)
			}
			data, err := vt.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreSnapshot(context.Background(), data, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restored.Close() }()
			if got := restored.Get(16); got != tc.want {
				t.Fatalf("snapshot shift state = %d, want %d", got, tc.want)
			}
			tracker, err := NewInputTracker()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tracker.Close() }()
			_, _ = tracker.Write([]byte(tc.output))
			prelude, err := tracker.InputReplay()
			if err != nil {
				t.Fatal(err)
			}
			replayed := newTestTerminal(t, 3, 20, 0, nil)
			// An earlier passthrough view may have left capture enabled.
			_, _ = replayed.Write([]byte("\x1b[>1s"))
			_, _ = replayed.Write(prelude)
			want := tc.want
			if want == 0 {
				want = 1 // VT has no operation to restore an unspecified preference.
			}
			if got := replayed.Get(16); got != want {
				t.Fatalf("replay shift state = %d, want %d", got, want)
			}
		})
	}
}
