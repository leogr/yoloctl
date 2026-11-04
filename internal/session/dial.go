package session

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// DialVM connects to an agentbox's attach socket from the host, through
// the same `limactl shell -- sudo socat` bridge ssh-proxy uses for the
// sandbox SSH socket. The returned transport is the bridge subprocess's
// stdio; Close ends the bridge — which to the holder is just this
// client detaching.
func DialVM(ctx context.Context, space, agentbox string) (io.ReadWriteCloser, error) {
	if spaceenv.Native() {
		if err := spaceenv.RequireSpace(space); err != nil {
			return nil, err
		}
		// The worker is already next to the holder. A local connection avoids
		// a socat child receiving the worker's detach signal and reporting a
		// spurious failure before the successful detach hint.
		return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(agentbox))
	}
	cmd := vmhost.ShellCommand(ctx, space,
		"sudo", "socat", "-", "UNIX-CONNECT:"+SocketPath(agentbox))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("session: bridge stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("session: bridge stdout: %w", err)
	}
	flush := vmhost.AttachFilteredStderr(cmd, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("session: start bridge: %w", err)
	}
	return &vmTransport{in: stdin, out: stdout, cmd: cmd, flush: flush}, nil
}

type vmTransport struct {
	in    io.WriteCloser
	out   io.ReadCloser
	cmd   *exec.Cmd
	flush func()
	once  sync.Once
}

func (t *vmTransport) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *vmTransport) Write(p []byte) (int, error) { return t.in.Write(p) }

// Close tears the bridge down. Killing the local limactl/ssh leg drops
// the remote socat's stdio, ending it too; the holder sees a plain
// client disconnect.
func (t *vmTransport) Close() error {
	t.once.Do(func() {
		_ = t.in.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		_ = t.cmd.Wait()
		t.flush()
	})
	return nil
}
