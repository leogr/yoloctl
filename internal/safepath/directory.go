package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"golang.org/x/sys/unix"
)

// VerifyPrivateDirectory verifies an existing directory without creating or
// repairing anything, including when an intermediate component is missing.
func VerifyPrivateDirectory(root, rel string, uid, gid int) error {
	parent, base, cleanup, err := openParent(root, rel, false)
	if err != nil {
		return err
	}
	defer cleanup()
	fd, err := unix.Openat(parent, base, directoryFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o777 != 0o700 {
		return errors.New("directory is not private to its expected owner")
	}
	return nil
}

// PrivateDirectory creates/verifies one directory below a trusted root. Every
// parent is opened without following links. Existing ownership and permissions
// are checked, never repaired or adopted. Newly created parents belong to the
// caller; only the leaf uses uid/gid. The caller serializes creation.
func PrivateDirectory(root, rel string, uid, gid int) error {
	parent, base, closeParent, err := openParent(root, rel, true)
	if err != nil {
		return err
	}
	defer closeParent()
	created := false
	if err := unix.Mkdirat(parent, base, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	fd, err := unix.Openat(parent, base, directoryFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if created {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return err
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o777 != 0o700 {
		return fmt.Errorf("directory %s is not private to its expected owner", filepath.Join(root, rel))
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if err := unix.Fsync(parent); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}
