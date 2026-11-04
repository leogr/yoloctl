package controller

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// WorkerPIDNamespace owns one original inherited observation descriptor.
// Its existence/type is NOT VM admission, a parent/receipt grant or a complete
// process view. Actual SDK use needs the live original parent checkpoint and
// callback-scoped active-thread comparison; no SDK caller is installed here.
type WorkerPIDNamespace struct {
	mu     sync.Mutex
	file   *os.File
	closed bool
	err    error
}

func workerPIDNamespaceType(fd int) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || uint64(fs.Type) != uint64(unix.NSFS_MAGIC) {
		return errors.Join(err, errors.New("worker namespace is not a genuine NSFS descriptor"))
	}
	kind, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWPID {
		return errors.Join(err, errors.New("worker namespace is not a typed PID namespace"))
	}
	return nil
}

func validateWorkerPIDNamespace(file *os.File) error {
	if file == nil {
		return errors.New("worker namespace descriptor unavailable")
	}
	if err := workerPIDNamespaceType(int(file.Fd())); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.Join(err, errors.New("parent namespace duplicate is not close-on-exec"))
	}
	return nil
}

// ClaimWorkerPIDNamespace is called immediately after decoding the ORIGINAL
// private WorkerInput, before dependency/workload execution. It never guesses
// FD5 or wraps/closes an unrecognized runtime/view/checkpoint descriptor.
// The bounded dynamic index is transported only by RunWorker's private payload.
// Failed raw eligibility leaves that unrelated descriptor untouched.
func ClaimWorkerPIDNamespace(work WorkerInput) (*WorkerPIDNamespace, error) {
	fd := work.PIDNamespaceFD
	if fd == 0 {
		return nil, nil
	}
	if fd < 5 || fd > 64 {
		return nil, errors.New("invalid private worker namespace descriptor index")
	}
	if err := workerPIDNamespaceType(fd); err != nil {
		return nil, err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC != 0 {
		return nil, errors.Join(err, errors.New("worker namespace is not an original inherited descriptor"))
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original private worker PID namespace")
	if err := validateWorkerPIDNamespace(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &WorkerPIDNamespace{file: file}, nil
}

func (n *WorkerPIDNamespace) Close() error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return n.err
	}
	n.closed = true
	if n.file != nil {
		n.err = n.file.Close()
		n.file = nil
	}
	return n.err
}
