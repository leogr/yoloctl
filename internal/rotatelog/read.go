package rotatelog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReadTail reads a bounded suffix across the current and previous generation.
// It preserves raw bytes, including a line split by rotation. This is for our
// private files, not for Lima-owned logs. Concurrent replacement is retried
// within the same bound as appends; non-absence errors are never hidden.
func ReadTail(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("invalid log read limit")
	}
	dir, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dir) }()
	name := filepath.Base(path)
	for range Attempts {
		current, err := openReadLog(dir, name)
		if errors.Is(err, os.ErrNotExist) {
			var st unix.Stat_t
			if e := unix.Fstatat(dir, name+".1", &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(e, os.ErrNotExist) {
				return nil, err
			} else if e != nil {
				return nil, e
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		data, retry, err := readGenerationTail(current, dir, name, limit)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		if !retry {
			return data, nil
		}
	}
	return nil, fmt.Errorf("log %q kept changing while reading", path)
}

func openReadLog(dir int, name string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0600 || st.Nlink != 1) {
		err = fmt.Errorf("log %q must be an owned private regular file", name)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func readGenerationTail(current *os.File, dir int, name string, limit int64) ([]byte, bool, error) {
	var before unix.Stat_t
	if err := unix.Fstat(int(current.Fd()), &before); err != nil {
		return nil, false, err
	}
	size := min(before.Size, limit)
	var data []byte
	if size < limit {
		previous, err := openReadLog(dir, name+".1")
		if err == nil {
			st, e := previous.Stat()
			if e == nil {
				data, e = io.ReadAll(io.NewSectionReader(previous, max(st.Size()-(limit-size), 0), min(st.Size(), limit-size)))
			}
			_ = previous.Close()
			if e != nil {
				return nil, false, e
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
	}
	tail, err := io.ReadAll(io.NewSectionReader(current, before.Size-size, size))
	if err != nil {
		return nil, false, err
	}
	var named unix.Stat_t
	err = unix.Fstatat(dir, name, &named, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if err != nil || before.Dev != named.Dev || before.Ino != named.Ino {
		return nil, true, nil
	}
	return append(data, tail...), false, nil
}
