package openshell

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"

	"golang.org/x/sys/unix"
)

// These exact generation-local leaves are mutable infrastructure state, not
// credentials or authority. Neither native projection is mounted in a child;
// only the opened control/run leaves enter their separately private views.
type runtimeShared struct {
	control       *os.File
	supervisorRun *os.File
}

func (s runtimeShared) close() error {
	var err error
	if s.control != nil {
		err = errors.Join(err, s.control.Close())
	}
	if s.supervisorRun != nil {
		err = errors.Join(err, s.supervisorRun.Close())
	}
	return err
}

func prepareRuntimeShared(record RuntimeRecord) (runtimeShared, error) {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return runtimeShared{}, errors.New("runtime shared directories require root PID 1")
	}
	fd, err := openActivationDirectory(NativeProjection, record.Generation)
	if err != nil {
		return runtimeShared{}, err
	}
	status := os.NewFile(uintptr(fd), "private-runtime-status")
	defer func() { _ = status.Close() }()
	return prepareRuntimeSharedAt(status, int(SupervisorUID), int(SupervisorGID))
}

// Only an initial fresh generation may prepare these leaves. Existing paths
// are refused instead of adopted, emptied, or repaired. Failed initialization
// retains exact partial state for the enclosing native lifecycle receipt.
func prepareRuntimeSharedAt(status *os.File, uid, gid int) (runtimeShared, error) {
	control, err := createRuntimeDirectoryAt(status, "runtime-control", 0o711, uid, gid)
	if err != nil {
		return runtimeShared{}, err
	}
	shared := runtimeShared{control: control}
	run, err := createRuntimeDirectoryAt(status, "runtime-supervisor", 0o700, uid, gid)
	if err != nil {
		_ = shared.close()
		return runtimeShared{}, err
	}
	shared.supervisorRun = run
	for _, name := range []string{"tmp", "ssh", "proxy-tls"} {
		dir, err := createRuntimeDirectoryAt(run, name, 0o700, uid, gid)
		if err != nil {
			_ = shared.close()
			return runtimeShared{}, err
		}
		if err := dir.Close(); err != nil {
			_ = shared.close()
			return runtimeShared{}, err
		}
	}
	if err := status.Sync(); err != nil {
		_ = shared.close()
		return runtimeShared{}, err
	}
	return shared, nil
}

