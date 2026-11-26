package controller

import (
	"os"

	"golang.org/x/sys/unix"
)

// available reports, without waiting, how many bytes f has ready to read
// (TIOCINQ, FIONREAD on Linux: pipes and PTY masters).
func available(f *os.File) int {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0
	}
	n := 0
	_ = raw.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCINQ); err == nil && v > 0 {
			n = v
		}
	})
	return n
}
