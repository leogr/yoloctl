package boxcache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// PrepareDirectories creates feature-declared directories in an existing cache
// leaf. Missing cache storage stays missing (clear of a missing cache is a no-op).
// Each path component is opened no-follow and remains owned by the space user.
func PrepareDirectories(ctx context.Context, name string, uid, gid int, dirs []string) error {
	return prepareDirectories(ctx, Root, name, uid, gid, dirs)
}

func prepareDirectories(ctx context.Context, root, name string, uid, gid int, dirs []string) error {
	if err := validateDirectoryPaths(dirs); err != nil {
		return err
	}
	if len(dirs) == 0 {
		return ctx.Err()
	}
	if _, err := Path(name); err != nil {
		return err
	}
	if uid < 1 || gid < 1 {
		return errors.New("cache directories require the positive space UID/GID")
	}
	parent, err := openParent(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := verifyLeaf(parent, name, uid, gid); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	leaf, err := parent.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = leaf.Close() }()
	for _, dir := range dirs {
		if err := prepareDirectoryPath(ctx, int(leaf.Fd()), strings.Split(dir, "/"), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func prepareDirectoryPath(ctx context.Context, parent int, parts []string, uid, gid int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := parts[0]
	fd, err := unix.Openat(parent, name, directoryFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		created := false
		if err = unix.Mkdirat(parent, name, 0o700); err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			return err
		}
		fd, err = unix.Openat(parent, name, directoryFlags, 0)
		if err == nil && created {
			if err = unix.Fchown(fd, uid, gid); err == nil {
				err = unix.Fchmod(fd, 0o700)
			}
			if err == nil {
				err = unix.Fsync(parent)
			}
		}
	}
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7777 != 0o700 {
		return errors.New("feature cache directory ownership or mode differs from the space user")
	}
	if len(parts) > 1 {
		if err := prepareDirectoryPath(ctx, fd, parts[1:], uid, gid); err != nil {
			return err
		}
	}
	return unix.Fsync(fd)
}

func validateDirectoryPaths(dirs []string) error {
	for _, dir := range dirs {
		if dir == "." || !fs.ValidPath(dir) || strings.ContainsRune(dir, 0) {
			return errors.New("invalid relative cache directory: " + dir)
		}
	}
	return nil
}
