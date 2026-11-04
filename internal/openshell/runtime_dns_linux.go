package openshell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const confinedResolver = "nameserver 127.0.0.53\nsearch .\n"

// Unconfined mode retains the container's real resolver configuration. Its
// contents confer no nft exception: native activation alone selects the exact
// trusted resolver addresses. This also retains explicit public/local resolver
// choices, subject to the already accepted destination restrictions.
//
// Confined mode gives the boundary's private view the upstream mediated relay.
// The supervisor keeps the original container resolver in its separate view.
func prepareRuntimeDNS(launch LaunchConfig) error {
	if launch.UnconfinedNetwork {
		return nil
	}
	if os.Getpid() == 1 || os.Getppid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("runtime DNS preparation requires a private root child")
	}
	root, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(root) }()
	// Ordinary image symlinks are resolved inside this container root. Kernel
	// magic links are not accepted; no resolution can escape through procfs.
	fd, err := unix.Openat2(root, "etc/resolv.conf", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var metadata unix.Stat_t
	if unix.Fstat(fd, &metadata) != nil || metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("runtime resolver target is not a regular container file")
	}
	resolved, err := os.Readlink(fmt.Sprintf("/proc/thread-self/fd/%d", fd))
	if err != nil {
		return err
	}
	if err := validateRuntimeResolverTarget(resolved, launch.Workdir); err != nil {
		return err
	}
	private, err := openRuntimeInfrastructure("/.openshell")
	if err != nil {
		return err
	}
	defer func() { _ = private.Close() }()
	fileFD, err := unix.Openat(int(private.Fd()), "resolv.conf", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	source := os.NewFile(uintptr(fileFD), "private-boundary-resolver")
	defer func() { _ = source.Close() }()
	if _, err := source.WriteString(confinedResolver); err != nil {
		return err
	}
	if err := source.Chmod(0o444); err != nil {
		return err
	}
	if err := bindRuntimeView(source, resolved, true, true); err != nil {
		return err
	}
	// Verify through the pathname the workload will use, after binding. A
	// changed image link or target cannot count as successful configuration.
	check, err := unix.Openat2(root, "etc/resolv.conf", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(check), "private-boundary-resolver-check")
	raw, readErr := io.ReadAll(io.LimitReader(reader, int64(len(confinedResolver))+1))
	var selected unix.Stat_t
	statErr := unix.Fstat(check, &selected)
	if err := errors.Join(readErr, statErr, reader.Close()); err != nil {
		return err
	}
	if unix.Fstat(fileFD, &metadata) != nil || metadata.Dev != selected.Dev || metadata.Ino != selected.Ino || !bytes.Equal(raw, []byte(confinedResolver)) {
		return errors.New("boundary resolver view differs from the mediated configuration")
	}
	return nil
}

func validateRuntimeResolverTarget(target, workdir string) error {
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" || strings.ContainsAny(target, "\x00\r\n") {
		return errors.New("invalid container resolver target")
	}
	for _, protected := range []string{workdir, "/.openshell", "/home/sandbox", "/opt/yoloctl", "/proc", "/sys", "/dev", "/run/yoloctl", "/run/openshell"} {
		if protected == "" || target == protected || strings.HasPrefix(target, protected+"/") || strings.HasPrefix(protected, target+"/") {
			return errors.New("container resolver aliases a protected runtime, account or workdir path")
		}
	}
	return nil
}
