package audit

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestStderrPrefixConsumesBeyondLimits(t *testing.T) {
	for _, raw := range []string{strings.Repeat("x", 256<<10), strings.Repeat("line\n", 100), "\xff\x1b[31mwarning\x00\nnext\u202e"} {
		var prefix stderrPrefix
		for start := 0; start < len(raw); start += 37 {
			part := raw[start:min(start+37, len(raw))]
			if n, err := prefix.Write([]byte(part)); n != len(part) || err != nil {
				t.Fatal("stderr was not fully consumed", n, err)
			}
		}
		if prefix.used > 4096 || prefix.lines > 8 {
			t.Fatal("unbounded prefix", prefix.used, prefix.lines)
		}
		text := Clean(prefix.summary())
		if len(text) > 1024 || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			t.Fatal("unsafe diagnostic", text)
		}
	}
	var prefix stderrPrefix
	_, _ = prefix.Write([]byte(strings.Repeat("first\n", 8) + "ninth line"))
	if !prefix.truncated || strings.Contains(prefix.summary(), "ninth") {
		t.Fatal("prefix crossed the line limit")
	}
}
