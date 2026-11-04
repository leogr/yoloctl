package openshell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type runtimeMount struct {
	id       uint64
	point    string
	readOnly bool
}

// verifyRuntimeMounts is a PID1 precondition, not container authorization or
// readiness. Native activation separately authenticates this process and its
// actual selected record inode. Hash/owner checks separately qualify files.
func verifyRuntimeMounts(launch LaunchConfig) error {
	if err := launch.Validate(); err != nil {
		return err
	}
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("runtime mount verification requires the container's root PID 1")
	}
	proc, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(proc) }()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(proc, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return errors.New("runtime mount verification requires genuine procfs")
	}
	// thread-self is an intentional kernel magic link in verified procfs.
	fd, err := unix.Openat(proc, "thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	info := os.NewFile(uintptr(fd), "runtime-mountinfo")
	if unix.Fstatfs(fd, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		_ = info.Close()
		return errors.New("runtime mountinfo is not a genuine procfs file")
	}
	raw, readErr := io.ReadAll(io.LimitReader(info, (4<<20)+1))
	closeErr := info.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	mounts, err := parseRuntimeMounts(raw)
	if err != nil {
		return err
	}
	ids := map[string]uint64{}
	for _, name := range []string{HelperDirectory, ArtifactProjection, NativeProjection, launch.Workdir} {
		open := openRuntimeInfrastructure
		if name == launch.Workdir {
			open = openRuntimeDirectory
		}
		file, err := open(name)
		if err != nil {
			return err
		}
		var st unix.Stat_t
		var sx unix.Statx_t
		err = unix.Fstat(int(file.Fd()), &st)
		if err == nil {
			err = unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx)
		}
		_ = file.Close()
		if err != nil {
			return err
		}
		if sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id == 0 {
			return errors.New("runtime kernel cannot attest the selected mount identity")
		}
		if name != launch.Workdir && (st.Uid != 0 || st.Gid != 0 || st.Mode&0o7022 != 0) {
			return errors.New("runtime infrastructure projection has an untrusted owner or writer")
		}
		if name == NativeProjection && st.Mode&0o7777 != 0o700 {
			return errors.New("native runtime projection is not root-private")
		}
		ids[name] = sx.Mnt_id
	}
	return validateRuntimeMounts(launch, mounts, ids)
}

func openRuntimeDirectory(name string) (*os.File, error) {
	return walkRuntimeDirectory(name, false)
}

// An individually private bind can still be renamed through a writable
// ancestor. Check the entire fixed infrastructure path so no source subtree
// can be moved to an alias which survives the child's reserved-root overmount.
func openRuntimeInfrastructure(name string) (*os.File, error) {
	return walkRuntimeDirectory(name, true)
}

func walkRuntimeDirectory(name string, infrastructure bool) (*os.File, error) {
	if !path.IsAbs(name) || path.Clean(name) != name || name == "/" {
		return nil, errors.New("invalid runtime directory")
	}
	// Only metadata and mount identity are needed. O_PATH retains traversal
	// and no-follow checks without requiring directory-list/read permission.
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	trusted := func(fd int) error {
		if !infrastructure {
			return nil
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if st.Uid != 0 || st.Gid != 0 || st.Mode&0o7022 != 0 {
			return errors.New("runtime infrastructure has an untrusted ancestor owner or writer")
		}
		return nil
	}
	if err := trusted(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		next, err := unix.Openat(fd, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		if err := trusted(fd); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), name), nil
}

func validateRuntimeMounts(launch LaunchConfig, mounts []runtimeMount, selected map[string]uint64) error {
	if err := launch.Validate(); err != nil {
		return err
	}
	want := map[string]bool{HelperDirectory: true, ArtifactProjection: true, NativeProjection: false, launch.Workdir: launch.WorkspaceReadOnly}
	if len(want) != 4 || len(selected) != 4 {
		return errors.New("runtime mount selection overlaps infrastructure")
	}
	for name, readonly := range want {
		id, ok := selected[name]
		if !ok || id == 0 {
			return errors.New("runtime projection lacks its opened mount identity")
		}
		found := false
		for _, mount := range mounts {
			if mount.id == id {
				if found || mount.point != name || mount.readOnly != readonly {
					return fmt.Errorf("runtime mount %s has the wrong identity, target or write mode", name)
				}
				found = true
			}
		}
		if !found {
			return errors.New("opened runtime projection is absent from kernel mountinfo")
		}
	}
	for _, mount := range mounts {
		for _, protected := range []string{HelperDirectory, ArtifactProjection, NativeProjection} {
			if strings.HasPrefix(mount.point, protected+"/") {
				return errors.New("runtime infrastructure contains an unexpected nested mount")
			}
		}
	}
	return nil
}

func parseRuntimeMounts(raw []byte) ([]runtimeMount, error) {
	if len(raw) == 0 || len(raw) > 4<<20 || strings.IndexByte(string(raw), 0) >= 0 {
		return nil, errors.New("missing or unbounded runtime mountinfo")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var mounts []runtimeMount
	seen := map[uint64]bool{}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return nil, errors.New("incomplete runtime mountinfo")
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || id == 0 || seen[id] {
			return nil, errors.New("invalid or repeated runtime mount ID")
		}
		seen[id] = true
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) != separator+4 {
			return nil, errors.New("invalid runtime mountinfo separator")
		}
		point, err := decodeMountPoint(fields[4])
		if err != nil {
			return nil, err
		}
		ro, rw := false, false
		for _, option := range strings.Split(fields[5], ",") {
			ro = ro || option == "ro"
			rw = rw || option == "rw"
		}
		if ro == rw {
			return nil, errors.New("ambiguous runtime mount write mode")
		}
		mounts = append(mounts, runtimeMount{id, point, ro})
		if len(mounts) > 16384 {
			return nil, errors.New("runtime mount inventory exceeds bound")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(mounts) == 0 {
		return nil, errors.New("empty runtime mount inventory")
	}
	return mounts, nil
}

func decodeMountPoint(encoded string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '\\' {
			decoded.WriteByte(encoded[i])
			continue
		}
		if i+3 >= len(encoded) {
			return "", errors.New("truncated mountinfo escape")
		}
		var value byte
		switch encoded[i+1 : i+4] {
		case "040":
			value = ' '
		case "011":
			value = '\t'
		case "012":
			value = '\n'
		case "134":
			value = '\\'
		default:
			return "", errors.New("invalid mountinfo escape")
		}
		decoded.WriteByte(value)
		i += 3
	}
	point := decoded.String()
	if !path.IsAbs(point) || path.Clean(point) != point {
		return "", errors.New("invalid mountinfo target")
	}
	return point, nil
}
