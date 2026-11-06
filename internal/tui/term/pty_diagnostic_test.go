package term

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func logPTYStartFailure(t *testing.T, stage string, err error) {
	t.Helper()
	var errno syscall.Errno
	var pathError *os.PathError
	errors.As(err, &errno)
	op, path := "", ""
	if errors.As(err, &pathError) {
		op, path = pathError.Op, pathError.Path
	}
	t.Logf("launch diagnostic: stage=%s error_type=%T errno=%d operation=%q path=%q syscall_origin=UNKNOWN error=%v", stage, err, errno, op, path, err)
}

// Opt-in host investigation; never retries a launch or changes the output test.
// Go's Darwin fork/exec error pipe merges setsid, TIOCSCTTY and execve errors.
// These separate launches isolate setup combinations, not the exact syscall
// that failed in a different launch. A success is only no-reproduction evidence.
func TestPTYStartDiagnostics(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_PTY_DIAGNOSTICS") != "1" {
		t.Skip("opt-in host launch diagnostics")
	}
	t.Logf("test binary: GOOS=%s GOARCH=%s runtime=%s", runtime.GOOS, runtime.GOARCH, runtime.Version())
	for _, argv := range [][]string{{"seq", "1", "100"}, {"env"}, {"sh", "-c", "printf MARK"}} {
		for _, mode := range []string{"exec", "setsid", "controlling-pty"} {
			t.Run(argv[0]+"/"+mode, func(t *testing.T) {
				path, err := exec.LookPath(argv[0])
				if err != nil {
					logPTYStartFailure(t, "resolve", err)
					t.Fatal(err)
				}
				t.Logf("resolved executable=%q mode=%s", path, mode)
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, path, argv[1:]...)
				cmd.Env = []string{"PTYMARK=diagnostic"}
				cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
				if mode != "exec" {
					cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				}
				if mode == "controlling-pty" {
					master, slave, err := pty.Open()
					if err != nil {
						logPTYStartFailure(t, "pty.Open", err)
						t.Fatal(err)
					}
					defer func() { _ = master.Close() }()
					defer func() { _ = slave.Close() }()
					if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
						logPTYStartFailure(t, "pty.Setsize", err)
						t.Fatal(err)
					}
					cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
					cmd.SysProcAttr.Setctty = true
				}
				if err := cmd.Start(); err != nil {
					logPTYStartFailure(t, "cmd.Start", err)
					t.Fatal(err)
				}
				t.Log("stage=cmd.Start success=true")
				// Fixed small outputs fit in the PTY queue; output retention is asserted
				// by the original three-case test, not by this setup diagnostic.
				if err := cmd.Wait(); err != nil {
					logPTYStartFailure(t, "cmd.Wait", err)
					t.Fatal(err)
				}
			})
		}
	}
}
