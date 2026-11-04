package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"strings"
	"testing"
)

func captureTestSnapshot(t *testing.T, vt *Terminal) []byte {
	t.Helper()
	p, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func restoreTestSnapshot(t *testing.T, p []byte, replies *bytes.Buffer) *Terminal {
	t.Helper()
	vt, err := RestoreSnapshot(context.Background(), p, func(p []byte) {
		if replies != nil {
			_, _ = replies.Write(p)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vt.Close() })
	return vt
}

func assertSameTerminal(t *testing.T, want, got *Terminal) {
	t.Helper()
	a, err := want.Frame(false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := got.Frame(false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("frame differs:\nwant %+v\ngot  %+v", a, b)
	}
	for field := uint32(1); field <= 15; field++ {
		if a, b := want.Get(field), got.Get(field); a != b {
			t.Fatalf("scalar %d: want %d, got %d", field, a, b)
		}
	}
	if want.Title() != got.Title() || want.Holding() != got.Holding() {
		t.Fatalf("title/hold differs: %q/%v vs %q/%v", want.Title(), want.Holding(), got.Title(), got.Holding())
	}
	for _, mode := range []uint16{1, 6, 7, 25, 47, 1000, 1002, 1003, 1004, 1006, 1047, 1049, 2004, 2026, 2027} {
		if want.Mode(mode) != got.Mode(mode) {
			t.Fatalf("mode %d differs", mode)
		}
	}
	for _, history := range []bool{false, true} {
		n := want.Get(2)
		if history {
			n = want.Get(9)
		}
		a, err := want.Lines(history, false, 0, n)
		if err != nil {
			t.Fatal(err)
		}
		b, err := got.Lines(history, false, 0, n)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("rows differ (history %v): want %q, got %q", history, a, b)
		}
	}
	for _, key := range []Key{{Name: "arrow_up", Action: 1}, {Name: "enter", Mods: 1, Action: 1}, {Name: "key_a", Text: "a", Codepoint: 'a', Mods: 2, Action: 1}} {
		a, err := want.EncodeKey(key)
		if err != nil {
			t.Fatalf("source key %+v: %v", key, err)
		}
		b, err := got.EncodeKey(key)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("key %+v: want %q, got %q (%v)", key, a, b, err)
		}
	}
	m := Mouse{X: 3, Y: 2, Button: 1, Action: MousePress}
	aMouse, aErr := want.EncodeMouse(m)
	bMouse, bErr := got.EncodeMouse(m)
	if aErr != nil || bErr != nil || !bytes.Equal(aMouse, bMouse) {
		t.Fatalf("mouse differs: %q (%v) vs %q (%v)", aMouse, aErr, bMouse, bErr)
	}
}

func TestSnapshotStateAndContinuation(t *testing.T) {
	for _, tc := range []struct{ name, prefix, suffix string }{
		{"stable header", "\x1b[2J\x1b[HHEADER" + strings.Repeat("\x1b[4;1Hstatus", 200), "\x1b[1;1HZ"},
		{"graphemes and wrap", "1234567890123456789界👩‍💻\x1b[31me\u0301", "tail"},
		{"screens and saved cursors", "primary\x1b[31m\x1b7\x1b[?1049hALT\x1b[32m\x1b7\x1b[2;3Hnext", "\x1b8Z\x1b[?1049l\x1b8Q"},
		{"history eviction", strings.Repeat("scroll\r\n", 400), "last\r\n"},
		{"regions and tabs", "\x1b[2;4r\x1b[3g\x1b[2;5H\x1bH\x1b[2;1Hone", "\tT\nnext\nlast\nscroll"},
		{"saved modes and keyboard stacks", "\x1b[>1u\x1b[>3u\x1b[?2004h\x1b[?2004s\x1b[?2004l\x1b[?1003;1006h\x1b[?1049h\x1b[>31u", "\x1b[<u\x1b[?1049l\x1b[<u\x1b[?2004r"},
		{"palette and hyperlinks", "\x1b]4;1;#abcdef\a\x1b[31m\x1b]8;id=a;https://example.org\x1b\\LINK", "MORE\x1b]8;;\x1b\\\x1b]104;1\a"},
		{"partial CSI", "text\x1b[38;2;1;2;", "3mcolor"},
		{"partial OSC", "text\x1b]2;Ü partial", " title\aend"},
		{"partial UTF8", "text\xe7\x95", "\x8cend"},
		{"partial query", "text\x1b[6", "n"},
		{"synchronized output", "before\x1b[?2026hafter", "\x1b[?2026l"},
		{"reset defaults", "\x1b[?2027l\x1b[?12l\x1b[2 q", "\x1bc👩‍💻X"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wantReply, gotReply bytes.Buffer
			vt := newTestTerminal(t, 4, 20, 20, &wantReply)
			if err := vt.SetPaletteColor(1, color.RGBA{R: 10, G: 20, B: 30, A: 255}); err != nil {
				t.Fatal(err)
			}
			if _, err := vt.Write([]byte(tc.prefix)); err != nil {
				t.Fatal(err)
			}
			got := restoreTestSnapshot(t, captureTestSnapshot(t, vt), &gotReply)
			if gotReply.Len() != 0 || got.Bells() != 0 {
				t.Fatal("restore emitted a historical reply or bell")
			}
			assertSameTerminal(t, vt, got)
			// Re-capture before finishing a partial sequence: continuation
			// tracking must survive restoration, not just the first capture.
			got = restoreTestSnapshot(t, captureTestSnapshot(t, got), &gotReply)
			for _, suffix := range []string{tc.suffix, "\x1b[?1049h", "\x1b[?1049l", "\x1b[6n\x1b[?u\x1bP+q544e\x1b\\"} {
				wantReply.Reset()
				gotReply.Reset()
				if _, err := vt.Write([]byte(suffix)); err != nil {
					t.Fatal(err)
				}
				if _, err := got.Write([]byte(suffix)); err != nil {
					t.Fatal(err)
				}
				assertSameTerminal(t, vt, got)
				if !bytes.Equal(wantReply.Bytes(), gotReply.Bytes()) {
					t.Fatalf("live replies differ: %q vs %q", wantReply.Bytes(), gotReply.Bytes())
				}
			}
			if err := vt.Resize(5, 14); err != nil {
				t.Fatal(err)
			}
			if err := got.Resize(5, 14); err != nil {
				t.Fatal(err)
			}
			assertSameTerminal(t, vt, got)
		})
	}
}

