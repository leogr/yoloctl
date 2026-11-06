package sandbox

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/sshtransport"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"golang.org/x/crypto/ssh"
)

// Stop asks OpenShell to signal the sandbox user's processes in its private PID
// namespace. PID 1 is excluded by kill(-1), so the supervisor can reap its
// entrypoint and clean up networking. No guest PID is used in the VM namespace.
// A broken SSH endpoint or a child ignoring SIGTERM still gets the runtime's
// bounded stop/escalation. Session-aware callers tear down holders first.
func Stop(ctx context.Context, c *container.Client, name string) error {
	return stopWithProbeTimeout(ctx, c, name, nil)
}

// Only semantic command fixtures replace timeout construction; public Stop
// always uses the same three-second, parent-bounded probe.
func stopWithProbeTimeout(ctx context.Context, c *container.Client, name string, newTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)) error {
	if err := spaceenv.RequireSpace(c.Env()); err != nil {
		return err
	}
	if err := layout.ValidateName("box", name); err != nil {
		return err
	}
	probe, cancel := stopProbeContext(ctx, newTimeout)
	var conn net.Conn
	var err error
	if spaceenv.Native() {
		conn, err = (&net.Dialer{}).DialContext(probe, "unix", SocketHostPath(name))
	} else {
		proxy := vmhost.ShellCommand(probe, c.Env(), "sudo", "socat", "STDIO", "UNIX-CONNECT:"+SocketHostPath(name))
		conn, err = sshtransport.ProcessConn(probe, proxy)
	}
	if err == nil {
		// Successful signalling commonly closes the SSH connection before it can
		// return an exit status; runtime stop below confirms the final outcome.
		_ = signalWorkloads(probe, conn)
	}
	cancel()
	return c.Stop(ctx, name)
}

func stopProbeContext(ctx context.Context, newTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)) (context.Context, context.CancelFunc) {
	if newTimeout == nil {
		newTimeout = context.WithTimeout
	}
	return newTimeout(ctx, 3*time.Second)
}

func signalWorkloads(ctx context.Context, conn net.Conn) error {
	input, err := os.Open(os.DevNull)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = input.Close() }()
	// This is the existing private OpenShell socket, protected by its VM-side
	// directory. No host SSH config, identity, agent or network endpoint is used.
	return sshtransport.Run(ctx, conn, &ssh.ClientConfig{
		User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}, "exec /bin/sh -c 'kill -TERM -1'", false, input, io.Discard, io.Discard)
}
