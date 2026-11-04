package terminal_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

func TestBellReplayFilterMatchesNativeParser(t *testing.T) {
	for _, input := range []string{
		"before\aafter\a", "\x1b]2;title\a\a", "\x1b]2;title\x1b\\\a",
		"\x1b[\a31mred", "\x1b\a[32mgreen", "\x1bP$q\a\x1b\\\a",
		"\x1b_ignore\a\x1b\\\a", "\x1b^ignore\a\x1b\\\a",
		"\x1bXignore\a\x1b\\\a", "\x1b]2;canceled\x18\a",
		"❯\x1b]2;Ü\a界\a", "\xc3\aX", "\xc3\a\xbc", "\x9d2;c1\a\a",
		"\x1b]2;" + strings.Repeat("x", 100000) + "\a\a",
	} {
		for _, chunk := range []int{1, 2, 3, 17, len(input)} {
			f, err := terminal.NewInputTracker()
			if err != nil {
				t.Fatal(err)
			}
			var filtered []byte
			for rest := []byte(input); len(rest) > 0; {
				n := min(chunk, len(rest))
				part, err := f.FilterBells(rest[:n])
				if err != nil {
					t.Fatal(err)
				}
				filtered = append(filtered, part...)
				rest = rest[n:]
			}
			_ = f.Close()
			before, beforeReplies := bellProbe(t, []byte(input))
			after, afterReplies := bellProbe(t, filtered)
			if after.Bells() != 0 {
				t.Fatalf("chunk=%d filtered transcript still rings (%q -> %q)", chunk, input, filtered)
			}
			a, _ := before.Lines(false, true, 0, 2)
			b, _ := after.Lines(false, true, 0, 2)
			if strings.Join(a, "\n") != strings.Join(b, "\n") || before.Title() != after.Title() || beforeReplies.String() != afterReplies.String() {
				t.Fatalf("chunk=%d filter changed terminal state for %q", chunk, input)
			}
			if len(filtered) != len(input) || int(before.Bells()) != bytes.Count(filtered, []byte{0}) {
				t.Fatalf("wrong number of neutralized bytes for %d native bells in %q", before.Bells(), input)
			}
			_ = before.Close()
			_ = after.Close()
		}
	}
}

func bellProbe(t *testing.T, p []byte) (*terminal.Terminal, *bytes.Buffer) {
	t.Helper()
	var replies bytes.Buffer
	vt, err := terminal.New(2, 40, 0, func(p []byte) { _, _ = replies.Write(p) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vt.Write(p); err != nil {
		t.Fatal(err)
	}
	return vt, &replies
}
