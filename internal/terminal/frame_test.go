package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func frameText(f Frame) string {
	var lines []string
	for _, line := range f.Lines {
		lines = append(lines, strings.TrimRight(ansi.Strip(line), " "))
	}
	return strings.Join(lines, "|")
}

func TestFrameHistoryAnchor(t *testing.T) {
	vt := newTestTerminal(t, 2, 12, 4, nil)
	_, _ = vt.Write([]byte("A\r\nB\r\nC\r\nD"))
	if off, err := vt.Scroll(1, false); err != nil || off != 1 {
		t.Fatalf("scroll = %d, %v", off, err)
	}
	for _, more := range []string{"", "\r\nE", "\r\nF"} {
		_, _ = vt.Write([]byte(more))
		f, err := vt.Frame(false)
		if err != nil || frameText(f) != "B|C" || f.Cursor.Visible {
			t.Fatalf("anchored frame = %q, %+v, %v", frameText(f), f.Cursor, err)
		}
	}
	_, _ = vt.Write([]byte("\r\nG\r\nH")) // B is now evicted: oldest surviving is C.
	f, err := vt.Frame(false)
	if err != nil || frameText(f) != "C|D" {
		history, _ := vt.Lines(true, true, 0, vt.Get(9))
		t.Fatalf("evicted anchor = %q, history=%q frame=%+v, %v", frameText(f), history, f, err)
	}
	vt.ScrollToBottom()
	f, err = vt.Frame(false)
	if err != nil || frameText(f) != "G|H" || f.Offset != 0 {
		t.Fatalf("live frame = %q, offset %d, %v", frameText(f), f.Offset, err)
	}
}

func TestFrameAnchorReflowAndReset(t *testing.T) {
	vt := newTestTerminal(t, 2, 8, 10, nil)
	_, _ = vt.Write([]byte("abcdefgh\r\nijklmnop\r\nqrstuvwx\r\nlast"))
	_, _ = vt.Scroll(1, false)
	if err := vt.Resize(2, 4); err != nil {
		t.Fatal(err)
	}
	f, err := vt.Frame(false)
	if err != nil || frameText(f) != "ijkl|mnop" {
		t.Fatalf("reflowed anchor = %q, %v", frameText(f), err)
	}
	_, _ = vt.Write([]byte("\x1bc1\r\n2\r\n3\r\n4"))
	f, err = vt.Frame(false)
	if err != nil || f.Offset != 0 || frameText(f) != "3|4" {
		t.Fatalf("reset reused old anchor: %q, %d, %v", frameText(f), f.Offset, err)
	}
}

func TestFrameTrimAndAlternateScreen(t *testing.T) {
	vt := newTestTerminal(t, 4, 10, 10, nil)
	_, _ = vt.Write([]byte("short\r\n"))
	f, err := vt.Frame(true)
	if err != nil || len(f.Lines) != 1 || f.ContentRows != 1 {
		t.Fatalf("trimmed frame = %+v, %v", f, err)
	}
	_, _ = vt.Write([]byte("2\r\n3\r\n4\r\n5\r\n6"))
	_, _ = vt.Scroll(1, true)
	_, _ = vt.Write([]byte("\x1b[?1049h\x1b[HALT"))
	f, err = vt.Frame(false)
	if err != nil || f.Offset != 0 || !strings.HasPrefix(frameText(f), "ALT|") {
		t.Fatalf("alternate screen = %q, %d, %v", frameText(f), f.Offset, err)
	}
}

func TestFrameCursorDefaultsAndExplicitStyle(t *testing.T) {
	vt := newTestTerminal(t, 2, 10, 10, nil)
	for _, tc := range []struct {
		input string
		shape int
		blink bool
	}{{"", 1, true}, {"\x1b[6 q", 0, false}, {"\x1b[3 q", 2, true}, {"\x1bc", 1, true}} {
		_, _ = vt.Write([]byte(tc.input))
		f, err := vt.Frame(false)
		if err != nil || f.Cursor.Shape != tc.shape || f.Cursor.Blink != tc.blink {
			t.Fatalf("cursor after %q = %+v, %v", tc.input, f.Cursor, err)
		}
	}
}
