package terminal

import (
	"bytes"
	"strings"
	"testing"
)

// dirty leaves as much state behind as a command can: modes, mouse and
// keyboard reporting, pen, hyperlink, charset, cursor shape and visibility,
// scrolling region, origin mode, a saved cursor, palette and the alternate
// screen.
const dirty = "\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[?1004h\x1b[4h\x1b[>5u\x1b[1;31;4m" +
	"\x1b]8;;https://example.org\x1b\\\x1b(0\x1b[5 q\x1b[?25l\x1b]4;1;rgb:12/34/56\x1b\\" +
	"\x1b[2;3r\x1b[?6h\x1b7\x1b[?1049halt screen text"

// probe queries modes and keyboard flags and prints text whose rendering shows
// pen, hyperlink and charset state.
const probe = "\x1b[?2004$p\x1b[?1000$p\x1b[?1004$p\x1b[?1049$p\x1b[?6$p\x1b[?25$p\x1b[4$p\x1b[?u\x1b[c" +
	"probe qx"

// Running a new command after ResetKeepHistory must behave like a fresh
// terminal, whatever the previous command left unfinished, while the earlier
// rows stay.
func TestResetKeepHistoryMatchesFreshTerminal(t *testing.T) {
	for _, unfinished := range []string{
		"", "\x1b]0;unterminated title", "\x1b[3", "\x1bP1$q", "\x1b_Gf=100;", "\xe2\x94", "\x1b]52;c;",
	} {
		var freshOut, reusedOut bytes.Buffer
		fresh := newTestTerminal(t, 6, 30, 50, &freshOut)
		reused := newTestTerminal(t, 6, 30, 50, &reusedOut)
		_, _ = reused.Write([]byte("first command\r\nsecond line"))
		_, _ = reused.Write([]byte(dirty + unfinished))
		reused.ResetKeepHistory()
		reusedOut.Reset()
		_, _ = fresh.Write([]byte(probe))
		_, _ = reused.Write([]byte(probe))
		if reusedOut.String() != freshOut.String() {
			t.Fatalf("unfinished %q: replies %q, fresh %q", unfinished, reusedOut.String(), freshOut.String())
		}
		for _, key := range []uint32{1, 3, 5, 6, 7, 8, 10, 11, 12, 13, 14, 15, 16} {
			if reused.Get(key) != fresh.Get(key) {
				t.Fatalf("unfinished %q: state %d = %d, fresh %d", unfinished, key, reused.Get(key), fresh.Get(key))
			}
		}
		if reused.Title() != fresh.Title() {
			t.Fatalf("unfinished %q: title %q", unfinished, reused.Title())
		}
		ff, _ := fresh.Frame(false)
		rf, _ := reused.Frame(false)
		probeLine := func(f Frame) string {
			for _, l := range f.Lines {
				if strings.Contains(l, "probe") {
					return l
				}
			}
			return ""
		}
		if got, want := probeLine(rf), probeLine(ff); got == "" || got != want {
			t.Fatalf("unfinished %q: probe rendered %q, fresh %q", unfinished, got, want)
		}
		if got := frameText(rf); !strings.HasPrefix(got, "first command|second line|probe qx") || strings.Contains(got, "alt screen") {
			t.Fatalf("unfinished %q: rows %q", unfinished, got)
		}
	}
}

