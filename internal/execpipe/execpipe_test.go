package execpipe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// command runs script in its own process group; cleanup kills the group so a
// fixture's background holder never outlives the test.
func command(t *testing.T, ctx context.Context, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return cmd
}

func TestRunReturnsCompleteOutputAndStatus(t *testing.T) {
	var out, errOut bytes.Buffer
	err := Run(t.Context(), command(t, t.Context(), `printf out; printf err >&2`), 0, &out, &errOut)
	if err != nil || out.String() != "out" || errOut.String() != "err" {
		t.Fatalf("complete output changed: %q %q %v", out.String(), errOut.String(), err)
	}
	err = Run(t.Context(), command(t, t.Context(), `printf partial; exit 7`), 0, &out, nil)
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 7 {
		t.Fatalf("exit status lost: %v", err)
	}
}

// leaderExited cancels after the fixture's leader has written its pid to mark
// as its last act and that process is gone, so a cancellation tests a held
// pipe after the exit, never a live leader.
func leaderExited(t *testing.T, mark string, then func()) {
	t.Helper()
	go func() {
		for {
			raw, err := os.ReadFile(mark)
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && convErr == nil && pid > 0 {
				if syscall.Kill(pid, 0) == syscall.ESRCH {
					then()
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// holderScript keeps stream open in a background descendant, records the
// leader's pid in mark and exits.
func holderScript(stream, mark string) string {
	holder := `(exec 2>/dev/null; sleep 30) &`
	if stream == "stderr" {
		holder = `(exec >/dev/null; sleep 30) &`
	}
	return holder + ` printf done; echo $$ > ` + strconv.Quote(mark)
}

// A descendant that keeps an output pipe open after the command exits holds
// Run only until the caller's deadline or cancellation, or the drain bound,
// not for the life of the descendant.
func TestRunEndsHeldPipeAfterExit(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, end := range []string{"cancel", "deadline", "drain"} {
			t.Run(stream+"/"+end, func(t *testing.T) {
				mark := filepath.Join(t.TempDir(), "leader")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				drain := time.Duration(0)
				switch end {
				case "cancel":
					drain = time.Hour
					leaderExited(t, mark, cancel)
				case "deadline":
					drain = time.Hour
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 500*time.Millisecond)
					defer stop()
				case "drain":
					drain = 300 * time.Millisecond
				}
				var out, errOut bytes.Buffer
				start := time.Now()
				err := Run(ctx, command(t, ctx, holderScript(stream, mark)), drain, &out, &errOut)
				want := map[string]error{"cancel": context.Canceled, "deadline": context.DeadlineExceeded, "drain": ErrOutputHeld}[end]
				if !errors.Is(err, want) || time.Since(start) > 3*time.Second {
					t.Fatalf("held %s was not ended by %s: %v after %v", stream, end, err, time.Since(start))
				}
				if end == "drain" && time.Since(start) < drain {
					t.Fatal("drain bound ended the wait before it started", time.Since(start))
				}
			})
		}
	}
}

// The drain bound starts at the exit: a command that runs longer than it
// still completes with its full output.
func TestRunDrainBoundStartsAtExit(t *testing.T) {
	var out bytes.Buffer
	if err := Run(t.Context(), command(t, t.Context(), `sleep 0.5; printf complete`), 100*time.Millisecond, &out, nil); err != nil || out.String() != "complete" {
		t.Fatalf("a running command was cut by the drain bound: %q %v", out.String(), err)
	}
}

// Without a held pipe, a command that exits returns at once even when its
// caller has no deadline.
func TestRunDoesNotWaitForTheCallerAfterEOF(t *testing.T) {
	var out bytes.Buffer
	start := time.Now()
	if err := Run(t.Context(), command(t, t.Context(), `printf ok`), 0, &out, nil); err != nil || out.String() != "ok" {
		t.Fatal(out.String(), err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("finished command waited", time.Since(start))
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// A writer error stops that stream and closes its read end, so the command
// sees a broken pipe instead of blocking on a full pipe.
func TestRunWriterErrorBreaksThePipe(t *testing.T) {
	boom := errors.New("capture refused")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := Run(ctx, command(t, ctx, `yes x`), 0, failingWriter{boom}, nil)
	if !errors.Is(err, boom) || ctx.Err() != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("writer error did not end the command: %v after %v", err, time.Since(start))
	}
}

// Run replaces output the command already had and leaves a nil stream at
// /dev/null.
func TestRunReplacesPresetOutput(t *testing.T) {
	preset := filepath.Join(t.TempDir(), "preset")
	f, err := os.Create(preset)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cmd := command(t, t.Context(), `printf out; printf err >&2`)
	cmd.Stdout, cmd.Stderr = f, f
	var out bytes.Buffer
	if err := Run(t.Context(), cmd, 0, &out, nil); err != nil || out.String() != "out" {
		t.Fatal(out.String(), err)
	}
	if raw, _ := os.ReadFile(preset); len(raw) != 0 {
		t.Fatalf("preset output still received %q", raw)
	}
}

// A context that has already ended starts nothing.
func TestRunRefusesEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	marker := filepath.Join(t.TempDir(), "started")
	err := Run(ctx, command(t, ctx, `touch `+strconv.Quote(marker)), 0, io.Discard, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) || strings.Contains(err.Error(), "touch") {
		t.Fatal("ended context still started the command", statErr, err)
	}
}