func createRuntimeDirectoryAt(parent *os.File, name string, mode uint32, uid, gid int) (*os.File, error) {
	if name == "" || path.Base(name) != name || name == "." || name == ".." || (mode != 0o700 && mode != 0o711) {
		return nil, errors.New("invalid private runtime directory construction")
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	if uid != os.Geteuid() || gid != os.Getegid() {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return fail(err)
		}
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if err := parent.Sync(); err != nil {
		return fail(err)
	}
	return file, nil
}

// prepareRuntimeChildView is a terminal root-child transition. The thread is
// never unlocked: every caller must exec or exit after success or failure.
// Source descriptors must already be independently verified and be closed
// before privilege handoff. This function is never invoked by filesystem tests.
func prepareRuntimeChildView(payload runtimeChildPayload, shared runtimeShared, artifacts *os.File) error {
	if os.Getpid() == 1 || os.Getppid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("private runtime view requires a root child of PID 1")
	}
	if _, err := runtimeChildFiles(payload.Role); err != nil {
		return err
	}
	if shared.control == nil || artifacts == nil || (payload.Role == runtimeSupervisor && shared.supervisorRun == nil) {
		return errors.New("private runtime view lacks its checked source descriptors")
	}
	if err := verifyRuntimeChildSources(payload, shared, artifacts); err != nil {
		return err
	}
	runtime.LockOSThread()
	// Inherited descriptors still reference mounts in PID 1's namespace after
	// unshare. Clone their exact already-verified paths while we are still in
	// that namespace; owned detached mounts can then be attached in the child.
	artifactView, err := cloneRuntimeView(artifacts)
	if err != nil {
		return err
	}
	defer func() { _ = artifactView.Close() }()
	controlView, err := cloneRuntimeView(shared.control)
	if err != nil {
		return err
	}
	defer func() { _ = controlView.Close() }()
	var supervisorView *os.File
	if payload.Role == runtimeSupervisor {
		supervisorView, err = cloneRuntimeView(shared.supervisorRun)
		if err != nil {
			return err
		}
		defer func() { _ = supervisorView.Close() }()
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("unshare mount namespace: %w", err)
	}
	// Propagation is made private in the new namespace before any mount. No
	// mutation can reach the parent helper, another child, the VM or host.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mount propagation private: %w", err)
	}
	privateRoot, err := openRuntimeInfrastructure("/.openshell")
	if err != nil {
		return err
	}
	_ = privateRoot.Close()
	if err := unix.Mount("yoloctl-openshell-child", "/.openshell", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0711,size=16m"); err != nil {
		return fmt.Errorf("mount private child tmpfs: %w", err)
	}
	// Overmount the entire reserved root. No native origin/generation/status,
	// issuer, activation key or other child's private configuration survives.
	root, err := os.Open("/.openshell")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := createViewPath(root, "runtime", 0o700, 0, 0); err != nil {
		return err
	}
	if err := attachRuntimeView(artifactView, ArtifactProjection, true, false); err != nil {
		return err
	}
	for _, name := range []string{"channel", "channel/control"} {
		if err := createViewPath(root, name, 0o711, 0, 0); err != nil {
			return err
		}
	}
	if err := createViewPath(root, "channel/control/supervisor", 0o700, 0, 0); err != nil {
		return err
	}
	if err := attachRuntimeView(controlView, path.Dir(controlSocket), payload.Role == runtimeBoundary, true); err != nil {
		return err
	}
	if payload.Role == runtimeSupervisor {
		if err := createViewPath(root, "supervisor", 0o711, 0, 0); err != nil {
			return err
		}
		if err := createViewPath(root, "supervisor/config", 0o700, int(SupervisorUID), int(SupervisorGID)); err != nil {
			return err
		}
		if err := createViewPath(root, "supervisor/run", 0o700, 0, 0); err != nil {
			return err
		}
		if err := attachRuntimeView(supervisorView, "/.openshell/supervisor/run", false, true); err != nil {
			return err
		}
		return writeRuntimeViewFiles(root, payload, "supervisor", "supervisor/config", int(SupervisorUID), int(SupervisorGID), 0o400)
	}
	if err := createViewPath(root, "channel/sandbox", 0o700, int(payload.Record.Launch.UID), int(payload.Record.Launch.GID)); err != nil {
		return err
	}
	if err := prepareBoundaryCAView(root, payload.Record.Launch.UID, payload.Record.Launch.GID); err != nil {
		return err
	}
	return writeRuntimeViewFiles(root, payload, "boundary", "channel/sandbox", int(payload.Record.Launch.UID), int(payload.Record.Launch.GID), 0o600)
}

const boundaryCARoot = "/run/openshell-supervisor-ca"

// Upstream StartAgent installs public CA material in the boundary's workload
// identity, not the supervisor identity. Keep only this fixed leaf writable in
// its private mount view; neither /run nor supervisor private keys are shared.
func prepareBoundaryCAView(root *os.File, uid, gid uint32) error {
	source, err := createBoundaryCAStorage(root, int(uid), int(gid))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	run, err := openRuntimeInfrastructure("/run")
	if err != nil {
		return err
	}
	defer func() { _ = run.Close() }()
	if err := ensureBoundaryCAMountpoint(run, 0, 0); err != nil {
		return err
	}
	return bindRuntimeView(source, boundaryCARoot, false, true)
}

func createBoundaryCAStorage(root *os.File, uid, gid int) (*os.File, error) {
	if err := createViewPath(root, "boundary-ca", 0o755, uid, gid); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), "boundary-ca", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "private-boundary-public-ca"), nil
}

