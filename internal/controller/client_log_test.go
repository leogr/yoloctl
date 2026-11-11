package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// fakeController answers the handshake, reads the request and sends frames.
func fakeController(t *testing.T, frames ...Frame) (net.Conn, <-chan Request) {
	t.Helper()
	client, server := net.Pipe()
	got := make(chan Request, 1)
	go func() {
		defer func() { _ = server.Close() }()
		wire := NewWire(server, server)
		if _, err := wire.Read(); err != nil {
			return
		}
		h := hello()
		if wire.Write(Frame{Type: "hello", Hello: &h}) != nil {
			return
		}
		f, err := wire.Read()
		if err != nil || f.Request == nil {
			return
		}
		got <- *f.Request
		go func() { _, _ = wire.Read() }() // eof/stdin frames
		for _, frame := range frames {
			if wire.Write(frame) != nil {
				return
			}
		}
	}()
	return client, got
}

func logFrame(t *testing.T, level slog.Level, msg string, attrs ...slog.Attr) Frame {
	t.Helper()
	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.AddAttrs(attrs...)
	raw, err := prettylog.MarshalRecord(r, prettylog.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return Frame{Type: "log", Data: raw}
}

type failingSink struct{}

func (failingSink) Handle(context.Context, slog.Record) error { return errors.New("terminal gone") }

func TestCallRendersLogFramesAndMarksOnlyAFinalFailure(t *testing.T) {
	step := []slog.Attr{slog.String(prettylog.KeyStep, "s.1"), slog.String(prettylog.KeyState, "failed")}
	cases := []struct {
		name     string
		frames   []Frame
		sink     LogSink
		rendered bool
	}{
		{"failed outcome", []Frame{logFrame(t, slog.LevelError, "Box dev failed", prettylog.Outcome(prettylog.OutcomeFailed))}, nil, true},
		{"unknown outcome", []Frame{logFrame(t, slog.LevelWarn, "outcome may be uncertain", prettylog.Outcome(prettylog.OutcomeUnknown))}, nil, true},
		{"a failed step is not an outcome", []Frame{logFrame(t, slog.LevelError, "Building image", step...)}, nil, false},
		{"an earlier ok never hides a later error", []Frame{logFrame(t, slog.LevelError, "Box dev failed", prettylog.Outcome(prettylog.OutcomeFailed)), logFrame(t, slog.LevelInfo, "Box dev ready", prettylog.Outcome(prettylog.OutcomeOK))}, nil, false},
		{"an unrecognised outcome is no evidence", []Frame{logFrame(t, slog.LevelError, "Box dev failed", slog.String(prettylog.KeyOutcome, "partial"))}, nil, false},
		{"a sink that failed did not render", []Frame{logFrame(t, slog.LevelError, "Box dev failed", prettylog.Outcome(prettylog.OutcomeFailed))}, failingSink{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := fakeController(t, append(tc.frames, Frame{Type: "exit", Status: 2, Error: "setup failed"})...)
			var stderr bytes.Buffer
			err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{Err: &stderr, Log: tc.sink})
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Status != 2 || exit.Error() != "setup failed" || OutcomeRendered(err) != tc.rendered {
				t.Fatalf("err=%v rendered=%v want %v", err, OutcomeRendered(err), tc.rendered)
			}
			if tc.sink == nil && stderr.Len() == 0 {
				t.Fatalf("records did not follow Err: %q", stderr.String())
			}
		})
	}
}

func TestCallLogFramesFollowErrAndTheRequestCarriesVerbosity(t *testing.T) {
	conn, got := fakeController(t, logFrame(t, slog.LevelWarn, "graceful shutdown failed", prettylog.Actor("host")), Frame{Type: "exit"})
	var stderr bytes.Buffer
	req := request("box.start")
	req.Log = LogDebug
	if err := Call(t.Context(), conn, hello(), req, ClientIO{Err: &stderr}); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.Log != LogDebug {
		t.Fatalf("request log verbosity %q", r.Log)
	}
	if stderr.String() != "warning: host: graceful shutdown failed\n" {
		t.Fatalf("%q", stderr.String())
	}
	// Completion and other silent callers pass io.Discard-like writers: no
	// record reaches the process stderr then.
	conn, _ = fakeController(t, logFrame(t, slog.LevelWarn, "hidden"), Frame{Type: "exit"})
	if err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{}); err != nil {
		t.Fatal(err)
	}
}

