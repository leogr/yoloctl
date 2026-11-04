package observe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

// rootOptions caps bytes and inodes, so no sandbox process can exhaust the
// VM's shared /run through its writable generation directory.
var rootOptions = fmt.Sprintf("size=%d,nr_inodes=%d,mode=0755,uid=0,gid=0", RootBytes, RootInodes)

// The expected owner is replaceable so tests can run unprivileged; the mount
// itself and the filesystem check live in root_linux.go (host builds compile
// stubs, since only the Linux VM controller manages these roots).
var rootUID, rootGID = 0, 0

// EnsureRoot mounts a box's capped tmpfs at dir, or verifies the one already
// there. The applier calls it before creating or starting a container that
// binds it, including after a VM reboot emptied /run. dir comes from
// layout.AgentObserveDir; its parents are created root-private.
func EnsureRoot(dir string) error {
	ok, err := mounted(dir)
	if err != nil {
		return err
	}
	if !ok {
		parent := filepath.Dir(dir)
		rel := filepath.Join(filepath.Base(parent), filepath.Base(dir))
		if err := safepath.PrivateDirectory(filepath.Dir(parent), rel, os.Geteuid(), os.Getegid()); err != nil {
			return fmt.Errorf("prepare agent observation mount point: %w", err)
		}
		if err := mountTmpfs(dir); err != nil {
			return fmt.Errorf("mount agent observation tmpfs at %s: %w", dir, err)
		}
		if ok, err = mounted(dir); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("agent observation tmpfs is not mounted at %s", dir)
		}
	}
	fd, err := openRoot(dir)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// RemoveRoot unmounts and removes a box's observation tmpfs once its
// container is gone. A missing root is not an error; a foreign mount at dir
// is refused rather than unmounted.
func RemoveRoot(dir string) error {
	ok, err := mounted(dir)
	if err != nil {
		return err
	}
	if ok {
		if err := unmountRoot(dir); err != nil {
			return fmt.Errorf("unmount agent observation tmpfs at %s: %w", dir, err)
		}
	}
	parent, err := unix.Open(filepath.Dir(dir), dirFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	return removeAt(parent, filepath.Base(dir), 0)
}

// openRoot opens a verified root: the capped tmpfs mounted at dir,
// root-owned, mode 0755. Every failure is ErrRootUnverified.
func openRoot(dir string) (int, error) {
	if ok, err := mounted(dir); err != nil {
		return -1, unverified("%v", err)
	} else if !ok {
		return -1, unverified("no capped tmpfs is mounted at %s", dir)
	}
	fd, err := unix.Open(dir, dirFlags, 0)
	if err != nil {
		return -1, unverified("open %s: %v", dir, err)
	}
	var st unix.Stat_t
	switch {
	case !observationFS(fd):
		err = unverified("%s is not the observation tmpfs", dir)
	case unix.Fstat(fd, &st) != nil || st.Uid != uint32(rootUID) || st.Gid != uint32(rootGID) || st.Mode&0o7777 != 0o755:
		err = unverified("%s is not root-owned with mode 0755", dir)
	}
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// mounted reports whether dir's top mount is the capped tmpfs. Any other
// mount there is an error: it is never adopted or unmounted.
func mounted(dir string) (bool, error) {
	raw, err := mountInfo()
	if err != nil {
		return false, err
	}
	var fstype, perMount, super string
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		sep := slices.Index(fields, "-")
		if sep < 6 || len(fields) < sep+4 || unescapeMountPath(fields[4]) != dir {
			continue
		}
		// Later lines are mounted on top of earlier ones at the same point.
		found, fstype, perMount, super = true, fields[sep+1], fields[5], fields[sep+3]
	}
	if !found {
		return false, nil
	}
	want := []string{fmt.Sprintf("size=%dk", RootBytes>>10), "nr_inodes=" + strconv.Itoa(RootInodes), "mode=755"}
	options := strings.Split(super, ",")
	flags := strings.Split(perMount, ",")
	if fstype != "tmpfs" || !containsAll(options, want) || !containsAll(flags, []string{"nosuid", "nodev", "noexec"}) {
		return false, fmt.Errorf("%s holds a %s mount (%s; %s) that is not the agent observation tmpfs", dir, fstype, perMount, super)
	}
	return true, nil
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// unescapeMountPath decodes mountinfo's octal escapes (\040 and friends).
func unescapeMountPath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// readNames lists an open directory's entries, failing beyond limit.
func readNames(fd, limit int) ([]string, error) {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "")
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(limit + 1)
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(names) > limit {
		return nil, fmt.Errorf("more than %d directory entries", limit)
	}
	return names, nil
}

// removeAt removes name below parent without following links. The tmpfs
// inode cap bounds both the depth and the width a sandbox process can build.
func removeAt(parent int, name string, depth int) error {
	if depth > RootInodes {
		return errors.New("directory tree deeper than the observation inode cap")
	}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return ignoreMissing(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ignoreMissing(unix.Unlinkat(parent, name, 0))
	}
	fd, err := unix.Openat(parent, name, dirFlags, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		// Replaced by a link or file since the stat: unlink that instead.
		return ignoreMissing(unix.Unlinkat(parent, name, 0))
	}
	if err != nil {
		return ignoreMissing(err)
	}
	names, err := readNames(fd, RootInodes)
	for _, child := range names {
		if err = removeAt(fd, child, depth+1); err != nil {
			break
		}
	}
	_ = unix.Close(fd)
	if err != nil {
		return err
	}
	return ignoreMissing(unix.Unlinkat(parent, name, unix.AT_REMOVEDIR))
}

func ignoreMissing(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