func ensureBoundaryCAMountpoint(run *os.File, uid, gid uint32) error {
	const leaf = "openshell-supervisor-ca"
	if err := unix.Mkdirat(int(run.Fd()), leaf, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(run.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uid || st.Gid != gid || st.Mode&0o7022 != 0 {
		return errors.New("boundary CA mountpoint is not an owned protected directory")
	}
	return nil
}

func verifyRuntimeChildSources(payload runtimeChildPayload, shared runtimeShared, artifacts *os.File) error {
	status, err := openActivationDirectory(NativeProjection, payload.Record.Generation)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(status) }()
	for _, source := range []struct {
		name string
		file *os.File
		mode uint32
	}{
		{"runtime-control", shared.control, 0o711}, {"runtime-supervisor", shared.supervisorRun, 0o700},
	} {
		if source.file == nil {
			if source.name == "runtime-supervisor" && payload.Role == runtimeBoundary {
				continue
			}
			return errors.New("missing private runtime source descriptor")
		}
		var selected, actual unix.Stat_t
		if unix.Fstatat(status, source.name, &selected, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(source.file.Fd()), &actual) != nil || selected.Mode&unix.S_IFMT != unix.S_IFDIR || selected.Mode&0o7777 != source.mode || selected.Uid != SupervisorUID || selected.Gid != SupervisorGID || selected.Dev != actual.Dev || selected.Ino != actual.Ino {
			return errors.New("private runtime source descriptor differs from its selected generation")
		}
	}
	projected, err := openRuntimeInfrastructure(ArtifactProjection)
	if err != nil {
		return err
	}
	defer func() { _ = projected.Close() }()
	var expected, actual unix.Stat_t
	if unix.Fstat(int(projected.Fd()), &expected) != nil || unix.Fstat(int(artifacts.Fd()), &actual) != nil || expected.Dev != actual.Dev || expected.Ino != actual.Ino || actual.Mode&unix.S_IFMT != unix.S_IFDIR || actual.Mode&0o7777 != 0o555 || actual.Uid != 0 || actual.Gid != 0 {
		return errors.New("private runtime artifacts descriptor differs from the qualified projection")
	}
	return nil
}

// createViewPath operates only inside our freshly mounted empty tmpfs, never
// an image/workdir/native-state path. Its caller fixes every member name.
func createViewPath(root *os.File, name string, mode uint32, uid, gid int) error {
	parent := path.Dir(name)
	dir := root
	if parent != "." {
		fd, err := unix.Openat(int(root.Fd()), parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		dir = os.NewFile(uintptr(fd), parent)
		defer func() { _ = dir.Close() }()
	}
	if err := unix.Mkdirat(int(dir.Fd()), path.Base(name), 0o700); err != nil {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), path.Base(name), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return err
	}
	return unix.Fchmod(fd, mode)
}

func bindRuntimeView(source *os.File, target string, readOnly, noExec bool) error {
	if !path.IsAbs(target) || path.Clean(target) != target || source == nil {
		return errors.New("invalid runtime view bind")
	}
	alias := fmt.Sprintf("/proc/thread-self/fd/%d", source.Fd())
	if err := unix.Mount(alias, target, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind private view %s: %w", target, err)
	}
	return protectRuntimeView(target, readOnly, noExec)
}

// cloneRuntimeView never looks up a path or recursively copies nested mounts.
// Closing an unattached result discards only this owned detached mount.
func cloneRuntimeView(source *os.File) (*os.File, error) {
	if source == nil {
		return nil, errors.New("missing runtime mount source")
	}
	fd, err := unix.OpenTree(int(source.Fd()), "", unix.AT_EMPTY_PATH|unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("clone private view source: %w", err)
	}
	return os.NewFile(uintptr(fd), "private-runtime-mount"), nil
}

func attachRuntimeView(mount *os.File, target string, readOnly, noExec bool) error {
	if mount == nil || !path.IsAbs(target) || path.Clean(target) != target {
		return errors.New("invalid detached runtime view")
	}
	if err := unix.MoveMount(int(mount.Fd()), "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach private view %s: %w", target, err)
	}
	return protectRuntimeView(target, readOnly, noExec)
}

func protectRuntimeView(target string, readOnly, noExec bool) error {
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_NOSUID | unix.MS_NODEV)
	if readOnly {
		flags |= unix.MS_RDONLY
	}
	if noExec {
		flags |= unix.MS_NOEXEC
	}
	if err := unix.Mount("", target, "", flags, ""); err != nil {
		return fmt.Errorf("remount private view %s: %w", target, err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(target, &stat); err != nil {
		return fmt.Errorf("verify private view %s: %w", target, err)
	}
	if (stat.Flags&unix.ST_RDONLY != 0) != readOnly || stat.Flags&unix.ST_NOSUID == 0 || stat.Flags&unix.ST_NODEV == 0 || (stat.Flags&unix.ST_NOEXEC != 0) != noExec {
		return errors.New("runtime view bind flags differ from required isolation")
	}
	return nil
}

func writeRuntimeViewFiles(root *os.File, payload runtimeChildPayload, source, target string, uid, gid int, mode uint32) error {
	names, err := runtimeChildFiles(payload.Role)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(root.Fd()), target, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), target)
	defer func() { _ = dir.Close() }()
	for _, name := range names {
		if path.Dir(name) != source {
			return errors.New("private view attempted to copy another child's authority")
		}
		body, ok := payload.Files[name]
		if !ok || sha256Hex(body) != payload.Record.Files[name] {
			return errors.New("private view source changed after verification")
		}
		member, err := unix.Openat(fd, path.Base(name), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(member), path.Base(name))
		if err := unix.Fchown(member, uid, gid); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return err
		}
		if err := unix.Fchmod(member, mode); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}
