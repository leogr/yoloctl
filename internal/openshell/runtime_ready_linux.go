package openshell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

var errRuntimeSSHKey = errors.New("runtime SSH endpoint differs from the pinned generation key")

// probeRuntimeReadyAt requires both the upstream's private readiness listener
// (published after attach/confirm) and a responding SSH handshake with this
// generation's key. Merely connecting to a stopped server's backlog is not
// readiness. No workload, session, exec request or forwarding channel is sent.
func probeRuntimeReadyAt(ctx context.Context, run *os.File, key ssh.PublicKey, uid, gid int) (bool, error) {
	if key == nil || key.Type() != ssh.KeyAlgoED25519 {
		return false, errors.New("runtime readiness lacks its pinned SSH identity")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, name := range []string{"ready.sock", "ssh/ssh.sock"} {
		if err := checkRuntimePrivateSocket(run, name, uid, gid); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return false, nil
			}
			return false, err
		}
	}
	health := fmt.Sprintf("/proc/self/fd/%d/ready.sock", run.Fd())
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", health)
	if err != nil {
		return false, nil
	}
	if err := conn.Close(); err != nil {
		return false, err
	}
	target := fmt.Sprintf("/proc/self/fd/%d/ssh/ssh.sock", run.Fd())
	conn, err = (&net.Dialer{}).DialContext(ctx, "unix", target)
	if err != nil {
		return false, nil
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return false, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	transport, _, _, err := ssh.NewClientConn(conn, target, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: func(_ string, _ net.Addr, actual ssh.PublicKey) error {
		if !bytes.Equal(actual.Marshal(), key.Marshal()) {
			return errRuntimeSSHKey
		}
		return nil
	}})
	if err != nil {
		if errors.Is(err, errRuntimeSSHKey) {
			return false, errRuntimeSSHKey
		}
		return false, nil
	}
	defer func() { _ = transport.Close() }()
	if err := ctx.Err(); err != nil {
		return false, nil
	}
	// Recheck the currently selected paths after the handshake. The private
	// supervisor can legitimately remove/rebind readiness as attach degrades.
	for _, name := range []string{"ready.sock", "ssh/ssh.sock"} {
		if err := checkRuntimePrivateSocket(run, name, uid, gid); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func checkRuntimePrivateSocket(root *os.File, name string, uid, gid int) error {
	fd, err := unix.Openat2(int(root.Fd()), name, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Nlink != 1 || stat.Mode&0o7000 != 0 {
		return errors.New("runtime private endpoint has an unexpected type, owner or link")
	}
	return nil
}
