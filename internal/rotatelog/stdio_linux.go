//go:build linux

package rotatelog

import (
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// CaptureStdio is for a dedicated background helper process only. It replaces
// stdout/stderr with one blocking pipe owned by that helper, releasing the
// inherited startup log inode. Finish closes those descriptors. The caller joins
// done after retiring all external producers; an unknown surviving producer
// must not extend lifecycle waits. Finish does not close dst. Producers using
// captured stdout/stderr must emit and flush their records before Finish; the
// relay ends with the process unless the caller can join done.
// It never changes stdin or reads terminal input.
func CaptureStdio(dst io.Writer) (finish func(), done <-chan struct{}, err error) {
	oldOut, err := unix.FcntlInt(os.Stdout.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = unix.Close(oldOut) }()
	oldErr, err := unix.FcntlInt(os.Stderr.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = unix.Close(oldErr) }()
	output, drained, err := Pipe(dst, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = output.Close() }()
	fd := int(output.Fd())
	// The existing os.Stdout/os.Stderr wrappers may describe regular startup
	// files and have no poller for this pipe. Keep their new descriptor blocking.
	if err = unix.SetNonblock(fd, false); err == nil {
		err = unix.Dup2(fd, int(os.Stdout.Fd()))
	}
	if err == nil {
		err = unix.Dup2(fd, int(os.Stderr.Fd()))
	}
	if err != nil {
		err = errors.Join(err, unix.Dup2(oldOut, int(os.Stdout.Fd())), unix.Dup2(oldErr, int(os.Stderr.Fd())))
		_ = output.Close()
		<-drained
		return nil, nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
		})
	}, drained, nil
}
