package openshell

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const runtimePortFloor = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

// prepareBoundaryNetwork runs in the private root child before capability-free
// handoff. The parent supplies the authenticated activation namespace identity.
// Unconfined mode keeps only that already filtered namespace; confined mode
// creates a fresh namespace and admits exactly its loopback interface.
func prepareBoundaryNetwork(unconfined bool, containerNamespace namespaceIdentity) error {
	if os.Getpid() == 1 || os.Getppid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("boundary network preparation requires a root PID 1 child")
	}
	runtime.LockOSThread()
	current, err := currentRuntimeNetwork()
	if err != nil || current != containerNamespace || current.Inode == 0 {
		return errors.New("boundary did not inherit the authenticated container namespace")
	}
	if !unconfined {
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			return err
		}
		fresh, err := currentRuntimeNetwork()
		if err != nil || fresh == containerNamespace {
			return errors.New("confined boundary did not enter a fresh network namespace")
		}
		if err := bringRuntimeLoopbackUp(); err != nil {
			return err
		}
	}
	return prepareRuntimePortFloor()
}

func currentRuntimeNetwork() (namespaceIdentity, error) {
	fd, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return namespaceIdentity{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	return networkNamespaceIdentity(fd)
}

func bringRuntimeLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	if request.Uint16()&unix.IFF_LOOPBACK == 0 {
		return errors.New("fresh boundary interface is not loopback")
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request); err != nil {
		return err
	}
	raw, err := syscall.NetlinkRIB(syscall.RTM_GETLINK, syscall.AF_UNSPEC)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("unbounded confined interface inventory")
	}
	messages, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return err
	}
	return verifyRuntimeLoopbackOnly(messages)
}

func verifyRuntimeLoopbackOnly(messages []syscall.NetlinkMessage) error {
	links := 0
	for _, message := range messages {
		if message.Header.Type == syscall.NLMSG_DONE {
			continue
		}
		if message.Header.Type != syscall.RTM_NEWLINK || len(message.Data) < syscall.SizeofIfInfomsg {
			return errors.New("incomplete confined interface inventory")
		}
		links++
		index := int32(binary.NativeEndian.Uint32(message.Data[4:8]))
		flags := binary.NativeEndian.Uint32(message.Data[8:12])
		if links != 1 || index <= 0 || flags&unix.IFF_LOOPBACK == 0 || flags&unix.IFF_UP == 0 {
			return errors.New("confined namespace contains a non-loopback or inactive interface")
		}
		attributes, err := routeAttributes(message.Data[syscall.SizeofIfInfomsg:])
		if err != nil {
			return err
		}
		found := false
		for _, attribute := range attributes {
			if attribute.kind != syscall.IFLA_IFNAME {
				continue
			}
			if found || string(attribute.data) != "lo\x00" {
				return errors.New("confined namespace interface is not exactly lo")
			}
			found = true
		}
		if !found {
			return errors.New("confined namespace lacks its interface name")
		}
	}
	if links != 1 {
		return errors.New("confined namespace lacks its single loopback interface")
	}
	return nil
}

// The OCI proc/sys mount stays read-only. Only this namespaced scalar receives
// a temporary bind with writes enabled in the child's private mount namespace;
// it is restored read-only and verified before any capability-free process.
func prepareRuntimePortFloor() error {
	parent, err := openRuntimeDirectory("/proc/sys/net/ipv4")
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	fd, err := unix.Openat(int(parent.Fd()), "ip_unprivileged_port_start", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "private-netns-port-floor")
	defer func() { _ = file.Close() }()
	var filesystem unix.Statfs_t
	var metadata unix.Stat_t
	if unix.Fstatfs(fd, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC || unix.Fstat(fd, &metadata) != nil || metadata.Mode&unix.S_IFMT != unix.S_IFREG || metadata.Uid != 0 || metadata.Gid != 0 {
		return errors.New("boundary port floor is not the kernel procfs scalar")
	}
	// The source FD is opened after entering the selected namespace. Never
	// borrow a proc/sys FD opened in PID 1's or the VM's network namespace.
	if err := bindRuntimeView(file, runtimePortFloor, false, true); err != nil {
		return err
	}
	fd, err = unix.Open(runtimePortFloor, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	writer := os.NewFile(uintptr(fd), "private-netns-port-floor-write")
	_, writeErr := writer.WriteString("0\n")
	if err := errors.Join(writeErr, writer.Close()); err != nil {
		return err
	}
	if err := unix.Mount("", runtimePortFloor, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	if err := unix.Statfs(runtimePortFloor, &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC || filesystem.Flags&unix.ST_RDONLY == 0 {
		return errors.New("boundary port floor did not return to read-only procfs")
	}
	fd, err = unix.Open(runtimePortFloor, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(fd), "private-netns-port-floor-check")
	raw, readErr := io.ReadAll(io.LimitReader(reader, 32))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "0" {
		return errors.New("boundary unprivileged port floor differs from zero")
	}
	return nil
}
