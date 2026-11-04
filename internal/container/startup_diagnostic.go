package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const startupDiagnosticBudget = 2 * time.Second
const startupDiagnosticPipeWait = 250 * time.Millisecond
const startupDiagnosticLogLimit = 4096

// StartupFailureDiagnostics reads only the original acknowledged CID. It must
// be called before owned failed-create cleanup removes that container's logs.
// Output is quoted for terminal safety and never supplies lifecycle authority.
// Diagnostic failure does not replace the original startup failure or replay it.
func (c *Client) StartupFailureDiagnostics(ctx context.Context, id string) string {
	if c == nil || ctx == nil || !nativeMountCID(id) {
		return "startup diagnostics unavailable: original full container ID required"
	}
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), startupDiagnosticBudget)
	defer cancel()
	state := c.startupDiagnosticRead(read, 512, "inspect", "--mode", "dockercompat", "--format", "{{.State.Status}} exit={{.State.ExitCode}} pid={{.State.Pid}}", id)
	logs := c.startupDiagnosticRead(read, startupDiagnosticLogLimit, "logs", "--tail", "16", id)
	return "container state: " + state + "; helper output: " + logs
}

// A named byte slice prevents io.Copy from bypassing Write via a promoted
// ReaderFrom. Stdout and stderr share one bounded, synchronized capture.
type startupDiagnosticCapture struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	cancel    context.CancelFunc
	truncated bool
}

func (w *startupDiagnosticCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := min(len(p), w.limit-len(w.data))
	w.data = append(w.data, p[:n]...)
	if n != len(p) {
		w.truncated = true
		w.cancel()
		return n, errors.New("startup diagnostic output exceeded its bound")
	}
	return n, nil
}

func (c *Client) startupDiagnosticRead(ctx context.Context, limit int, args ...string) string {
	read, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &startupDiagnosticCapture{data: make([]byte, 0, limit), limit: limit, cancel: cancel}
	cmd := c.command(read, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = startupDiagnosticPipeWait
	err := cmd.Run()
	out.mu.Lock()
	defer out.mu.Unlock()
	result := fmt.Sprintf("%q", strings.TrimSpace(string(out.data)))
	if out.truncated {
		result += " [truncated]"
	}
	if err != nil {
		result += fmt.Sprintf(" [capture failed: %v]", err)
	}
	return result
}
