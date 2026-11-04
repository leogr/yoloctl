//go:build linux || darwin

package workercheckpoint

import (
	"context"
	"errors"
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

func (c *Channel) initialize() (out fileIdentity, result error) {
	raw, err := c.file.SyscallConn()
	if err != nil {
		return out, err
	}
	err = raw.Control(func(fd uintptr) {
		var st unix.Stat_t
		if e := unix.Fstat(int(fd), &st); e != nil {
			result = e
			return
		}
		out = fileIdentity{uint64(st.Dev), uint64(st.Ino), uint32(st.Mode) & unix.S_IFMT}
		flags, e := unix.FcntlInt(fd, unix.F_GETFD, 0)
		if e != nil {
			result = e
			return
		}
		if _, e = unix.FcntlInt(fd, unix.F_SETFD, flags|unix.FD_CLOEXEC); e != nil {
			result = e
			return
		}
		result = unix.SetNonblock(int(fd), true)
	})
	return out, errors.Join(err, result)
}
func (c *Channel) part(ctx context.Context, b []byte, write bool) (n int, result error) {
	for {
		if err := contextError(ctx); err != nil {
			return 0, err
		}
		select {
		case <-c.done:
			return 0, c.closedError()
		default:
		}
		raw, err := c.file.SyscallConn()
		if err != nil {
			return 0, err
		}
		var opErr error
		err = raw.Control(func(fd uintptr) {
			var st unix.Stat_t
			if e := unix.Fstat(int(fd), &st); e != nil {
				opErr = e
				return
			}
			id := fileIdentity{uint64(st.Dev), uint64(st.Ino), uint32(st.Mode) & unix.S_IFMT}
			if id != c.base {
				opErr = errors.New("original checkpoint file identity changed")
				return
			}
			flags, e := unix.FcntlInt(fd, unix.F_GETFD, 0)
			if e != nil || flags&unix.FD_CLOEXEC == 0 {
				opErr = errors.Join(errors.New("original checkpoint lost CLOEXEC"), e)
				return
			}
			// Retain the SAME File; never call Fd(), duplicate or wrap a replacement.
			if e := unix.SetNonblock(int(fd), true); e != nil {
				opErr = e
				return
			}
			if write {
				n, opErr = unix.Write(int(fd), b)
			} else {
				n, opErr = unix.Read(int(fd), b)
			}
		})
		if err != nil {
			return 0, err
		}
		if opErr == nil {
			if n == 0 && !write {
				return 0, io.EOF
			}
			return n, nil
		}
		if errors.Is(opErr, syscall.EINTR) {
			continue
		}
		if !errors.Is(opErr, syscall.EAGAIN) && !errors.Is(opErr, syscall.EWOULDBLOCK) {
			return n, opErr
		}
		events := int16(unix.POLLIN)
		if write {
			events = unix.POLLOUT
		}
		// A bounded poll under Control protects the original File reference and
		// lets Close/cancellation settle without depending on os.File poller setup
		// in old private worker test/legacy constructor paths. No arbitrary timeout.
		pollErr := error(nil)
		err = raw.Control(func(fd uintptr) { _, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: events}}, 20) })
		if err != nil {
			return 0, err
		}
		if pollErr != nil && !errors.Is(pollErr, syscall.EINTR) {
			return 0, pollErr
		}
	}
}
func (c *Channel) readPart(ctx context.Context, b []byte) (int, error)  { return c.part(ctx, b, false) }
func (c *Channel) writePart(ctx context.Context, b []byte) (int, error) { return c.part(ctx, b, true) }
