//go:build linux || darwin

package boxexport

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type directory struct{ file *os.File }
type fileState struct {
	kind uint32
	mode fs.FileMode
	dev  uint64
	ino  uint64
	uid  uint32
}

func (s fileState) directory() bool { return s.kind == unix.S_IFDIR }
func (s fileState) regular() bool   { return s.kind == unix.S_IFREG }
func (d *directory) fd() int        { return int(d.file.Fd()) }
func (d *directory) close() error   { return d.file.Close() }
func (d *directory) stat(name string) (fileState, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd(), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fileState{}, err
	}
	return fileState{kind: uint32(st.Mode & unix.S_IFMT), mode: fs.FileMode(st.Mode & 0777), dev: uint64(st.Dev), ino: uint64(st.Ino), uid: st.Uid}, nil
}
func (d *directory) open(name string) (*directory, error) {
	fd, err := unix.Openat(d.fd(), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &directory{os.NewFile(uintptr(fd), name)}, nil
}
func (d *directory) mkdir(name string) (*directory, error) {
	if err := unix.Mkdirat(d.fd(), name, 0700); err != nil {
		return nil, err
	}
	return d.open(name)
}
func (d *directory) create(name string) (*os.File, error) {
	fd, err := unix.Openat(d.fd(), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (d *directory) names() ([]string, error) {
	copy, err := d.open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = copy.close() }()
	return copy.file.Readdirnames(-1)
}
func openHostDirectory(name string) (*directory, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	// These are the system's ordinary macOS aliases, not caller-controlled
	// destination links. Every subsequent component is opened no-follow.
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/tmp", "/var", "/etc"} {
			if abs == alias || strings.HasPrefix(abs, alias+"/") {
				if target, e := os.Readlink(alias); e == nil && (target == "private"+alias || target == "/private"+alias) {
					abs = "/private" + abs
				}
				break
			}
		}
	}
	fd, err := unix.Open("/", pathDirectoryFlags(), 0)
	if err != nil {
		return nil, err
	}
	d := &directory{os.NewFile(uintptr(fd), "/")}
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		nextFD, e := unix.Openat(d.fd(), part, pathDirectoryFlags(), 0)
		var next *directory
		if e == nil {
			next = &directory{os.NewFile(uintptr(nextFD), part)}
		}
		_ = d.close()
		if e != nil {
			return nil, fmt.Errorf("destination parent component %q: %w", part, e)
		}
		d = next
	}
	readable, err := d.open(".")
	_ = d.close()
	return readable, err
}
func newStage(parent *directory) (string, *directory, error) {
	name := ".yoloctl-cp-" + rand.Text()
	d, err := parent.mkdir(name)
	if err != nil {
		return "", nil, err
	}
	var held unix.Stat_t
	err = unix.Fstat(d.fd(), &held)
	st, pathErr := parent.stat(name)
	if err != nil || pathErr != nil || st.dev != uint64(held.Dev) || st.ino != uint64(held.Ino) || held.Uid != uint32(os.Geteuid()) || held.Mode&0777 != 0700 {
		_ = d.close()
		return "", nil, errors.New("copy staging directory is not private to the caller")
	}
	return name, d, nil
}
func (d *directory) parent(name string, create bool) (*directory, string, error) {
	parts := strings.Split(name, "/")
	at, err := d.open(".")
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, e := at.open(part)
		if e != nil && create && errors.Is(e, os.ErrNotExist) {
			next, e = at.mkdir(part)
		}
		_ = at.close()
		if e != nil {
			return nil, "", e
		}
		at = next
	}
	return at, parts[len(parts)-1], nil
}
func setAttributes(file *os.File, mode fs.FileMode, mod time.Time) error {
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if !mod.IsZero() {
		tv := unix.NsecToTimeval(mod.UnixNano())
		if err := unix.Futimes(int(file.Fd()), []unix.Timeval{tv, tv}); err != nil {
			return err
		}
	}
	return file.Sync()
}
func (d *directory) removeTree(name string) error {
	st, err := d.stat(name)
	if err != nil {
		return err
	}
	if !st.directory() {
		return unix.Unlinkat(d.fd(), name, 0)
	}
	child, err := d.open(name)
	if err != nil {
		return err
	}
	names, err := child.names()
	if err == nil {
		for _, n := range names {
			if e := child.removeTree(n); e != nil {
				err = errors.Join(err, e)
			}
		}
	}
	err = errors.Join(err, child.close())
	if err != nil {
		return err
	}
	return unix.Unlinkat(d.fd(), name, unix.AT_REMOVEDIR)
}
func cleanupStage(parent *directory, name string, stage *directory) error {
	var st unix.Stat_t
	if err := unix.Fstat(stage.fd(), &st); err != nil {
		return err
	}
	now, err := parent.stat(name)
	if err != nil {
		return err
	}
	if now.dev != uint64(st.Dev) || now.ino != uint64(st.Ino) || !now.directory() {
		return errors.New("copy staging identity changed; retained")
	}
	return parent.removeTree(name)
}
func moveEntry(from *directory, old string, to *directory, name string, overwrite bool) error {
	if !overwrite {
		return renameExclusive(from.fd(), old, to.fd(), name)
	}
	st, err := to.stat(name)
	if errors.Is(err, os.ErrNotExist) {
		return renameExclusive(from.fd(), old, to.fd(), name)
	}
	if err != nil {
		return err
	}
	if !st.regular() {
		return fmt.Errorf("destination %q is not a regular file; not overwritten", name)
	}
	source, err := from.stat(old)
	if err != nil {
		return err
	}
	if !source.regular() {
		return fmt.Errorf("source %q cannot replace a regular file", old)
	}
	// renameat replaces the directory entry atomically; it never opens or
	// follows the existing leaf. Parent descriptors remain fixed throughout.
	return unix.Renameat(from.fd(), old, to.fd(), name)
}
