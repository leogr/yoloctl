package term

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
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
					stage, err := runDiagnosticPTY(ctx, cmd, func() { t.Log("stage=cmd.Start success=true") })
					if err != nil {
						logPTYStartFailure(t, stage, err)
						t.Fatal(err)
					}
					return
				}

				if err := cmd.Start(); err != nil {
					logPTYStartFailure(t, "cmd.Start", err)
					t.Fatal(err)
				}
				t.Log("stage=cmd.Start success=true")
				if err := waitDiagnosticChild(ctx, cmd, func() {}); err != nil {
					logPTYStartFailure(t, "cmd.Wait", err)
					t.Fatal(err)
				}
			})
		}
	}
}

// waitDiagnosticChild owns the only Wait. Closing the PTY before awaiting
// reaping prevents terminal backpressure from trapping cancellation cleanup.
func waitDiagnosticChild(ctx context.Context, cmd *exec.Cmd, unblock func()) error {
	defer unblock()
	reaped := make(chan error, 1)
	go func() { reaped <- cmd.Wait() }()
	select {
	case err := <-reaped:
		if err != nil {
			return errors.Join(err, ctx.Err())
		}
		return nil
	case <-ctx.Done():
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		unblock()
		select {
		case err := <-reaped:
			return errors.Join(ctx.Err(), killErr, err)
		case <-time.After(5 * time.Second):
			return errors.Join(ctx.Err(), killErr, errors.New("diagnostic reaper did not settle after unblocking"))
		}
	}
}

// The diagnostic discards output concurrently; the separate, unchanged output
// acceptance test is responsible for asserting complete retained bytes.
func runDiagnosticPTY(ctx context.Context, cmd *exec.Cmd, started func()) (stage string, resultErr error) {
	master, slave, err := pty.Open()
	if err != nil {
		return "pty.Open", err
	}
	var closeErr error
	closePTY := sync.OnceFunc(func() {
		for _, file := range []*os.File{slave, master} {
			if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErr = errors.Join(closeErr, err)
			}
		}
	})
	defer func() {
		closePTY()
		resultErr = errors.Join(resultErr, closeErr)
	}()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		return "pty.Setsize", err
	}
	pollable, err := pollablePTY(master)
	if err != nil {
		return "pollablePTY", err
	}
	master = pollable
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setctty = true, true
	if err := cmd.Start(); err != nil {
		return "cmd.Start", err
	}
	drained := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, master)
		drained <- err
	}()
	if started != nil {
		started()
	}
	waitErr := waitDiagnosticChild(ctx, cmd, closePTY)
	// Also close on successful exit so the parent-held slave cannot keep the
	// reader alive. A pollable master makes close interrupt the pending read.
	closePTY()
	select {
	case readErr := <-drained:
		// Closing our master interrupts the read; an exited session leader
		// can also revoke its terminal and produce EIO instead of EOF.
		if readErr != nil && !errors.Is(readErr, os.ErrClosed) && !errors.Is(readErr, syscall.EIO) {
			return "PTY read", errors.Join(waitErr, readErr)
		}
		return "cmd.Wait", waitErr
	case <-time.After(5 * time.Second):
		return "reader cleanup", errors.Join(waitErr, errors.New("diagnostic reader did not settle after close"))
	}
}

func TestDiagnosticPTYDrainsBackpressureAndSettles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "seq", "1", "200000")
	cmd.Env = []string{"PTYMARK=diagnostic"}
	if stage, err := runDiagnosticPTY(ctx, cmd, nil); err != nil {
		t.Fatal(stage, err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("child was not reaped successfully")
	}
}

func TestDiagnosticPTYCancellationAndStartFailureSettle(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", "while :; do :; done")
		cmd.Env = []string{"PTYMARK=diagnostic"}
		stage, err := runDiagnosticPTY(ctx, cmd, cancel)
		if stage != "cmd.Wait" || !errors.Is(err, context.Canceled) || cmd.ProcessState == nil {
			t.Fatal("cancel did not settle child", stage, err)
		}
	})
	t.Run("start-failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "absent"))
		if stage, err := runDiagnosticPTY(ctx, cmd, nil); stage != "cmd.Start" || !errors.Is(err, os.ErrNotExist) {
			t.Fatal(stage, err)
		}
		if cmd.Process != nil {
			t.Fatal("failed start created a process")
		}
	})
}
