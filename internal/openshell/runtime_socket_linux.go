package openshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const publicRuntimeSSHDirectory = "/run/openshell"

// runtimeSSH is a transparent Unix transport owned by PID 1. It has no SSH
// authentication, command or arbitrary-target interface of its own. The exact
// supervisor endpoint remains the only sandbox workload entry.
type runtimeSSH struct {
	directory *os.File // borrowed until Close completes
	listener  *net.UnixListener
	inode     unix.Stat_t
	uid, gid  int
	once      sync.Once
	closeErr  error
}

// listenRuntimeSSHAt receives an already verified, private socket bind. The
// caller must have checked native activation, live child identities, health and
// pinned SSH host key before publication. Existing sockets are never adopted.
// Owner parameters allow ordinary Unix-socket tests; PID 1 supplies 0:0.
func listenRuntimeSSHAt(directory *os.File, uid, gid int) (_ *runtimeSSH, err error) {
	if err := runtimeSSHDirectoryOwner(directory, uid, gid); err != nil {
		return nil, err
	}
	var existing unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "ssh.sock", &existing, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return nil, errors.New("public runtime SSH endpoint already exists or cannot be inventoried")
	}
	alias := fmt.Sprintf("/proc/self/fd/%d/ssh.sock", directory.Fd())
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: alias, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	endpoint := &runtimeSSH{directory: directory, listener: listener, uid: uid, gid: gid}
	if err := unix.Fstatat(int(directory.Fd()), "ssh.sock", &endpoint.inode, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = listener.Close()
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, endpoint.Close())
		}
	}()
	if endpoint.inode.Mode&unix.S_IFMT != unix.S_IFSOCK || endpoint.inode.Uid != uint32(uid) || endpoint.inode.Gid != uint32(gid) || endpoint.inode.Nlink != 1 {
		return nil, errors.New("public runtime SSH socket has unexpected type, owner or links")
	}
	if err := unix.Fchmodat(int(directory.Fd()), "ssh.sock", 0o600, 0); err != nil {
		return nil, err
	}
	if err := syncRuntimeSSHDirectory(directory); err != nil {
		return nil, err
	}
	return endpoint, nil
}

// This is the public socket directory, not a private authority directory.
// Match the creator/forward/withdrawal contract: trusted owner and no other
// writers or special bits. The published socket itself remains exactly0600.
func runtimeSSHDirectoryOwner(directory *os.File, uid, gid int) error {
	if directory == nil {
		return errors.New("public runtime SSH directory is unavailable")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7022 != 0 {
		return errors.New("public runtime SSH directory has an untrusted owner, writer or type")
	}
	return nil
}

// The infrastructure walker intentionally lends an O_PATH descriptor. Open
// only '.' relative to that SAME retained directory for fsync; never resolve
// its pathname again or synchronize an unverified replacement.
func syncRuntimeSSHDirectory(directory *os.File) error {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return errors.Join(unix.Fsync(fd), unix.Close(fd))
}

func (e *runtimeSSH) Close() error {
	e.once.Do(func() {
		if err := e.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			e.closeErr = err
		}
		var current unix.Stat_t
		if err := unix.Fstatat(int(e.directory.Fd()), "ssh.sock", &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			e.closeErr = errors.Join(e.closeErr, err)
			return
		}
		if current.Dev != e.inode.Dev || current.Ino != e.inode.Ino || current.Mode&unix.S_IFMT != unix.S_IFSOCK || current.Uid != uint32(e.uid) || current.Gid != uint32(e.gid) || current.Nlink != 1 {
			e.closeErr = errors.Join(e.closeErr, errors.New("public runtime SSH socket changed; refusing foreign cleanup"))
			return
		}
		e.closeErr = errors.Join(e.closeErr, unix.Unlinkat(int(e.directory.Fd()), "ssh.sock", 0))
		if e.closeErr == nil {
			e.closeErr = syncRuntimeSSHDirectory(e.directory)
		}
	})
	return e.closeErr
}

// serveRuntimeSSH keeps active streams independent. It rechecks admission and
// the fixed private socket on every new connection, never reconnects a broken
// stream, and preserves EOF in one direction while responses still arrive.
func (e *runtimeSSH) serveRuntimeSSH(ctx context.Context, target string, admit func() bool, checkTarget func() error) (err error) {
	if target == "" || admit == nil || checkTarget == nil {
		return errors.New("runtime SSH relay lacks its fixed target or live admission")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(serveCtx, func() { _ = e.Close() })
	defer stop()
	// Accept can observe listener.Close before its asynchronous caller finishes
	// unlink/fsync. Join the once-only cleanup before reporting termination.
	defer func() { err = errors.Join(err, e.Close()) }()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	slots := make(chan struct{}, 64)
	for {
		incoming, err := e.listener.AcceptUnix()
		if err != nil {
			cancel()
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = incoming.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-slots }()
			defer func() { _ = incoming.Close() }()
			stopIncoming := context.AfterFunc(serveCtx, func() { _ = incoming.Close() })
			defer stopIncoming()
			if !admit() || checkTarget() != nil {
				return
			}
			setup, cancelSetup := context.WithTimeout(serveCtx, time.Second)
			outgoing, err := (&net.Dialer{}).DialContext(setup, "unix", target)
			cancelSetup()
			if err != nil {
				return
			}
			defer func() { _ = outgoing.Close() }()
			stopOutgoing := context.AfterFunc(serveCtx, func() { _ = outgoing.Close() })
			defer stopOutgoing()
			if !admit() || checkTarget() != nil {
				return
			}
			_ = relayRuntimeSSH(incoming, outgoing)
		}()
	}
}

func relayRuntimeSSH(first, second net.Conn) error {
	results := make(chan error, 2)
	copyHalf := func(destination, source net.Conn) {
		_, err := io.Copy(destination, source)
		if half, ok := destination.(interface{ CloseWrite() error }); ok {
			err = errors.Join(err, half.CloseWrite())
		} else {
			err = errors.Join(err, errors.New("runtime SSH transport has no write half-close"))
		}
		results <- err
	}
	go copyHalf(first, second)
	go copyHalf(second, first)
	firstErr := <-results
	if firstErr != nil {
		_ = first.Close()
		_ = second.Close()
	}
	return errors.Join(firstErr, <-results)
}
