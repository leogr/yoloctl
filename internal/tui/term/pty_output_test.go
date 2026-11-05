package term

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

// A child that writes and exits before the master is read must not lose its
// output. A failing macOS CI run showed a direct `seq 1 100` reaped before
// the reader's first Read, which then returned EOF with nothing (the shell
// case kept its output). startPTYGeometry keeps the parent's slave until the
// child is reaped and released. Each case starts its reader late and
// independently of the single reaper; release gets a test deadline longer
// than the reader delay, so the result does not depend on whether the
// kernel holds the exiting child until the master is read.
func TestPTYKeepsOutputOfChildThatExitsBeforeRead(t *testing.T) {
	var seqWant strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&seqWant, "%d\r\n", i)
	}
	for _, tc := range []struct {
		name string
		argv []string
		env  []string
		want string
	}{
		{"direct seq", []string{"seq", "1", "100"}, nil, seqWant.String()},
		{"direct env", []string{"env"}, []string{"PTYMARK=kept"}, "PTYMARK=kept\r\n"},
		{"shell control", []string{"sh", "-c", "printf MARK; exit 0"}, nil, "MARK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(tc.argv[0], tc.argv[1:]...)
			cmd.Env = tc.env
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
			t.Logf("reaped after %v (reader started after %v), read %d bytes", reapedAt.Round(time.Millisecond), readDelay, len(got.out))
			if isTimeout(got.err) {
				t.Fatalf("read did not end at EOF: %v (got %q)", got.err, got.out)
			}
			if !strings.Contains(string(got.out), tc.want) {
				t.Fatalf("output of a child that exited before the read was lost or incomplete: want %q in %q (read end: %v)", tc.want, got.out, got.err)
			}
		})
	}
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
