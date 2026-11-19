package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
)

func restartPresentationFixture(t *testing.T, interactive bool, verbosity prettylog.Verbosity) (*prettylog.Logger, *stepview.View, *bytes.Buffer) {
	t.Helper()
	restoreInitial := logging.SetLogger(logging.Logger())
	logging.InitWorker(io.Discard, verbosity)
	out := &bytes.Buffer{}
	l := prettylog.New(out, prettylog.Options{Format: prettylog.FormatPretty, Verbosity: verbosity, ActorWidth: 20})
	restoreLogger := logging.SetLogger(l)
	var view *stepview.View
	if interactive {
		view = stepview.Install(l, out, stepview.Options{ActorWidth: 20, Interval: time.Hour, Size: func() (int, int) { return 160, 50 }})
	}
	t.Cleanup(func() { view.Close(); _ = l.Close(); restoreLogger(); restoreInitial() })
	return l, view, out
}

func TestRestartOutputKeepsBothStreamsAndTypedLogsInTheirOwners(t *testing.T) {
	l, _, shown := restartPresentationFixture(t, true, prettylog.Normal)
	root := l.StartStep("Starting space work", prettylog.Actor(logging.Host))
	ctx := context.WithValue(t.Context(), cliStepContextKey{}, root)
	// Retained genuine npm maintenance stderr, not injected live output or a feature pin.
	banner, err := os.ReadFile("testdata/space-start-npm-notice.txt")
	if err != nil {
		t.Fatal(err)
	}
	var rawOut, rawErr bytes.Buffer
	var returned error
	got := restartSessionOutput(ctx, "one", controller.ClientIO{Out: &rawOut, Err: &rawErr}, func(streams controller.ClientIO) error {
		client, server := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
		hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Generation: "fixture", Space: "work"}
		record := slog.NewRecord(time.Now(), prettylog.LevelNotice, "typed worker notice", 0)
		data, err := prettylog.MarshalRecord(record, prettylog.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		frames := []controller.Frame{{Type: "stdout", Data: []byte("changed 2")}, {Type: "stderr", Data: banner[:17]}, {Type: "stderr", Data: banner[17:]}, {Type: "log", Data: data}, {Type: "stdout", Data: []byte(" packages in 24s\n")}, {Type: "stderr", Data: []byte("final unterminated diagnostic")}, {Type: "exit", Status: 9, Error: "maintenance failed"}}
		done := make(chan error, 1)
		go func() {
			wire := controller.NewWire(server, server)
			if _, err := wire.Read(); err != nil {
				done <- err
				return
			}
			if err := wire.Write(controller.Frame{Type: "hello", Hello: &hello}); err != nil {
				done <- err
				return
			}
			if _, err := wire.Read(); err != nil {
				done <- err
				return
			}
			for _, f := range frames {
				if err := wire.Write(f); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		returned = controller.Call(ctx, client, hello, controller.Request{ID: "fixture-request-0123456789", Method: "box.restart", Space: "work", Args: []string{"one"}, Dash: -1}, streams)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return returned
	})
	if got != returned {
		t.Fatal("presentation replaced the controller result", got, returned)
	}
	var remote *controller.ExitError
	if !errors.As(got, &remote) || remote.Status != 9 {
		t.Fatal("exit classification changed", got)
	}
	root.Fail(got)
	if rawOut.Len() != 0 || rawErr.Len() != 0 {
		t.Fatalf("maintenance leaked raw: out=%q err=%q", rawOut.String(), rawErr.String())
	}
	plain := ansi.Strip(shown.String())
	for _, line := range []string{"changed 2 packages in 24s", "New major version of npm available!", "final unterminated diagnostic", "typed worker notice"} {
		if !strings.Contains(plain, line) {
			t.Fatalf("lost %q in %s", line, plain)
		}
	}
	if !strings.Contains(plain, "• typed worker notice") {
		t.Fatalf("typed record became plain detail: %s", plain)
	}
	if strings.Contains(plain, "changed 2npm") {
		t.Fatal("stdout partial joined stderr")
	}
}

func TestRestartOutputOutsideDefaultViewKeepsExactStreams(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interactive bool
		level       prettylog.Verbosity
	}{{"pipes", false, prettylog.Normal}, {"debug-pipes", false, prettylog.Verbose}, {"debug-tty", true, prettylog.Verbose}, {"warn-pipes", false, prettylog.Quiet}, {"errors-tty", true, prettylog.Errors}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, shown := restartPresentationFixture(t, tc.interactive, tc.level)
			var out, errw bytes.Buffer
			sentinel := errors.New("original failure")
			calls := 0
			result := restartSessionOutput(t.Context(), "one", controller.ClientIO{Out: &out, Err: &errw}, func(streams controller.ClientIO) error {
				calls++
				if streams.Out != &out || streams.Err != &errw || streams.Log != nil {
					t.Fatal("line mode streams changed")
				}
				_, _ = io.WriteString(streams.Out, "summary\nunterminated")
				_, _ = io.WriteString(streams.Err, "npm notice\r\n")
				return sentinel
			})
			if result != sentinel || calls != 1 || out.String() != "summary\nunterminated" || errw.String() != "npm notice\r\n" || shown.Len() != 0 {
				t.Fatalf("plain/debug parity changed: %v %d %q %q %q", result, calls, out.String(), errw.String(), shown.String())
			}
		})
	}
}

