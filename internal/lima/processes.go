package lima

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// VMProcessIDs snapshots Lima's host-agent/driver PID files before shutdown.
// Keep these IDs even if Lima removes the files during a forced stop.
func VMProcessIDs(space string) ([]int, error) {
	dir, err := InstanceDir(space)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), dir)
	defer func() { _ = root.Close() }()
	entries, err := root.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		child, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(child), entry.Name())
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64 {
			_ = f.Close()
			return nil, fmt.Errorf("cannot read VM PID file %s", entry.Name())
		}
		raw, err := io.ReadAll(io.LimitReader(f, 64))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid VM PID file %s", entry.Name())
		}
		ids = append(ids, pid)
	}
	return ids, nil
}

// RequireVMProcessesStopped never signals a process beyond the existence probe.
// Permission failures and PID reuse both conservatively refuse forced deletion.
func RequireVMProcessesStopped(space, backend string, before []int) error {
	if backend != "qemu" && backend != "vz" {
		return fmt.Errorf("cannot establish shutdown for VM backend %q", backend)
	}
	after, err := VMProcessIDs(space)
	if err != nil {
		return err
	}
	for _, pid := range append(before, after...) {
		if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("VM process %d is still alive or inaccessible", pid)
		}
	}
	return nil
}
