package controller

import (
	"os"

	"golang.org/x/sys/unix"
)

// OpenWorkerLog claims the worker's structured log pipe. It accepts only the
// appended write end whose identity the controller recorded in the private
// input, and makes it close-on-exec before the worker starts any child, so
// tools never inherit it and EOF arrives when the worker exits. A missing or
// mismatched descriptor is left untouched and logging falls back to stderr.
func OpenWorkerLog(work WorkerInput) *os.File {
	if work.LogFD <= 4 || work.LogPipe == nil {
		return nil
	}
	actual, ok := nativePhasePipeIdentity(work.LogFD)
	if !ok || *actual != *work.LogPipe {
		return nil
	}
	flags, err := unix.FcntlInt(uintptr(work.LogFD), unix.F_GETFD, 0)
	if err == nil {
		_, err = unix.FcntlInt(uintptr(work.LogFD), unix.F_SETFD, flags|unix.FD_CLOEXEC)
	}
	if err != nil {
		_ = unix.Close(work.LogFD)
		return nil
	}
	return os.NewFile(uintptr(work.LogFD), "worker-log")
}
