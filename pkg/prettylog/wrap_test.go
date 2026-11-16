package prettylog

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestPrettyTerminalWidthHangsAndResizes(t *testing.T) {
	width, queries := 30, 0
	l, b := newTest(Options{Format: FormatPretty, ActorWidth: 8, TerminalWidth: func() int { queries++; return width }})
	msg := "alpha beta gamma delta epsilon"
	at(l, LevelWarn, msg, Actor("host"))
	head := "! host" + strings.Repeat(" ", 6)
	want := head + "alpha beta gamma\n" + strings.Repeat(" ", 12) + "delta epsilon\n"
	if b.String() != want {
		t.Fatalf("wrapped %q want %q", b.String(), want)
	}
	b.Reset()
	width = 42
	at(l, LevelWarn, msg, Actor("host"))
	if got := b.String(); got != head+msg+"\n" {
		t.Fatalf("stale width: %q", got)
	}
	if queries != 2 {
		t.Fatalf("width queries=%d, want one per write", queries)
	}
}

func TestPrettyWrapPreservesUnicodeColorAndExplicitLines(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Color: true, Palette: ANSIPalette, ActorWidth: 8,
		TerminalWidth: func() int { return 28 }})
	msg := "界界 e\u0301lan 👩‍💻 warning\nnext paragraph"
	at(l, LevelError, msg, Actor("主机"), slog.String(KeyErr, "some lengthy failure explanation"))
	text := b.String()
	if !strings.Contains(text, "\x1b[") || !strings.Contains(text, "👩‍💻") || !strings.Contains(text, "e\u0301lan") || !utf8.ValidString(text) {
		t.Fatalf("lost color or graphemes: %q", text)
	}
	rows := strings.Split(strings.TrimSuffix(ansi.Strip(text), "\n"), "\n")
	for i, row := range rows {
		if ansi.StringWidth(row) > 28 {
			t.Fatalf("row exceeds terminal cells: %q", row)
		}
		if i > 0 && !strings.HasPrefix(row, strings.Repeat(" ", 12)) {
			t.Fatalf("unaligned row: %q", row)
		}
	}
	if got := strings.Join(strings.Fields(strings.Join(rows, " ")), " "); got != "✗ 主机 界界 e\u0301lan 👩‍💻 warning next paragraph some lengthy failure explanation" {
		t.Fatalf("changed text: %q", got)
	}
}

func TestPrettyWidthUnavailableOrTooNarrowKeepsBytes(t *testing.T) {
	for _, width := range []int{0, -1, 8, 12} {
		base, want := newTest(Options{Format: FormatPretty, ActorWidth: 8})
		l, got := newTest(Options{Format: FormatPretty, ActorWidth: 8, TerminalWidth: func() int { return width }})
		for _, logger := range []*Logger{base, l} {
			at(logger, LevelWarn, "alpha beta gamma\nnext line", Actor("host"))
		}
		if got.String() != want.String() {
			t.Fatalf("width%d changed bytes: %q", width, got.String())
		}
	}
}

func TestPrettyLongTokenKeepsGraphemes(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, TerminalWidth: func() int { return 8 }})
	msg := strings.Repeat("👩‍💻", 7) + strings.Repeat("e\u0301", 7)
	at(l, LevelWarn, msg)
	rows := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	var text strings.Builder
	for _, row := range rows {
		if ansi.StringWidth(row) > 8 {
			t.Fatalf("long token row: %q", row)
		}
		text.WriteString(row[2:]) // glyph+space first, two-space continuation thereafter
	}
	if text.String() != msg {
		t.Fatalf("split or dropped a grapheme: %q", text.String())
	}
}

func TestTerminalWidthDoesNotChangePlainOrWire(t *testing.T) {
	for _, format := range []Format{FormatPlain, FormatWire} {
		var want, got bytes.Buffer
		a := New(&want, Options{Format: format})
		b := New(&got, Options{Format: format, TerminalWidth: func() int { t.Fatal("non-pretty format queried terminal"); return 1 }})
		for _, logger := range []*Logger{a, b} {
			at(logger, LevelWarn, "alpha beta gamma\nnext line", Actor("host"))
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("format%d changed bytes", format)
		}
	}
}

func TestPrettyWrapsDetailAndRepeatAtTheirColumn(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Verbosity: Trace, TerminalWidth: func() int { return 12 }})
	at(l, LevelDebug, "alpha beta gamma", KeyStep, "step", KeyDetail, true)
	if got := b.String(); got != "  alpha beta\n  gamma\n" {
		t.Fatalf("detail: %q", got)
	}
	b.Reset()
	text := l.summary(&foldRun{n: 2, last: t0, col: 2})
	for _, row := range strings.Split(text, "\n") {
		if ansi.StringWidth(row) > 12 || !strings.HasPrefix(row, "  ") {
			t.Fatalf("summary: %q", text)
		}
	}
}
