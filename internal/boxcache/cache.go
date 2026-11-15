// Package boxcache manages only controller-derived, per-box cache directories.
// It never collects them automatically or touches a container's writable layer.
package boxcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/sys/unix"
)

const Root = "/var/lib/yoloctl/box-cache"
const Target = "/home/sandbox/.cache"
const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// Parent ownership is fixed in production; tests use their own isolated owner.
var parentUID, parentGID = 0, 0

func Path(name string) (string, error) {
	if err := layout.ValidateName("box", name); err != nil {
		return "", err
	}
	return filepath.Join(Root, name), nil
}

// Ensure creates a private cache leaf, or verifies the retained one. Existing
// ownership, links and modes are never repaired or adopted.
func Ensure(name string, uid, gid int) error { return ensure(Root, name, uid, gid) }

// Remove is called only after an authoritative container removal. Missing cache
// state is harmless; foreign state is retained for inspection.
func Remove(name string, uid, gid int) error { return remove(Root, name, uid, gid) }

func openParent(path string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("invalid cache parent")
	}
	// The controller's existing infrastructure directory is the trusted
	// anchor, as for its other VM-local state. Never traverse the cache leaf
	// or follow either final directory component through a link.
	anchor, err := unix.Open(filepath.Dir(path), directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(anchor) }()
	name := filepath.Base(path)
	fd, err := unix.Openat(anchor, name, directoryFlags, 0)
	if errors.Is(err, unix.ENOENT) && create {
		if err = unix.Mkdirat(anchor, name, 0o755); err == nil {
			fd, err = unix.Openat(anchor, name, directoryFlags, 0)
			if err == nil {
				if e := unix.Fchown(fd, parentUID, parentGID); e != nil {
					_ = unix.Close(fd)
					return nil, e
				}
				if e := unix.Fchmod(fd, 0o755); e != nil {
					_ = unix.Close(fd)
					return nil, e
				}
				if e := unix.Fsync(anchor); e != nil {
					_ = unix.Close(fd)
					return nil, e
				}
			}
		} else if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(anchor, name, directoryFlags, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Uid != uint32(parentUID) || st.Gid != uint32(parentGID) || st.Mode&0o7777 != 0o755 {
		return nil, errors.New("cache parent is not root-owned mode 0755")
	}
	// Root keeps subsequent operations descriptor-relative. Check its identity
	// against the no-follow walk before using it, including after a path rename.
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	f, err := r.Open(".")
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	var actual unix.Stat_t
	err = unix.Fstat(int(f.Fd()), &actual)
	_ = f.Close()
	if err != nil || actual.Dev != st.Dev || actual.Ino != st.Ino {
		_ = r.Close()
		return nil, errors.New("cache parent changed while opening")
	}
	return r, nil
}

func verifyLeaf(r *os.Root, name string, uid, gid int) error {
	if err := layout.ValidateName("box", name); err != nil {
		return err
	}
	info, err := r.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("cache leaf is not a private directory")
	}
	f, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	current, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("cache leaf changed while opening")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7777 != 0o700 {
		return errors.New("cache leaf ownership or mode differs from the space user")
	}
	return nil
}

func ensure(root, name string, uid, gid int) error {
	if _, err := Path(name); err != nil {
		return err
	}
	if uid < 1 || gid < 1 {
		return errors.New("cache leaf requires the positive space UID/GID")
	}
	r, err := openParent(root, true)
	if err != nil {
		return fmt.Errorf("open box cache parent: %w", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Mkdir(name, 0o700); err == nil {
		if err := r.Chown(name, uid, gid); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := verifyLeaf(r, name, uid, gid); err != nil {
		return err
	}
	for _, path := range []string{name, "."} {
		f, err := r.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func remove(root, name string, uid, gid int) error {
	if uid < 1 || gid < 1 {
		return errors.New("cache leaf requires the positive space UID/GID")
	}
	if _, err := Path(name); err != nil {
		return err
	}
	r, err := openParent(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := verifyLeaf(r, name, uid, gid); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := r.RemoveAll(name); err != nil {
		return err
	}
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
