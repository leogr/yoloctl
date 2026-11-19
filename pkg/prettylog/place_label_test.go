package prettylog

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func placeOf(actor string) (string, string) {
	switch actor {
	case "host":
		return "host", "yoloctl"
	case "controller", "builder":
		return "space", actor
	}
	return "", actor
}

// A Label hook turns the root label into place▸name (plain place>name), and
// the executor leaves the label to prefix the message.
func TestPlaceLabelShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		emit   func(*slog.Logger)
		want   string
	}{
		{"pretty actor only", FormatPretty, func(s *slog.Logger) { s.Info("Starting VM", Actor("host")) },
			"› host▸yoloctl        Starting VM\n"},
		{"pretty tool", FormatPretty, func(s *slog.Logger) {
			s.Info("Image ready", Actor("builder"), Executor("buildkit"), Took(42200*time.Millisecond))
		}, "› space▸builder       buildkit: Image ready  42.2s\n"},
		{"pretty longest label", FormatPretty, func(s *slog.Logger) { s.Info("Ready", Actor("controller")) },
			"› space▸controller    Ready\n"},
		{"pretty subject", FormatPretty, func(s *slog.Logger) {
			s.Log(context.Background(), LevelNotice, "using space:", Actor("host"), Subject("falco"))
		}, "• host▸yoloctl        using space: falco\n"},
		{"plain tool", FormatPlain, func(s *slog.Logger) {
			s.Info("Image ready", Actor("builder"), Executor("buildkit"), Took(42200*time.Millisecond))
		}, "space>builder: buildkit: Image ready (42.2s)\n"},
		{"plain error tool", FormatPlain, func(s *slog.Logger) {
			s.Error("Starting controller", Actor("controller"), Executor("openshell"), Err(errors.New("exit status 1")))
		}, "error: space>controller: openshell: Starting controller: exit status 1\n"},
		{"plain subject", FormatPlain, func(s *slog.Logger) { s.Info("using space:", Actor("host"), Subject("falco")) },
			"host>yoloctl: using space: falco\n"},
		{"unknown actor keeps today's label", FormatPlain, func(s *slog.Logger) {
			s.Info("Something", Actor("other"), Executor("tool"))
		}, "other/tool: Something\n"},
		{"unknown actor pretty", FormatPretty, func(s *slog.Logger) {
			s.Info("Something", Actor("other"), Executor("tool"))
		}, "› other · tool        Something\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, b := newTest(Options{Format: tc.format, Verbosity: Normal, ActorWidth: 18, Label: placeOf})
			tc.emit(l.Slog())
			_ = l.Close()
			if b.String() != tc.want {
				t.Fatalf("got  %q\nwant %q", b.String(), tc.want)
			}
		})
	}
}

// Without a hook the label is exactly today's actor[ · executor].
func TestNoLabelHookKeepsActorExecutor(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Verbosity: Normal, ActorWidth: 20})
	l.Slog().Info("Image ready", Actor("builder"), Executor("buildkit"))
	_ = l.Close()
	if b.String() != "› builder · buildkit    Image ready\n" {
		t.Fatalf("%q", b.String())
	}
}

// Place, name, tool and subject are escaped like the message.
func TestPlaceLabelEscapes(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Normal, Label: func(string) (string, string) { return "sp\x1bace", "na\nme" }})
	l.Slog().Info("m", Actor("x"), Executor("to\x1bol"), Subject("su\x1bb"))
	_ = l.Close()
	if strings.ContainsRune(b.String(), '\x1b') || strings.Count(b.String(), "\n") != 1 {
		t.Fatalf("unescaped: %q", b.String())
	}
}

// A wrapped message keeps the hanging indent at the message column after the
// tool prefix moved into it.
func TestPlaceLabelWrapIndent(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Verbosity: Normal, ActorWidth: 18, Label: placeOf,
		TerminalWidth: func() int { return 40 }})
	l.Slog().Info("one two three four five six seven eight", Actor("builder"), Executor("buildkit"))
	_ = l.Close()
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], strings.Repeat(" ", 22)) || strings.HasPrefix(lines[1], strings.Repeat(" ", 23)) {
		t.Fatalf("indent: %q", b.String())
	}
}

// HideField removes only the fields it names; at Verbose all others remain.
func TestHideField(t *testing.T) {
	hide := func(key, value string) bool { return key == "space" && value == "falco" }
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Normal, Fields: []string{"space", "box"}, HideField: hide})
	s := l.Slog()
	s.Info("a", "space", "falco", "box", "b")
	s.Info("c", "space", "other")
	_ = l.Close()
	if b.String() != "a box=b\nc space=other\n" {
		t.Fatalf("%q", b.String())
	}
}

// An ordinary "subject" field is still a field: not shown unless listed, and
// carried on the wire without the structural length bound.
func TestOrdinarySubjectFieldUnchanged(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Normal})
	l.Slog().Info("finished", "subject", "build")
	_ = l.Close()
	if b.String() != "finished\n" {
		t.Fatalf("%q", b.String())
	}
	r := slog.NewRecord(t0, slog.LevelInfo, "m", 0)
	r.AddAttrs(slog.String("subject", strings.Repeat("x", 257)))
	if _, err := MarshalRecord(r, Limits{}); err != nil {
		t.Fatalf("ordinary subject field rejected: %v", err)
	}
}
