package lima

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stderrDrainGrace is how long the filtered-stderr reader is given to flush
// a command's final stderr AFTER the child process has been reaped, before
// the read end is force-closed. It only elapses when a daemonized grandchild
// (e.g. limactl's long-lived host-agent) still holds the inherited stderr
// write end open — in the common case the pipe EOFs the instant the child
// exits and this is never reached.
const stderrDrainGrace = 200 * time.Millisecond

// DebugEnabled reports whether yoloctl should preserve noisy upstream
// subprocess diagnostics that are normally hidden from user-facing
// output.
func DebugEnabled() bool {
	return os.Getenv("YOLOCTL_DEBUG") == "1"
}

// FilterStderrText removes known-benign limactl warning lines unless
// YOLOCTL_DEBUG=1 is set. Lima occasionally emits compatibility
// warnings such as "treating lima version ... as very latest release"
// on otherwise successful commands; surfacing those in yoloctl's
// normal output makes healthy commands look broken.
func FilterStderrText(raw string) string {
	if raw == "" || DebugEnabled() {
		return raw
	}
	var b strings.Builder
	for len(raw) > 0 {
		line := raw
		rest := ""
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			line = raw[:idx+1]
			rest = raw[idx+1:]
		}
		if !isKnownBenignWarning(line) {
			b.WriteString(line)
		}
		raw = rest
		if rest == "" {
			break
		}
	}
	return b.String()
}

// WriteFilteredStderr applies FilterStderrText and writes the result
// to w. Nil writers and empty filtered output are no-ops.
func WriteFilteredStderr(w io.Writer, raw string) {
	if w == nil {
		return
	}
	filtered := FilterStderrText(raw)
	if filtered == "" {
		return
	}
	_, _ = io.WriteString(w, filtered)
}

// RunWithFilteredStderr runs cmd to completion with a streaming stderr
// filter attached, gating completion on the child PROCESS exiting — not on
// stderr reaching EOF. Use this for commands whose stderr is meant to be
// displayed live. For commands that capture stderr into a bytes.Buffer, keep
// using FilterStderrText / WriteFilteredStderr at the call site.
//
// See AttachFilteredStderr for why completion must not wait on stderr EOF.
func RunWithFilteredStderr(cmd *exec.Cmd, stderr io.Writer) error {
	stop := AttachFilteredStderr(cmd, stderr)
	defer stop()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}

// AttachFilteredStderr attaches a streaming, benign-warning-filtering reader
// to cmd's stderr and returns a stop function the caller MUST invoke once,
// AFTER cmd.Wait() has returned (the child reaped). Use this when callers
// drive Start/Wait themselves; RunWithFilteredStderr wraps it for the Run
// case. Idempotent and safe to call from a defer.
//
// It hands the child an *os.File (an OS pipe write end), NOT the filter
// io.Writer directly, and that distinction is load-bearing: a non-*os.File
// stderr makes os/exec allocate its own pipe and a copier goroutine that
// cmd.Wait() joins. A command that daemonizes a grandchild inheriting stderr
// — limactl's long-lived host-agent, spawned via `space create` / `space
// start` / `run` — keeps that write end open forever, so the copier never
// EOFs and cmd.Wait() blocks indefinitely (the symptom: `yoloctl space
// start` stuck "running" in the TUI). With an *os.File stderr, os/exec
// spawns no copier and cmd.Wait() gates on the PROCESS alone; we own the
// reader and force it closed shortly after the child exits, so a lingering
// grandchild can't wedge us.
func AttachFilteredStderr(cmd *exec.Cmd, stderr io.Writer) func() {
	if stderr == nil || DebugEnabled() {
		// Debug / discard: hand stderr straight to the child. Callers pass an
		// *os.File (os.Stderr or similar), so this stays copier-free too; a
		// nil writer sends the child's stderr to /dev/null.
		cmd.Stderr = stderr
		return func() {}
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		// Couldn't make a pipe — fall back to a direct attach. Callers pass
		// an *os.File here, so this stays copier-free (no hang); the only
		// loss is that benign upstream warnings aren't filtered out.
		cmd.Stderr = stderr
		return func() {}
	}
	cmd.Stderr = pw
	filter := &stderrLineFilter{dst: stderr}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 16*1024)
		for {
			n, rerr := pr.Read(buf)
			if n > 0 {
				_, _ = filter.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		filter.Flush()
		close(done)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			// Drop our copy of the write end so the pipe EOFs as soon as the
			// child (and any non-daemon descendants) have exited — the common
			// case finishes here with no delay. A daemonized grandchild
			// holding its inherited copy keeps the pipe open: give the reader
			// a beat to flush, then force the read end closed to stop it.
			_ = pw.Close()
			select {
			case <-done:
			case <-time.After(stderrDrainGrace):
			}
			_ = pr.Close()
			<-done
		})
	}
}

type stderrLineFilter struct {
	mu      sync.Mutex
	dst     io.Writer
	partial string
}

func (f *stderrLineFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	raw := f.partial + string(p)
	f.partial = ""
	for len(raw) > 0 {
		idx := strings.IndexByte(raw, '\n')
		if idx < 0 {
			f.partial = raw
			break
		}
		line := raw[:idx+1]
		if !isKnownBenignWarning(line) {
			if _, err := io.WriteString(f.dst, line); err != nil {
				return 0, err
			}
		}
		raw = raw[idx+1:]
	}
	return len(p), nil
}

func (f *stderrLineFilter) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.partial == "" {
		return
	}
	if !isKnownBenignWarning(f.partial) {
		_, _ = io.WriteString(f.dst, f.partial)
	}
	f.partial = ""
}

func isKnownBenignWarning(line string) bool {
	line = strings.ToLower(line)
	return strings.Contains(line, "treating lima version") &&
		strings.Contains(line, "as very latest release")
}