func TestResetKeepHistoryKeepsRowsAndContinuesBelowThem(t *testing.T) {
	vt := newTestTerminal(t, 3, 10, 50, nil)
	// Scrollback, then the cursor moved above the last text, mid-line.
	_, _ = vt.Write([]byte("one\r\ntwo\r\nthree\r\nfour\r\nfive\x1b[1;2H"))
	history := vt.Get(9)
	vt.ResetKeepHistory()
	// "five" is on the bottom row, so one row scrolls into history.
	if vt.Get(9) != history+1 {
		t.Fatalf("history rows %d, were %d", vt.Get(9), history)
	}
	_, _ = vt.Write([]byte("next"))
	f, _ := vt.Frame(false)
	if got := frameText(f); got != "four|five|next" {
		t.Fatalf("rows %q", got)
	}
	// Bottom row full of text: one row scrolls.
	vt.ResetKeepHistory()
	_, _ = vt.Write([]byte("after"))
	f, _ = vt.Frame(false)
	if got := frameText(f); got != "five|next|after" {
		t.Fatalf("rows %q", got)
	}
	// History is still selectable and copyable.
	if _, err := vt.Scroll(100, false); err != nil {
		t.Fatal(err)
	}
	if err := vt.Select(MousePress, 0, 0, false, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := vt.Select(MouseMotion, 3, 1, false, 1, false); err != nil {
		t.Fatal(err)
	}
	if text, err := vt.SelectedText(1 << 20); err != nil || text != "one\ntwo" {
		t.Fatalf("history copy %q, %v", text, err)
	}
	// Resizing after the reset keeps working.
	if err := vt.Resize(4, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := vt.Frame(false); err != nil {
		t.Fatal(err)
	}
	// An empty screen starts at the top.
	empty := newTestTerminal(t, 3, 10, 50, nil)
	empty.ResetKeepHistory()
	if empty.Get(3) != 0 || empty.Get(4) != 0 {
		t.Fatalf("cursor %d,%d", empty.Get(3), empty.Get(4))
	}
}

// A drag in progress when the command ends cannot resume across the reset,
// whichever screen it started on (review F1).
func TestResetKeepHistoryEndsSelectionGesture(t *testing.T) {
	for _, alt := range []bool{false, true} {
		vt := newTestTerminal(t, 4, 20, 50, nil)
		_, _ = vt.Write([]byte("old content\r\nsecond line"))
		if alt {
			_, _ = vt.Write([]byte("\x1b[?1049halt content"))
		}
		if err := vt.Select(MousePress, 0, 0, false, 0, false); err != nil {
			t.Fatal(err)
		}
		if err := vt.Select(MouseMotion, 4, 0, false, 1, false); err != nil {
			t.Fatal(err)
		}
		vt.ResetKeepHistory()
		_, _ = vt.Write([]byte("new command output"))
		if err := vt.Select(MouseMotion, 6, 2, false, 2, false); err != nil {
			t.Fatal(err)
		}
		if text, err := vt.SelectedText(1 << 20); err != nil || text != "" {
			t.Fatalf("alt=%v: old gesture resumed: %q, %v", alt, text, err)
		}
		// A new gesture still selects retained and new rows.
		_ = vt.Select(MousePress, 0, 0, false, 1_000_000_000, false)
		_ = vt.Select(MouseMotion, 3, 2, false, 1_000_000_001, false)
		if text, _ := vt.SelectedText(1 << 20); !strings.HasPrefix(text, "old content") || !strings.HasSuffix(text, "new") {
			t.Fatalf("alt=%v: new selection %q", alt, text)
		}
	}
}

// New output after the reset is output, whatever OSC 133 prompt or input
// state the previous command left; retained rows keep theirs (review F2).
func TestResetKeepHistoryRestoresOutputSemanticContent(t *testing.T) {
	for name, prev := range map[string]string{
		"prompt":          "\x1b]133;A\x07$ ",
		"input":           "\x1b]133;A\x07$ \x1b]133;B\x07typed",
		"input clear eol": "\x1b]133;A\x07$ \x1b]133;I\x07typed",
	} {
		for _, scroll := range []bool{false, true} {
			rows := 6
			if scroll {
				rows = 1 // the prompt is on the bottom row: the reset scrolls
			}
			vt := newTestTerminal(t, rows, 30, 50, nil)
			_, _ = vt.Write([]byte(prev))
			if vt.Get(19) == 0 {
				t.Fatalf("%s: fixture left output state", name)
			}
			vt.ResetKeepHistory()
			if got := vt.Get(19); got != 0 {
				t.Fatalf("%s scroll=%v: cursor semantic content %d after reset", name, scroll, got)
			}
			_, _ = vt.Write([]byte("out"))
			if got := vt.Get(20); got != 0 {
				t.Fatalf("%s scroll=%v: new cell semantic content %d", name, scroll, got)
			}
		}
	}
	// Control: without the reset the new cells inherit the input state.
	vt := newTestTerminal(t, 6, 30, 50, nil)
	_, _ = vt.Write([]byte("\x1b]133;A\x07$ \x1b]133;B\x07typed\r\nout"))
	if vt.Get(20) == 0 {
		t.Fatal("control: input state did not reach new cells; the regression would prove nothing")
	}
}