func TestCallRejectsAnInvalidLogRecord(t *testing.T) {
	conn, _ := fakeController(t, Frame{Type: "log", Data: []byte(`{"unknown":1}`)})
	err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{})
	var unknown *OutcomeUnknown
	if !errors.As(err, &unknown) {
		t.Fatal(err)
	}
}

func TestRequestLogVerbosityIsValidated(t *testing.T) {
	for value, ok := range map[string]bool{"": true, LogDebug: true, LogWarn: true, LogError: true, "info": false, "trace": false, "Debug": false} {
		req := request("box.start")
		req.Log = value
		if err := req.Validate(); (err == nil) != ok {
			t.Fatalf("%q: %v", value, err)
		}
	}
	for _, v := range []prettylog.Verbosity{prettylog.Errors, prettylog.Quiet, prettylog.Normal, prettylog.Trace} {
		if got, err := LogVerbosity(LogSetting(v)); err != nil || got != v {
			t.Fatal(v, got, err)
		}
	}
	if LogSetting(prettylog.Verbose) != LogDebug {
		t.Fatal("a verbosity between info and debug must ask the worker for debug")
	}
}

func TestCallExitCarriesTheUncertainReceiptState(t *testing.T) {
	for _, state := range []string{"", "uncertain", "command-uncertain"} {
		conn, _ := fakeController(t, Frame{Type: "exit", Status: 2, Error: "existing wording", State: state})
		err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{})
		var exit *ExitError
		if !errors.As(err, &exit) || exit.State != state || exit.Error() != "existing wording" || exit.Status != 2 {
			t.Fatalf("%q: %v", state, err)
		}
	}
	conn, _ := fakeController(t, Frame{Type: "exit", Status: 2, State: "complete"})
	var unknown *OutcomeUnknown
	if err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{}); !errors.As(err, &unknown) {
		t.Fatal(err)
	}
}

// A host rendering failure is reported once, never through the failed sink,
// and changes neither the result nor the later attempts.
func TestCallReportsLogRenderingFailures(t *testing.T) {
	frames := []Frame{logFrame(t, slog.LevelInfo, "first"), logFrame(t, slog.LevelInfo, "second"), {Type: "exit"}}
	conn, _ := fakeController(t, frames...)
	var reported []error
	calls := 0
	sink := sinkFunc(func(slog.Record) error { calls++; return errors.New("terminal gone") })
	err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{Log: sink, LogError: func(e error) { reported = append(reported, e) }})
	if err != nil || calls != 2 || len(reported) != 1 || reported[0].Error() != "terminal gone" {
		t.Fatal(err, calls, reported)
	}
	conn, _ = fakeController(t, frames...)
	var stderr bytes.Buffer
	if err := Call(t.Context(), conn, hello(), request("box.start"), ClientIO{Err: &stderr, Log: sink}); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "yoloctl: showing controller log records failed: terminal gone\n" {
		t.Fatalf("%q", stderr.String())
	}
}

type sinkFunc func(slog.Record) error

func (f sinkFunc) Handle(_ context.Context, r slog.Record) error { return f(r) }

// With no steps block (here: a plain writer at the default level, where
// detail lines are hidden), a failed step relayed from the controller is
// followed by its last output lines.
func TestCallExpandsAFailedStepInLineOutput(t *testing.T) {
	step := func(state string) []slog.Attr {
		return []slog.Attr{slog.String(prettylog.KeyStep, "s.1"), slog.String(prettylog.KeyState, state)}
	}
	detail := func(text string) Frame {
		return logFrame(t, prettylog.LevelDebug, text, slog.String(prettylog.KeyStep, "s.1"), slog.Bool(prettylog.KeyDetail, true))
	}
	conn, _ := fakeController(t,
		logFrame(t, slog.LevelInfo, "RUN make", step("running")...),
		detail("before failure"), detail("QA-MARKER-42"),
		logFrame(t, slog.LevelError, "RUN make", append(step("failed"), slog.String(prettylog.KeyErr, "exit code: 3"))...),
		Frame{Type: "exit", Status: 2, Error: "image preparation failed"})
	var stderr bytes.Buffer
	_ = Call(t.Context(), conn, hello(), request("box.start"), ClientIO{Err: &stderr})
	got := stderr.String()
	if !strings.Contains(got, "error: Last output of RUN make:\n  before failure\n  QA-MARKER-42") {
		t.Fatalf("%q", got)
	}
}
