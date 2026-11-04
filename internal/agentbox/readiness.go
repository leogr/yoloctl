package agentbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// Probe the VM-side endpoint without starting a sandboxed process. A socket
// inode can outlive its listener across stop/recreate, so test -S is not a
// readiness check. Python is also required by the VM's account-state helper.
const sandboxSSHProbe = `import socket, sys
try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(1)
        sock.connect(sys.argv[1])
        sock.sendall(b"SSH-2.0-yoloctl-readiness\r\n")
        with sock.makefile("rb") as stream:
            banner = stream.readline(256)
        if not (banner.startswith(b"SSH-2.0-") and banner.endswith(b"\n")):
            sys.exit(1)
except OSError:
    sys.exit(1)
`

// WaitForSandboxSocket waits for the supervisor to accept a connection and
// return its SSH greeting. Shared by create, recreate, restart (including
// policy rollback), and start of an existing container. Both an unresponsive
// listener and a stuck Lima invocation are bounded by the caller's deadline.
func WaitForSandboxSocket(ctx context.Context, space, name string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	socketPath := sandbox.SocketHostPath(name)
	nextInspect := time.Time{}
	for waitCtx.Err() == nil {
		probeCtx, probeCancel := context.WithTimeout(waitCtx, vmhost.GuestProbeTimeout)
		var err error
		if spaceenv.Native() {
			err = probeSandboxSSH(probeCtx, socketPath)
		} else {
			err = vmhost.ShellCommand(probeCtx, space, "sudo", "python3", "-c", sandboxSSHProbe, socketPath).Run()
		}
		probeCancel()
		if err == nil && waitCtx.Err() == nil {
			return nil
		}
		if time.Now().After(nextInspect) {
			if code, exited := sandboxContainerExit(waitCtx, space, name); exited {
				return fmt.Errorf("box %s exited with code %d before its SSH endpoint was ready; inspect with yoloctl box logs %s --space %s", name, code, name, space)
			}
			nextInspect = time.Now().Add(200 * time.Millisecond)
		}
		interval := 200 * time.Millisecond
		if spaceenv.Native() {
			interval = 10 * time.Millisecond
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(interval):
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("box SSH endpoint %s not ready after %s; inspect with yoloctl box logs %s --space %s: %w",
		socketPath, timeout, name, space, waitCtx.Err())
}

// The controller is already in the guest. Check the actual SSH greeting
// directly instead of launching a Python interpreter on each readiness poll.
func probeSandboxSSH(ctx context.Context, path string) error {
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probe, "unix", path)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(probe, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := probe.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := io.WriteString(conn, "SSH-2.0-yoloctl-readiness\r\n"); err != nil {
		return err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 256)).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "SSH-2.0-") || !strings.HasSuffix(line, "\n") {
		return fmt.Errorf("invalid sandbox SSH greeting")
	}
	return probe.Err()
}

// Only inspect state: container environment and logs can contain secrets.
// A failed/ambiguous inspect is not evidence of exit; keep the bounded SSH
// readiness wait in that case.
func sandboxContainerExit(ctx context.Context, space, name string) (int, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	cmd := vmhost.ShellCommand(probeCtx, space, "sudo", "nerdctl", "inspect", "--format", "{{json .State}}", name)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	var state struct {
		Status   string
		Running  bool
		ExitCode *int
	}
	if json.Unmarshal(out, &state) != nil || state.Running || state.ExitCode == nil {
		return 0, false
	}
	if state.Status == "exited" || state.Status == "dead" {
		return *state.ExitCode, true
	}
	return 0, false
}
