//go:build linux

package resources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadHostCPU measures this process's affinity mask and the tightest CPU
// bandwidth limit visible along its own cgroup v2 and legacy v1 CPU
// hierarchies. It does not use GOMAXPROCS, sample load or count other VMs.
func ReadHostCPU(ctx context.Context) (HostCPU, error) {
	if err := ctx.Err(); err != nil {
		return HostCPU{}, err
	}
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err != nil {
		return HostCPU{}, fmt.Errorf("read CPU affinity: %w", err)
	}
	host := HostCPU{Logical: set.Count()}
	membership, err := readProcFile("/proc/self/cgroup", 64<<10)
	if err != nil {
		return HostCPU{}, err
	}
	mounts, err := readProcFile("/proc/self/mountinfo", 8<<20)
	if err != nil {
		return HostCPU{}, err
	}
	host.Quota, host.Period, err = cgroupBandwidth(membership, mounts, verifyCgroupFS)
	if err != nil {
		return HostCPU{}, fmt.Errorf("read cgroup CPU limits: %w", err)
	}
	return host, ctx.Err()
}

func readProcFile(name string, limit int64) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > limit {
		return "", fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	return string(raw), nil
}

// cgroupBandwidth returns the tightest visible limit as quota/period, or zeros
// when no limit is visible. verify accepts the opened mount point's filesystem.
func cgroupBandwidth(membership, mountinfo string, verify func(fd int, v2 bool) error) (quota, period uint64, err error) {
	member, err := parseProcCgroup(membership)
	if err != nil {
		return 0, 0, err
	}
	mounts, err := parseMountInfo(mountinfo)
	if err != nil {
		return 0, 0, err
	}
	for _, h := range []struct {
		path string
		v2   bool
	}{{member.v2, true}, {member.v1CPU, false}} {
		if h.path == "" {
			continue
		}
		point, rel, visible, err := cgroupDir(mounts, h.v2, h.path)
		if err != nil {
			return 0, 0, err
		}
		if !visible {
			continue
		}
		q, p, err := walkCgroupBandwidth(point, rel, h.v2, verify)
		if err != nil {
			return 0, 0, err
		}
		if p != 0 && (period == 0 || tighter(q, p, quota, period)) {
			quota, period = q, p
		}
	}
	return quota, period, nil
}

const cgroupDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

// walkCgroupBandwidth reads each level from the mount point down to rel with
// descriptor-relative, no-follow opens that may not leave the mount's device.
// A level without the controller's files has no limit; other errors fail.
func walkCgroupBandwidth(point, rel string, v2 bool, verify func(fd int, v2 bool) error) (quota, period uint64, err error) {
	fd, err := unix.Open(point, cgroupDirFlags, 0)
	if err != nil {
		return 0, 0, &os.PathError{Op: "open", Path: point, Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	if err := verify(fd, v2); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", point, err)
	}
	var mount unix.Stat_t
	if err := unix.Fstat(fd, &mount); err != nil {
		return 0, 0, err
	}
	components := []string{}
	if rel != "" {
		components = strings.Split(rel, "/")
	}
	dir := point
	for i := 0; ; i++ {
		q, p, limited, err := cgroupLevelBandwidth(fd, v2)
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", dir, err)
		}
		if limited && (period == 0 || tighter(q, p, quota, period)) {
			quota, period = q, p
		}
		if i == len(components) {
			return quota, period, nil
		}
		next, err := unix.Openat(fd, components[i], cgroupDirFlags, 0)
		dir += "/" + components[i]
		if err != nil {
			return 0, 0, &os.PathError{Op: "open", Path: dir, Err: err}
		}
		_ = unix.Close(fd)
		fd = next
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return 0, 0, err
		}
		if st.Dev != mount.Dev {
			return 0, 0, fmt.Errorf("%s leaves its cgroup mount", dir)
		}
	}
}

func cgroupLevelBandwidth(dir int, v2 bool) (quota, period uint64, limited bool, err error) {
	if v2 {
		raw, found, err := readCgroupFile(dir, "cpu.max")
		if err != nil || !found {
			return 0, 0, false, err
		}
		return parseCPUMax(raw)
	}
	quotaRaw, found, err := readCgroupFile(dir, "cpu.cfs_quota_us")
	if err != nil || !found {
		return 0, 0, false, err
	}
	periodRaw, found, err := readCgroupFile(dir, "cpu.cfs_period_us")
	if err != nil {
		return 0, 0, false, err
	}
	if !found {
		return 0, 0, false, errors.New("cpu.cfs_quota_us without cpu.cfs_period_us")
	}
	return parseCFS(quotaRaw, periodRaw)
}

// readCgroupFile reads one small regular file; found is false only when it
// does not exist, meaning the controller is not active at that level.
func readCgroupFile(dir int, name string) (raw string, found bool, err error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open %s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > 256 {
		return "", false, fmt.Errorf("%s exceeds 256 bytes", name)
	}
	return string(data), true, nil
}

func verifyCgroupFS(fd int, v2 bool) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return err
	}
	want := int64(unix.CGROUP_SUPER_MAGIC)
	if v2 {
		want = unix.CGROUP2_SUPER_MAGIC
	}
	if int64(st.Type) != want {
		return errors.New("mount point is not the expected cgroup filesystem")
	}
	return nil
}
