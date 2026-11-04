package controller

import "golang.org/x/sys/unix"

const nativePhaseTransportAvailable = true

func nativePhasePipeIdentity(fd int) (*NativePhasePipeID, bool) {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil, false
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return nil, false
	}
	return &NativePhasePipeID{Device: uint64(stat.Dev), Inode: stat.Ino}, true
}

// Increasing capacity is optional and failure never becomes a prerequisite.
// Raw child writes remain finite/nonblocking even on smaller kernel pipes.
func nativePhaseEnlargePipe(fd int) {
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETPIPE_SZ, 64<<10)
}

func nativePhaseOpenWriter(fd int, expected NativePhasePipeID) *NativePhaseWriter {
	actual, ok := nativePhasePipeIdentity(fd)
	if !ok || *actual != expected {
		return nil // An unowned FD must not be closed or modified.
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err == nil {
		_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC)
	}
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	if err != nil {
		_ = unix.Close(fd) // Exact validated diagnostic ownership only.
		return nil
	}
	return &NativePhaseWriter{fd: fd}
}

func nativePhaseWrite(fd int, raw []byte) (int, bool) {
	n, err := unix.Write(fd, raw)
	if n < 0 {
		n = 0
	}
	return n, err == unix.EINTR || err == nil && n > 0
}

func nativePhaseClose(fd int) { _ = unix.Close(fd) }