func TestSnapshotEveryOutputCut(t *testing.T) {
	// A cut may divide UTF-8, a grapheme, parameters, OSC/DCS payloads, ST or
	// a terminal query. Compare future output/replies to uninterrupted parsing.
	stream := "A界👩‍💻e\u0301\x1b[38;2;1;2;3mB\x1b]2;títle\x1b\\\x1b]4;3;#abcdef\a" +
		"\x1bP+q544e\x1b\\\x1b[>3u\x1b[6n\x1b[?1049hALT\x1b[?1049l\aZ"
	for cut := 0; cut <= len(stream); cut++ {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			var wantReply, gotReply bytes.Buffer
			vt := newTestTerminal(t, 4, 20, 20, &wantReply)
			if _, err := vt.Write([]byte(stream[:cut])); err != nil {
				t.Fatal(err)
			}
			got := restoreTestSnapshot(t, captureTestSnapshot(t, vt), &gotReply)
			if gotReply.Len() != 0 || got.Bells() != 0 {
				t.Fatal("restore emitted effects")
			}
			wantReply.Reset()
			before := vt.Bells()
			for _, b := range []byte(stream[cut:]) {
				if _, err := vt.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
				if _, err := got.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			assertSameTerminal(t, vt, got)
			if !bytes.Equal(wantReply.Bytes(), gotReply.Bytes()) || got.Bells() != vt.Bells()-before {
				t.Fatalf("effects differ: %q/%d vs %q/%d", wantReply.Bytes(), vt.Bells()-before, gotReply.Bytes(), got.Bells())
			}
		})
	}
}

func TestSnapshotEffectsAndLocalState(t *testing.T) {
	vt := newTestTerminal(t, 3, 20, 20, nil)
	_, _ = vt.Write([]byte("\aline1\r\nline2\r\nline3\r\nline4\x1b[?2026h\x1b[6n"))
	if _, err := vt.Scroll(1, false); err != nil {
		t.Fatal(err)
	}
	if err := vt.Select(MousePress, 0, 0, false, 100, false); err != nil {
		t.Fatal(err)
	}
	if err := vt.Select(MouseMotion, 3, 1, false, 110, false); err != nil {
		t.Fatal(err)
	}
	var replies bytes.Buffer
	got := restoreTestSnapshot(t, captureTestSnapshot(t, vt), &replies)
	frame, err := got.Frame(false)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := got.SelectedText(1024)
	if err != nil || selected != "" || frame.Offset != 0 || got.Bells() != 0 || replies.Len() != 0 || !got.Holding() {
		t.Fatalf("unexpected local state: selection %q (%v), offset %d, bells %d, replies %q, hold %v", selected, err, frame.Offset, got.Bells(), replies.String(), got.Holding())
	}
	var holds []bool
	got.SetHoldHandler(func(v bool) { holds = append(holds, v) })
	_, _ = got.Write([]byte("\x1b[?2026l\a\x1b[6n\x1b[?2026h"))
	if got.Err() != nil || got.Bells() != 1 || !bytes.Contains(replies.Bytes(), []byte("R")) || !reflect.DeepEqual(holds, []bool{false, true}) {
		t.Fatalf("callbacks not restored: %v, bells %d, replies %q, holds %v", got.Err(), got.Bells(), replies.String(), holds)
	}
	_, _ = got.Scroll(1, false)
	_, _ = got.Write([]byte("\x1bc"))
	if got.anchor != 0 || !got.Mode(2027) {
		t.Fatal("reset callback/defaults not restored")
	}
}

