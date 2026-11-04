package vmshare

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// SSHFS has file fsync but no directory-fsync operation. A successful namespace
// reply therefore includes syncing the affected parent(s). Hold the same parent
// descriptors for mutation and sync: looking them up again could sync a different
// directory after a concurrent host rename. Like os.Root, an open descriptor
// continues to name its original object if the host moves it.
func (s *Share) parent(name string) (*os.File, string, error) {
	name = strings.TrimRight(relative(name), "/")
	index := strings.LastIndexByte(name, '/')
	dir, base := ".", name
	if index >= 0 {
		dir, base = name[:index], name[index+1:]
	}
	if base == "" || base == "." || base == ".." {
		return nil, "", os.ErrPermission
	}
	f, err := s.root.OpenFile(dir, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	st, err := f.Stat()
	if err == nil && !st.IsDir() {
		err = unix.ENOTDIR
	}
	if err != nil {
		_ = f.Close()
		return nil, "", err
	}
	return f, base, nil
}

func (s *Share) syncParents(files ...*os.File) error {
	var result error
	for _, f := range files {
		if err := s.syncDir(f); err != nil {
			result = errors.Join(result, fmt.Errorf("namespace changed but parent sync failed: %w", err))
		}
	}
	return result
}

func (s *Share) changeName(name string, change func(int, string) error) error {
	if !s.writable {
		return os.ErrPermission
	}
	parent, base, err := s.parent(name)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := change(int(parent.Fd()), base); err != nil {
		return err
	}
	return s.syncParents(parent)
}

func (s *Share) changeNames(old, next string, change func(int, string, int, string) error) error {
	if !s.writable {
		return os.ErrPermission
	}
	src, srcName, err := s.parent(old)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, dstName, err := s.parent(next)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	if err := change(int(src.Fd()), srcName, int(dst.Fd()), dstName); err != nil {
		return err
	}
	return s.syncParents(src, dst)
}

// Existing files (including symlink targets) open through the full grant root.
// Creation is exclusive at a pinned parent, so the final component cannot race
// into a followed symlink. A dangling relative symlink is resolved within the
// grant and retried, with the same finite bound as ordinary symlink traversal.
func (s *Share) openFile(name string, flags int, perm os.FileMode) (*os.File, error) {
	if flags&os.O_CREATE == 0 {
		return s.root.OpenFile(relative(name), flags, perm)
	}
	for range 40 {
		if flags&os.O_EXCL == 0 {
			f, err := s.root.OpenFile(relative(name), flags&^os.O_CREATE, perm)
			if !errors.Is(err, os.ErrNotExist) {
				return f, err
			}
		}
		parent, base, err := s.parent(name)
		if err != nil {
			return nil, err
		}
		fd, err := unix.Openat(int(parent.Fd()), base, flags|os.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm.Perm()))
		if err == nil {
			f := os.NewFile(uintptr(fd), name)
			err = s.syncParents(parent)
			_ = parent.Close()
			if err != nil {
				_ = f.Close()
				return nil, err
			}
			return f, nil
		}
		_ = parent.Close()
		if !errors.Is(err, os.ErrExist) || flags&os.O_EXCL != 0 {
			return nil, err
		}
		link, err := s.root.Readlink(relative(name))
		if err != nil {
			if errors.Is(err, unix.EINVAL) || errors.Is(err, os.ErrNotExist) {
				continue // raced with replacement; retry through os.Root
			}
			return nil, err
		}
		if strings.HasPrefix(link, "/") {
			return nil, os.ErrPermission
		}
		// Do not clean away '..': os.Root must interpret it after symlinks.
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[:i+1] + link
		} else {
			name = link
		}
	}
	return nil, unix.ELOOP
}
