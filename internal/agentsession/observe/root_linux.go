//go:build linux

package observe

import (
	"os"

	"golang.org/x/sys/unix"
)

// The mount and its verification are replaceable so tests can run unprivileged
// on an ordinary directory; production always uses the kernel's view.
var (
	rootFS     int64 = unix.TMPFS_MAGIC
	mountTmpfs       = func(dir string) error {
		return unix.Mount("yoloctl-observe", dir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, rootOptions)
	}
	unmountRoot = func(dir string) error { return unix.Unmount(dir, 0) }
	mountInfo   = func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }
)

// observationFS reports whether fd is on the root's filesystem type.
func observationFS(fd int) bool {
	var fs unix.Statfs_t
	return unix.Fstatfs(fd, &fs) == nil && int64(fs.Type) == rootFS
}