func TestSnapshotRejectsInvalidAndLeavesSourceUsable(t *testing.T) {
	vt := newTestTerminal(t, 3, 20, 20, nil)
	_, _ = vt.Write([]byte("keep this\x1b[>3u\x1b]2;title\a"))
	original := captureTestSnapshot(t, vt)
	for cut := 0; cut < len(original); cut++ {
		got, err := RestoreSnapshot(context.Background(), original[:cut], nil)
		if got != nil {
			_ = got.Close()
		}
		if err == nil {
			t.Fatalf("accepted truncation at %d/%d", cut, len(original))
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"outer version", func(p []byte) []byte { p[7]++; return p }},
		{"core identity", func(p []byte) []byte { p[8] ^= 1; return p }},
		{"history policy", func(p []byte) []byte { p[snapshotHeaderSize-4]++; return p }},
		{"native version", func(p []byte) []byte { p[snapshotHeaderSize+8]++; return p }},
		{"record CRC", func(p []byte) []byte { p[snapshotHeaderSize+16] ^= 1; return p }},
		{"trailing bytes", func(p []byte) []byte { return append(p, 0) }},
		{"second snapshot", func(p []byte) []byte { return append(p, p[snapshotHeaderSize:]...) }},
		{"oversized record", func(p []byte) []byte { binary.LittleEndian.PutUint32(p[snapshotHeaderSize+12:], ^uint32(0)); return p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RestoreSnapshot(context.Background(), tc.mutate(bytes.Clone(original)), nil)
			if got != nil {
				_ = got.Close()
			}
			if err == nil {
				t.Fatal("accepted damaged snapshot")
			}
		})
	}
	if got := captureTestSnapshot(t, vt); !bytes.Equal(got, original) {
		t.Fatal("failed restores changed the source terminal")
	}
	if _, err := vt.Write([]byte("still live")); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotBoundsAndLifetime(t *testing.T) {
	vt := newTestTerminal(t, 3, 20, 20, nil)
	_, _ = vt.Write([]byte("\x1b]2;" + strings.Repeat("a", 40<<10)))
	if _, err := vt.Snapshot(); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("oversized continuation accepted: %v", err)
	}
	if _, err := vt.Write([]byte("\aafter")); err != nil {
		t.Fatal(err)
	}
	p := captureTestSnapshot(t, vt)
	ctx, cancel := context.WithCancel(context.Background())
	got, err := RestoreSnapshot(ctx, p, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { _ = got.Close() }()
	cancel()
	if _, err := got.Write([]byte("live after cancel")); err != nil {
		t.Fatalf("decode context leaked into live terminal: %v", err)
	}
	if _, err := RestoreSnapshot(ctx, p, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore: %v", err)
	}
	if _, err := RestoreSnapshot(context.Background(), make([]byte, MaxSnapshotBytes+1), nil); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("oversized restore: %v", err)
	}
}

func TestSnapshotLargerThanScratch(t *testing.T) {
	vt := newTestTerminal(t, 12, 160, 1000, nil)
	for line := range 500 {
		_, _ = fmt.Fprintf(vt, "%06d %s\r\n", line, strings.Repeat("xyz", 50))
	}
	p := captureTestSnapshot(t, vt)
	if len(p) <= scratchSize {
		t.Fatalf("test did not exercise allocated export: %d bytes", len(p))
	}
	got := restoreTestSnapshot(t, p, nil)
	assertSameTerminal(t, vt, got)
}

func FuzzRestoreSnapshot(f *testing.F) {
	vt, err := New(3, 20, 10, nil)
	if err != nil {
		f.Fatal(err)
	}
	_, _ = vt.Write([]byte("hello\x1b[>3u\x1b[?1049halt\x1b[6"))
	p, err := vt.Snapshot()
	_ = vt.Close()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(p[snapshotHeaderSize:])
	f.Add([]byte("GHOSTSNP\x01\x00"))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 4096 {
			t.Skip()
		}
		data := append(bytes.Clone(p[:snapshotHeaderSize]), payload...)
		got, err := RestoreSnapshot(t.Context(), data, nil)
		if err != nil {
			return
		}
		defer func() { _ = got.Close() }()
		if _, err := got.Frame(false); err != nil {
			t.Fatal(err)
		}
		if _, err := got.Write([]byte("n\x1bcstill live")); err != nil {
			t.Fatal(err)
		}
		if _, err := got.Snapshot(); err != nil {
			t.Fatal(err)
		}
	})
}