func TestSpaceStartPresentationLeavesNoOrphanRowsInPinnedVT(t *testing.T) {
	l, view, out := restartPresentationFixture(t, true, prettylog.Normal)
	// Retained genuine npm maintenance stderr, not injected live output or a feature pin.
	banner, err := os.ReadFile("testdata/space-start-npm-notice.txt")
	if err != nil {
		t.Fatal(err)
	}
	root := l.StartStep("Starting space work", prettylog.Actor(logging.Host))
	root.Step("Started controller").Done()
	l.Slog().Info("Restarting box one")
	for i := range 60 {
		l.Slog().Info(fmt.Sprintf("Later restart log %d", i))
	}
	ctx := context.WithValue(t.Context(), cliStepContextKey{}, root)
	if err := restartSessionOutput(ctx, "one", controller.ClientIO{Out: out, Err: out}, func(streams controller.ClientIO) error {
		_, _ = streams.Out.Write([]byte("changed 2"))
		_, _ = streams.Err.Write(banner)
		_, _ = streams.Out.Write([]byte(" packages in 24s\n"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	root.Done()
	for i := range 60 {
		l.Slog().Info(fmt.Sprintf("After the final group %d", i))
	}
	view.Close()
	vt, err := terminal.New(50, 160, 5000, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	if _, err := vt.Write(out.Bytes()); err != nil {
		t.Fatal(err)
	}
	lines, err := vt.Lines(false, true, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	grid := strings.Join(lines, "\n")
	history, err := vt.Lines(true, true, 0, vt.Get(9))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(history, "\n") + "\n" + grid
	if strings.Count(all, "Started controller") != 1 || strings.Count(all, "Starting space work") != 1 || strings.ContainsAny(all, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("stale/duplicated final scrollback:\n%s", all)
	}
	if strings.Index(all, "Restarting box one") > strings.Index(all, "Starting space work") || strings.Index(all, "Starting space work") > strings.Index(all, "Started controller") || strings.Index(all, "Started controller") > strings.Index(all, "After the final group 0") {
		t.Fatalf("final group out of order:\n%s", all)
	}
	scrollback := strings.Join(history, "\n")
	if !strings.Contains(scrollback, "Starting space work") || !strings.Contains(scrollback, "Started controller") {
		t.Fatal("fixture did not exercise an off-screen retained group")
	}
}

func TestRestartOutputRenderingFailureKeepsOriginalResult(t *testing.T) {
	restoreInitial := logging.SetLogger(logging.Logger())
	logging.InitWorker(io.Discard, prettylog.Normal)
	l := prettylog.New(rejectedCLILog{}, prettylog.Options{Verbosity: prettylog.Normal})
	restoreLogger := logging.SetLogger(l)
	view := stepview.Install(l, rejectedCLILog{}, stepview.Options{Interval: time.Hour})
	t.Cleanup(func() { view.Close(); _ = l.Close(); restoreLogger(); restoreInitial() })
	sentinel := errors.New("original restart result")
	result := restartSessionOutput(t.Context(), "one", controller.ClientIO{}, func(streams controller.ClientIO) error {
		r := slog.NewRecord(time.Now(), prettylog.LevelNotice, "worker progress", 0)
		if err := streams.Log.Handle(t.Context(), r); err == nil {
			t.Fatal("fixture did not fail rendering")
		}
		_, _ = io.WriteString(streams.Err, "final diagnostic")
		return sentinel
	})
	if result != sentinel {
		t.Fatal("rendering replaced operation result", result)
	}
}
