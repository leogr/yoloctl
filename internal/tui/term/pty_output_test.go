package term

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

// A child that writes and exits before the master is read must not lose its
// output. macOS discards unread PTY output when the last slave reference
// closes (Linux retains it), so startPTYGeometry keeps the parent's slave
// until the child is reaped and released. The reader starts late and runs
// independently of the single reaper; release gets a test deadline longer
// than the reader delay, so the test does not depend on whether the
// kernel's exit drain blocks Wait.
func TestPTYKeepsOutputOfChildThatExitsBeforeRead(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf MARK; exit 0")
	master, release, err := startPTYGeometry(cmd, terminal.Geometry{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = master.Close() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	}()

	const readDelay = 300 * time.Millisecond
	started := time.Now()
	type readResult struct {
		out []byte
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		time.Sleep(readDelay)
		_ = master.SetReadDeadline(time.Now().Add(5 * time.Second))
		var out bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				readDone <- readResult{out.Bytes(), err}
				return
			}
		}
	}()
	reaped := make(chan time.Duration, 1)
	go func() {
		_ = cmd.Wait()
		at := time.Since(started)
		t.Logf("DIAG master-pending at reap=%s", pending(master)) // diagnostic branch only
		release(time.Now().Add(2 * time.Second))
		reaped <- at
	}()

	var got readResult
	select {
	case got = <-readDone:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not finish")
	}
	var reapedAt time.Duration
	select {
	case reapedAt = <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("reaper did not finish")
	}
	t.Logf("reaped after %v (reader started after %v)", reapedAt.Round(time.Millisecond), readDelay)
	if isTimeout(got.err) {
		t.Fatalf("read did not end at EOF: %v (got %q)", got.err, got.out)
	}
	if !strings.Contains(string(got.out), "MARK") {
		t.Fatalf("output of a child that exited before the read was lost: got %q (read end: %v)", got.out, got.err)
	}
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
