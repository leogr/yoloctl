package rotatelog

import (
	"errors"
	"fmt"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

// OpenStartupFile opens a private inherited startup sink. It is only for the
// interval before a detached helper installs CaptureStdio; retaining it for
// normal output would bypass rotation. Identity/privacy checks match appends.
func OpenStartupFile(path string) (*os.File, error) {
	w, err := Open(path, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	defer func() { _ = w.Close() }()
	for range Attempts {
		fd, err := unix.Openat(w.dir, w.name, unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), path)
		retry, err := w.app.AppendLocked(f, fd, w.dir, w.name, path, nil)
		unlock := unix.Flock(fd, unix.LOCK_UN)
		if err != nil || unlock != nil {
			_ = f.Close()
			return nil, errors.Join(err, unlock)
		}
		if !retry {
			return f, nil
		}
		_ = f.Close()
	}
	return nil, fmt.Errorf("startup log %q kept changing", path)
}
