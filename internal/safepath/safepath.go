// Package safepath provides descriptor-relative file operations beneath a
// trusted directory. It is used for account-state leaves, whose contents are
// writable by an untrusted in-box agent: ordinary path-based operations could
// follow symlinks planted by that agent back into the host filesystem.
package safepath

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

// cleanRelative validates and cleans a file path relative to a trusted root.
func cleanRelative(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be a non-empty relative path", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its trusted root", rel)
	}
	return clean, nil
}

func pathError(op, root, rel string, err error) error {
	return &os.PathError{Op: op, Path: filepath.Join(root, rel), Err: err}
}

// openParent opens rel's parent directory by walking from root one component
// at a time with O_NOFOLLOW. When create is true, missing directories beneath
// root are created at mode 0700. The returned cleanup closes parentFD.
func openParent(root, rel string, create bool) (parentFD int, base string, cleanup func(), err error) {
	clean, err := cleanRelative(rel)
	if err != nil {
		return -1, "", nil, err
	}
	if create {
		// root is the derived account-state leaf itself. A confined agent can
		// mutate entries inside this mount but cannot replace the mount root.
		if err := os.MkdirAll(root, 0o700); err != nil {
			return -1, "", nil, pathError("mkdir", root, rel, err)
		}
	}
	fd, err := unix.Open(root, directoryFlags, 0)
	if err != nil {
		return -1, "", nil, pathError("open root", root, rel, err)
	}
	closeFD := func() { _ = unix.Close(fd) }

	dir, base := filepath.Split(clean)
	dir = filepath.Clean(dir)
	if dir != "." {
		for _, component := range strings.Split(dir, string(filepath.Separator)) {
			if component == "" || component == "." {
				continue
			}
			next, openErr := unix.Openat(fd, component, directoryFlags, 0)
			if openErr != nil && create && errors.Is(openErr, fs.ErrNotExist) {
				if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil &&
					!errors.Is(mkdirErr, fs.ErrExist) {
					closeFD()
					return -1, "", nil, pathError("mkdir", root, rel, mkdirErr)
				}
				next, openErr = unix.Openat(fd, component, directoryFlags, 0)
				if syncErr := unix.Fsync(fd); syncErr != nil {
					if next >= 0 {
						_ = unix.Close(next)
					}
					closeFD()
					return -1, "", nil, pathError("sync parent", root, rel, syncErr)
				}
			}
			if openErr != nil {
				closeFD()
				return -1, "", nil, pathError("open parent", root, rel, openErr)
			}
			_ = unix.Close(fd)
			fd = next
		}
	}
	return fd, base, closeFD, nil
}

func openRegularAt(parentFD int, base string) (int, error) {
	fd, err := unix.Openat(parentFD, base,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("not a regular file")
	}
	return fd, nil
}

// RegularFileExists reports whether rel names a regular file beneath root.
// Symlinks and other special files are errors, never followed.
func RegularFileExists(root, rel string) (bool, error) {
	parentFD, base, cleanup, err := openParent(root, rel, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer cleanup()
	fd, err := openRegularAt(parentFD, base)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, pathError("open", root, rel, err)
	}
	_ = unix.Close(fd)
	return true, nil
}

func createTempAt(parentFD int) (fd int, name string, err error) {
	for range 10 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return -1, "", err
		}
		name = fmt.Sprintf(".yoloctl-%x.tmp", nonce)
		fd, err = unix.Openat(parentFD, name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600)
		if err == nil {
			return fd, name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return -1, "", err
		}
	}
	return -1, "", errors.New("could not allocate a unique temporary file")
}

// WriteFile atomically writes rel beneath root without following symlinks.
// Parent components are opened descriptor-relatively with O_NOFOLLOW; the
// final rename replaces a malicious final symlink itself rather than writing
// through it.
func WriteFile(root, rel string, body []byte, perm fs.FileMode) error {
	return WriteFileOwned(root, rel, body, perm, -1, -1)
}

// WriteFileOwned sets ownership before publication, so a private projected
// credential never has a partially published permission/ownership transition.
// Negative IDs preserve the writer's ownership, as in WriteFile.
func WriteFileOwned(root, rel string, body []byte, perm fs.FileMode, uid, gid int) error {
	parentFD, base, cleanup, err := openParent(root, rel, true)
	if err != nil {
		return err
	}
	defer cleanup()
	fd, tmpName, err := createTempAt(parentFD)
	if err != nil {
		return pathError("create temporary file", root, rel, err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = unix.Unlinkat(parentFD, tmpName, 0)
		}
	}()
	f := os.NewFile(uintptr(fd), filepath.Join(root, tmpName))
	if f == nil {
		_ = unix.Close(fd)
		return pathError("create temporary file", root, rel, errors.New("invalid file descriptor"))
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return pathError("write", root, rel, err)
	}
	if err := f.Chmod(perm.Perm()); err != nil {
		_ = f.Close()
		return pathError("chmod", root, rel, err)
	}
	if uid >= 0 || gid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			_ = f.Close()
			return pathError("chown", root, rel, err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return pathError("sync", root, rel, err)
	}
	if err := f.Close(); err != nil {
		return pathError("close", root, rel, err)
	}
	if err := unix.Renameat(parentFD, tmpName, parentFD, base); err != nil {
		return pathError("rename", root, rel, err)
	}
	renamed = true
	if err := unix.Fsync(parentFD); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}

// ReadFile reads one bounded regular file without following any symlink.
func ReadFile(root, rel string, limit int64) ([]byte, error) {
	data, _, err := ReadFileInfo(root, rel, limit)
	return data, err
}

// ReadFileInfo returns metadata for the same inode as the returned bytes.
func ReadFileInfo(root, rel string, limit int64) ([]byte, os.FileInfo, error) {
	parent, base, cleanup, err := openParent(root, rel, false)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	fd, err := openRegularAt(parent, base)
	if err != nil {
		return nil, nil, pathError("open", root, rel, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(root, rel))
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	info, err := f.Stat()
	return data, info, err
}

// RemoveFile durably unlinks a regular file. A symlink is rejected, not followed.
func RemoveFile(root, rel string) error {
	parent, base, cleanup, err := openParent(root, rel, false)
	if err != nil {
		return err
	}
	defer cleanup()
	fd, err := openRegularAt(parent, base)
	if err != nil {
		return pathError("open", root, rel, err)
	}
	_ = unix.Close(fd)
	if err := unix.Unlinkat(parent, base, 0); err != nil {
		return err
	}
	if err := unix.Fsync(parent); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}
