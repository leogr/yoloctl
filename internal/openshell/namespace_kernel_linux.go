package openshell

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// kernelInfrastructure holds current kernel observations, not admission,
// storage, container ownership or an effect ACK. The mount namespace is pinned
// independently through VM PID 1, so an eventual bind in this namespace is not
// confined to an exiting worker's private mount namespace. No setns is done.
// It is usable only on the locked thread which opened it, within the callback.
type kernelInfrastructure struct {
	proc, init, mount, network *os.File
	bootID                     string
	mountID, networkID         namespaceIdentity
	tid                        int
}

func (k *kernelInfrastructure) close() error {
	var result error
	for _, file := range []*os.File{k.network, k.mount, k.init, k.proc} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	k.network, k.mount, k.init, k.proc = nil, nil, nil, nil
	return result
}

// withNativeKernelInfrastructure must run under the caller's current native
// mutation admission and verified disk identity. This private entry rejects
// container/host CLI use; process-local context alone is not disk verification.
// The callback must keep the calling thread in these namespaces. New pin/store
// effects and cold cleanup still need their separate ownership/ACK protocol.
func withNativeKernelInfrastructure(ctx context.Context, use func(*kernelInfrastructure) error) (err error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 || use == nil {
		return errors.New("namespace infrastructure requires admitted native VM-root execution")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	k, err := openKernelInfrastructure()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, k.close()) }()
	if err := k.revalidate(ctx); err != nil {
		return err
	}
	return use(k)
}

// openKernelInfrastructure requires a locked OS thread. All names are fixed;
// there is no caller-selected proc root, boot file, PID or namespace path.
func openKernelInfrastructure() (_ *kernelInfrastructure, err error) {
	k := &kernelInfrastructure{tid: unix.Gettid()}
	defer func() {
		if err != nil {
			_ = k.close()
		}
	}()
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	k.proc = os.NewFile(uintptr(fd), "namespace-proc")
	if err := verifyKernelProcFD(fd, true); err != nil {
		return nil, err
	}
	k.bootID, err = readKernelBootID(fd)
	if err != nil {
		return nil, err
	}
	init, err := unix.Openat(fd, "1", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	k.init = os.NewFile(uintptr(init), "namespace-vm-init")
	if err := verifyKernelProcFD(init, true); err != nil {
		return nil, err
	}
	k.mount, k.mountID, err = openKernelNamespace(init, "mnt", unix.CLONE_NEWNS)
	if err != nil {
		return nil, err
	}
	k.network, k.networkID, err = openKernelNamespace(init, "net", unix.CLONE_NEWNET)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// revalidate requires both the actual calling thread and current PID 1 to
// match the pinned VM namespaces. A thread-private namespace is refused rather
// than silently entered or treating an inode from a disk record as a handle.
func (k *kernelInfrastructure) revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || k.proc == nil || k.init == nil || k.mount == nil || k.network == nil || unix.Gettid() != k.tid {
		return errors.New("namespace infrastructure handle is closed or used on another thread")
	}
	boot, err := readKernelBootID(int(k.proc.Fd()))
	if err != nil || boot != k.bootID {
		return errors.New("namespace infrastructure kernel boot identity changed or is unavailable")
	}
	// thread-self is the intentional kernel magic link under verified procfs.
	fd, err := unix.Openat(int(k.proc.Fd()), "thread-self", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := verifyKernelProcFD(fd, true); err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		kind int
		want namespaceIdentity
	}{{"mnt", unix.CLONE_NEWNS, k.mountID}, {"net", unix.CLONE_NEWNET, k.networkID}} {
		for _, process := range []int{int(k.init.Fd()), fd} {
			file, actual, err := openKernelNamespace(process, item.name, item.kind)
			if err != nil {
				return err
			}
			closeErr := file.Close()
			if closeErr != nil || actual != item.want {
				return errors.New("namespace infrastructure must remain in VM PID 1's mount and network namespaces")
			}
		}
	}
	return nil
}

func verifyKernelProcFD(fd int, directory bool) error {
	var fs unix.Statfs_t
	var st unix.Stat_t
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC || unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != kind || st.Uid != 0 || st.Gid != 0 || st.Mode&0o022 != 0 {
		return errors.New("namespace infrastructure requires protected kernel procfs objects")
	}
	return nil
}

func readKernelBootID(proc int) (string, error) {
	if err := verifyKernelProcFD(proc, true); err != nil {
		return "", err
	}
	parent, err := unix.FcntlInt(uintptr(proc), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(parent) }()
	for _, name := range []string{"sys", "kernel", "random"} {
		next, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return "", err
		}
		_ = unix.Close(parent)
		parent = next
		if err := verifyKernelProcFD(parent, true); err != nil {
			return "", err
		}
	}
	fd, err := unix.Openat(parent, "boot_id", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), "namespace-kernel-boot-id")
	defer func() { _ = file.Close() }()
	if err := verifyKernelProcFD(fd, false); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 38))
	if err != nil {
		return "", err
	}
	return decodeKernelBootID(raw)
}

func decodeKernelBootID(raw []byte) (string, error) {
	if len(raw) != 37 || raw[36] != '\n' || !canonicalUUID(string(raw[:36])) {
		return "", errors.New("kernel boot identity is not one canonical UUID line")
	}
	return string(raw[:36]), nil
}

func openKernelNamespace(process int, name string, kind int) (*os.File, namespaceIdentity, error) {
	if (name != "mnt" || kind != unix.CLONE_NEWNS) && (name != "net" || kind != unix.CLONE_NEWNET) {
		return nil, namespaceIdentity{}, errors.New("unsupported infrastructure namespace selection")
	}
	if err := verifyKernelProcFD(process, true); err != nil {
		return nil, namespaceIdentity{}, err
	}
	ns, err := unix.Openat(process, "ns", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, namespaceIdentity{}, err
	}
	defer func() { _ = unix.Close(ns) }()
	if err := verifyKernelProcFD(ns, true); err != nil {
		return nil, namespaceIdentity{}, err
	}
	// Only these fixed magic links are followed, after authenticating procfs.
	fd, err := unix.Openat(ns, name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, namespaceIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), "namespace-"+name)
	identity, err := kernelNamespaceIdentity(fd, kind)
	if err != nil {
		_ = file.Close()
		return nil, namespaceIdentity{}, err
	}
	return file, identity, nil
}

func kernelNamespaceIdentity(fd, kind int) (namespaceIdentity, error) {
	if kind != unix.CLONE_NEWNS && kind != unix.CLONE_NEWNET {
		return namespaceIdentity{}, errors.New("unsupported kernel namespace type")
	}
	var fs unix.Statfs_t
	var st unix.Stat_t
	// NS_GET_NSTYPE returns the kernel namespace operations' clone flag.
	actual, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xb703, 0)
	if errno != 0 || actual != uintptr(kind) || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.NSFS_MAGIC || unix.Fstat(fd, &st) != nil || st.Dev == 0 || st.Ino == 0 {
		return namespaceIdentity{}, errors.New("descriptor is not the selected kernel namespace type")
	}
	return namespaceIdentity{Device: uint64(st.Dev), Inode: st.Ino}, nil
}
