package prettylog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 21, 46, 0, 120e6, time.UTC)

func fixed() time.Time { return t0 }

func newTest(o Options) (*Logger, *bytes.Buffer) {
	var b bytes.Buffer
	if o.Now == nil {
		o.Now = fixed
	}
	return New(&b, o), &b
}

// at writes a record with the fixed test time.
func at(l *Logger, level slog.Level, msg string, args ...any) {
	r := slog.NewRecord(t0, level, msg, 0)
	r.Add(args...)
	_ = l.Handle(context.Background(), r)
}

func TestPrettyLines(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Verbosity: Normal, ActorWidth: 20, Fields: []string{"box"}})
	s := l.Slog()
	s.Info("Image claude ready", Actor("builder"), Executor("buildkit"), Took(134*time.Second+120*time.Millisecond))
	s.Warn("Graceful shutdown failed; forcing VM stop", Actor("host"), Executor("limactl"))
	s.Log(context.Background(), LevelNotice, "Box dev restarted", "box", "dev", "hidden", 1)
	s.Debug("not shown at Normal")
	s.Error("Cannot read configuration", Err(errors.New("open x: denied\nsecond line")))
	l.Outcome(OutcomeUnknown, "connection ended before the result of request 4f2c; outcome may be uncertain: EOF", Actor("controller"))
	want := strings.Join([]string{
		"› builder · buildkit    Image claude ready  2m14.1s",
		"! host · limactl        Graceful shutdown failed; forcing VM stop",
		"• Box dev restarted  box=dev",
		"✗ Cannot read configuration",
		"  open x: denied",
		"  second line",
		"? controller            connection ended before the result of request 4f2c; outcome may be uncertain: EOF",
		""}, "\n")
	if got := b.String(); got != want {
		t.Fatalf("pretty lines:\n%s\nwant:\n%s", got, want)
	}
}

func TestPlainLines(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Normal, Time: TimeRFC3339, Fields: []string{"box"}})
	at(l, LevelWarn, "Graceful shutdown failed; forcing VM stop", Actor("host"), Executor("limactl"))
	at(l, LevelNotice, "Box dev restarted", Actor("controller"), "box", "dev")
	at(l, LevelInfo, "Image claude ready", Actor("builder"), Executor("buildkit"), Took(2*time.Minute))
	at(l, LevelError, "Cannot read configuration", Err(errors.New("denied")))
	want := strings.Join([]string{
		"2026-11-11T21:46:00.120Z warning: host/limactl: Graceful shutdown failed; forcing VM stop",
		"2026-11-11T21:46:00.120Z notice: controller: Box dev restarted box=dev",
		"2026-11-11T21:46:00.120Z builder/buildkit: Image claude ready (2m0s)",
		"2026-11-11T21:46:00.120Z error: Cannot read configuration: denied",
		""}, "\n")
	if got := b.String(); got != want {
		t.Fatalf("plain lines:\n%s\nwant:\n%s", got, want)
	}
}

func TestEscapingCoversKeysValuesAndMessage(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Verbose})
	l.Slog().Info("a\x1b[31mred\tx", "k\x1bey", "v\x07", slog.Group("g\x1b", "in", "y"))
	got := b.String()
	if strings.ContainsAny(got[:len(got)-1], "\x1b\x07\t") {
		t.Fatalf("controls leaked: %q", got)
	}
	want := "a\\x1b[31mred x k\\x1bey=\"v\\a\" g\\x1b.in=y\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestColourUsesPaletteAndResets(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Color: true, Palette: ANSIPalette, Verbosity: Normal})
	l.Slog().Warn("careful")
	want := "\x1b[1;38;5;3m!\x1b[0m \x1b[1;38;5;3mcareful\x1b[0m\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
	b.Reset()
	l.SetPalette(Palette{Warn: RGB(1, 2, 3)})
	l.Slog().Warn("x")
	if !strings.Contains(b.String(), "38;2;1;2;3") {
		t.Fatalf("palette not replaced: %q", b.String())
	}
}

func TestQuietShowsProblemsAndOutcomes(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Quiet})
	s := l.Slog()
	s.Info("progress")
	s.Log(context.Background(), LevelNotice, "notice")
	s.Warn("warn")
	l.Outcome(OutcomeOK, "done")
	st := l.StartStep("step")
	st.Fail(errors.New("boom"))
	want := "warning: warn\ndone\nerror: step: failed (0s): boom\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
	if k, ok := l.LastOutcome(); !ok || k != OutcomeOK {
		t.Fatalf("last outcome %v %v", k, ok)
	}
}

func TestDecideColor(t *testing.T) {
	cases := []struct {
		term   bool
		mode   ColorMode
		env    map[string]string
		format Format
		color  bool
	}{
		{true, ColorAuto, nil, FormatPretty, true},
		{true, ColorAuto, map[string]string{"NO_COLOR": "1"}, FormatPretty, false},
		{true, ColorAuto, map[string]string{"TERM": "dumb"}, FormatPretty, false},
		{true, ColorNever, nil, FormatPretty, false},
		{true, ColorAlways, map[string]string{"NO_COLOR": "1"}, FormatPretty, true},
		{false, ColorAuto, nil, FormatPlain, false},
		{false, ColorNever, nil, FormatPlain, false},
		{false, ColorAlways, nil, FormatPlain, true},
	}
	for i, c := range cases {
		getenv := func(k string) string { return c.env[k] }
		f, col := DecideColor(io.Discard, c.mode, getenv, func(io.Writer) bool { return c.term })
		if f != c.format || col != c.color {
			t.Errorf("case %d: got %v/%v want %v/%v", i, f, col, c.format, c.color)
		}
	}
}

func TestErrorsShowsOnlyErrorsAndUncertainResults(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Errors})
	s := l.Slog()
	s.Info("progress")
	s.Warn("warn")
	s.Error("broken")
	l.Outcome(OutcomeOK, "done")
	l.Outcome(OutcomeUnknown, "maybe")
	l.Outcome(OutcomeFailed, "failed")
	st := l.StartStep("step")
	st.Fail(errors.New("boom"))
	want := "error: broken\nuncertain: maybe\nerror: failed\nerror: step: failed (0s): boom\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
}
