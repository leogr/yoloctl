//go:build linux

package workercheckpoint

import (
	"errors"

	"golang.org/x/sys/unix"
)

func (c *Channel) observeOriginal() (out Identity, result error) {
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
		if (fileIdentity{uint64(st.Dev), uint64(st.Ino), uint32(st.Mode) & unix.S_IFMT}) != c.base || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
			result = errors.New("bound checkpoint is not original socket")
			return
		}
		flags, e := unix.FcntlInt(fd, unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC == 0 {
			result = errors.Join(errors.New("bound checkpoint lost CLOEXEC"), e)
			return
		}
		domain, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_DOMAIN)
		if e != nil || domain != unix.AF_UNIX {
			result = errors.New("bound checkpoint is not AF_UNIX")
			return
		}
		typ, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if e != nil || typ != unix.SOCK_STREAM {
			result = errors.New("bound checkpoint is not stream")
			return
		}
		peer, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil || peer == nil {
			result = errors.Join(errors.New("bound checkpoint creation peer unavailable"), e)
			return
		}
		out = Identity{int(fd), uint64(st.Dev), uint64(st.Ino), uint32(st.Mode) & unix.S_IFMT, peer.Pid, peer.Uid, peer.Gid}
	})
	if err != nil || result != nil {
		return Identity{}, errors.Join(err, result)
	}
	return out, nil
}
