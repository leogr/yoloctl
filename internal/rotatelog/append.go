// Package rotatelog provides bounded private append logs. Writers reopen the
// named generation under a lock; a stale descriptor is never written after
// another writer rotates it. Callers own the parent directory descriptor.
package rotatelog

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Attempts bounds an append across rotations and macOS O_CREAT/rename races.
const Attempts = 4

// Appender shares the host diagnostics rotation mechanism with background logs.
// Limit is the active generation size; one previous generation is retained.
// A single record can exceed Limit, so stream writers must split large writes.
// Sync preserves host.log's per-record durability; ordinary stream logs need
// only the directory sync on rotation, matching their existing write behavior.
type Appender struct {
	Limit int64
	Label string
	Sync  bool
	Open  func(dir int, name string) (int, error)
	// TrimOversize bounds a pre-existing unbounded generation on rotation.
	// It retains its last Limit bytes without truncating a live inode.
	TrimOversize bool
}

func (a Appender) label() string {
	if a.Label != "" {
		return a.Label
	}
	return "log"
}

func (a Appender) open(dir int, name string) (int, error) {
	if a.Open != nil {
		return a.Open(dir, name)
	}
	flags := unix.O_WRONLY
	if a.TrimOversize {
		flags = unix.O_RDWR
	}
	return unix.Openat(dir, name, flags|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
}

// Append writes one record to the current generation. Name must be a single
// trusted leaf; Path is used only in diagnostics. All writers must use this
// protocol, rather than retain a file descriptor across external rotations.
func (a Appender) Append(dir int, name, path string, record []byte) error {
	_, err := a.append(dir, name, path, record)
	return err
}

func (a Appender) append(dir int, name, path string, record []byte) (int, error) {
	var openErr error
	for range Attempts {
		fd, err := a.open(dir, name)
		if errors.Is(err, unix.ENOENT) {
			// macOS can report ENOENT for O_CREAT while another writer
			// renames the name away; the next attempt opens the new file.
			openErr = err
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("open %s %q: %w", a.label(), path, err)
		}
		f := os.NewFile(uintptr(fd), path)
		retry, n, err := a.appendLocked(f, fd, dir, name, path, record)
		_ = f.Close()
		if err != nil || !retry {
			return n, err // includes a failed sync after rotation
		}
	}
	if openErr != nil {
		return 0, fmt.Errorf("open %s %q: %w", a.label(), path, openErr)
	}
	return 0, fmt.Errorf("%s %q kept changing while appending", a.label(), path)
}

// AppendLocked checks identity before validating or writing the current file.
// Startup descriptor handoff uses this check; ordinary writers call Append.
func (a Appender) AppendLocked(f *os.File, fd, dir int, name, path string, record []byte) (bool, error) {
	retry, _, err := a.appendLocked(f, fd, dir, name, path, record)
	return retry, err
}

func (a Appender) appendLocked(f *os.File, fd, dir int, name, path string, record []byte) (bool, int, error) {
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return false, 0, err
	}
	var st, named unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, 0, err
	}
	if err := unix.Fstatat(dir, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Dev != st.Dev || named.Ino != st.Ino {
		return true, 0, nil // rotated or replaced meanwhile: reopen the current file
	}
	// Two rotations can unlink a waiting writer's old inode. Validate only
	// the current named file; never append to that stale descriptor.
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0600 || st.Nlink != 1 {
		return false, 0, fmt.Errorf("%s %q must be an owned private regular file", a.label(), path)
	}
	if st.Size > 0 && st.Size+int64(len(record)) > a.Limit {
		// Keep one previous generation; renaming never follows a planted name.
		if err := a.rotate(f, dir, name, st.Size); err != nil {
			return false, 0, fmt.Errorf("rotate %s %q: %w", a.label(), path, err)
		}
		return true, 0, unix.Fsync(dir)
	}
	n, err := f.Write(record)
	if err != nil {
		return false, n, err
	}
	if a.Sync {
		return false, n, errors.Join(f.Sync(), unix.Fsync(dir))
	}
	return false, n, nil
}

// rotate keeps the ordinary rename path used by host.log. A previously
// unbounded background file needs a bounded tail first. Publish that tail
// before retiring the current name; a failure keeps the current file intact.
func (a Appender) rotate(current *os.File, dir int, name string, size int64) error {
	if !a.TrimOversize || size <= a.Limit {
		return unix.Renameat(dir, name, dir, name+".1")
	}
	tempName := "." + name + ".rotate-" + rand.Text()
	fd, err := unix.Openat(dir, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	temp := os.NewFile(uintptr(fd), tempName)
	defer func() { _ = temp.Close(); _ = unix.Unlinkat(dir, tempName, 0) }()
	if _, err = io.Copy(temp, io.NewSectionReader(current, size-a.Limit, a.Limit)); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = unix.Renameat(dir, tempName, dir, name+".1"); err != nil {
		return err
	}
	return unix.Unlinkat(dir, name, 0)
}
