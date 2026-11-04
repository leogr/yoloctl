package termio

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const ViewFDEnv = "YOLOCTL_SESSION_VIEW_FD"

// ViewPair is private infrastructure, inherited only by the shared CLI child.
// Never infer framed session traffic from an application's PTY output.
func ViewPair() (*os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
		if err := unix.SetNonblock(fd, true); err != nil {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
			return nil, nil, err
		}
	}
	return os.NewFile(uintptr(fds[0]), "session-view"), os.NewFile(uintptr(fds[1]), "session-view-child"), nil
}

func InheritedView() (*os.File, error) {
	value := os.Getenv(ViewFDEnv)
	if value == "" {
		return nil, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1024 {
		return nil, errors.New("invalid session view descriptor")
	}
	return ViewFile(fd)
}

func ViewFile(fd int) (*os.File, error) {
	unix.CloseOnExec(fd) // descendants must not inherit the infrastructure channel
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "session-view-parent")
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		_ = f.Close()
		return nil, errors.New("session view descriptor is not a socket")
	}
	return f, nil
}
