package terminal

import (
	"strings"
	"testing"
)

func selectionTerminal(t *testing.T, rows, cols int, text string) *Terminal {
	t.Helper()
	v, err := New(rows, cols, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	return v
}

func selectAt(t *testing.T, v *Terminal, action MouseAction, x, y int, now uint64) {
	t.Helper()
	if err := v.Select(action, x, y, false, now, false); err != nil {
		t.Fatal(err)
	}
}

func selectedText(t *testing.T, v *Terminal, want string) {
	t.Helper()
	got, err := v.SelectedText(1 << 20)
	if err != nil || got != want {
		t.Fatalf("selection = %q, %v; want %q", got, err, want)
	}
}

func TestSelectionNativeText(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		cols, x, y       int
	}{
		{"cell boundaries", "hello world", "hello", 20, 5, 0},
		{"wide and combining", "界e\u0301👩‍💻 end", "界e\u0301👩‍💻", 20, 5, 0},
		{"soft wrap", "abcdefghij", "abcdefghij", 5, 5, 1},
		{"hard line", "abc\r\ndef", "abc\ndef", 10, 3, 1},
		{"blank line", "abc\r\n\r\ndef", "abc\n\ndef", 10, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := selectionTerminal(t, 4, tc.cols, tc.text)
			selectAt(t, v, MousePress, 0, 0, 0)
			selectAt(t, v, MouseMotion, tc.x, tc.y, 1)
			selectAt(t, v, MouseRelease, tc.x, tc.y, 2)
			selectedText(t, v, tc.want)
			if _, err := v.SelectedText(2); err == nil {
				t.Fatal("oversized selection silently truncated")
			}
			v.ClearSelection()
			selectedText(t, v, "")
		})
	}
}

func TestSelectionWordsLinesHistoryAndReflow(t *testing.T) {
	v := selectionTerminal(t, 3, 20, "one two three\r\nlast")
	selectAt(t, v, MousePress, 5, 0, 0)
	selectAt(t, v, MouseRelease, 5, 0, 10)
	selectAt(t, v, MousePress, 5, 0, 100_000_000)
	selectedText(t, v, "two")
	selectAt(t, v, MouseRelease, 5, 0, 110_000_000)
	selectAt(t, v, MousePress, 5, 0, 200_000_000)
	selectedText(t, v, "one two three")
	if err := v.Resize(3, 8); err != nil {
		t.Fatal(err)
	}
	selectedText(t, v, "one two three")
	_, _ = v.Write([]byte(strings.Repeat("\r\nmore", 6)))
	selectedText(t, v, "one two three")
	_, _ = v.Scroll(100, false)
	f, _ := v.Frame(false)
	if len(f.Selection) == 0 || f.Selection[0].End == 0 {
		t.Fatal("selection lost from historical frame")
	}
	v.Reset()
	selectedText(t, v, "")
	selectAt(t, v, MousePress, 0, 0, 1_000_000_000)
	_, _ = v.Write([]byte("\x1b[?1049hnew screen"))
	selectAt(t, v, MouseMotion, 5, 0, 1_100_000_000)
	selectedText(t, v, "") // old gesture never selects an unrelated screen
}

func TestSelectionBackwardRectangleAndEviction(t *testing.T) {
	v := selectionTerminal(t, 3, 10, "abcdef\r\nghijkl")
	selectAt(t, v, MousePress, 5, 0, 0)
	selectAt(t, v, MouseMotion, 1, 0, 1)
	selectedText(t, v, "bcde")
	v.ClearSelection()
	selectAt(t, v, MousePress, 1, 0, 0)
	if err := v.Select(MouseMotion, 4, 1, true, 1, false); err != nil {
		t.Fatal(err)
	}
	selectedText(t, v, "bcd\nhij")
	// Eviction may clamp or clear the native selection, but must not retain
	// a stale pointer or reproduce text that is no longer present.
	// Native page allocation may retain more rows than the visible Go cap.
	// First prove that the native history actually evicted early rows.
	evicted := false
	for produced := 256; produced <= 65536; produced += 256 {
		_, _ = v.Write([]byte(strings.Repeat("\r\nnew", 256)))
		if int(v.call("yolo_vt_get", v.ptr, 9)) < produced/2 {
			evicted = true
			break
		}
	}
	if !evicted {
		t.Fatal("fixture did not exhaust native history")
	}
	text, err := v.SelectedText(1 << 20)
	if err != nil || strings.Contains(text, "bcd") || strings.Contains(text, "hij") {
		t.Fatalf("evicted selection = %q, %v", text, err)
	}
	if _, err := v.Frame(false); err != nil {
		t.Fatal(err)
	}
}
