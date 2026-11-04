//go:build linux

package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func acceptFS(int, bool) error { return nil }

func cgroupTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCgroupWalkUsesTightestVisibleLevel(t *testing.T) {
	for _, tc := range []struct {
		name          string
		files         map[string]string
		quota, period uint64
	}{
		{"ancestor tighter", map[string]string{"a/cpu.max": "150000 100000\n", "a/b/cpu.max": "max 100000\n"}, 150000, 100000},
		{"leaf tighter", map[string]string{"a/cpu.max": "400000 100000\n", "a/b/cpu.max": "50000 50000\n"}, 50000, 50000},
		{"unlimited", map[string]string{"a/cpu.max": "max 100000\n", "a/b/cpu.max": "max 100000\n"}, 0, 0},
		{"controller absent", nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, p, err := walkCgroupBandwidth(cgroupTree(t, tc.files), "a/b", true, acceptFS)
			if err != nil || q != tc.quota || p != tc.period {
				t.Fatalf("%d/%d, %v", q, p, err)
			}
		})
	}
	q, p, err := walkCgroupBandwidth(cgroupTree(t, map[string]string{
		"cpu.cfs_quota_us": "-1\n", "cpu.cfs_period_us": "100000\n",
		"a/cpu.cfs_quota_us": "50000\n", "a/cpu.cfs_period_us": "100000\n",
	}), "a", false, acceptFS)
	if err != nil || q != 50000 || p != 100000 {
		t.Fatalf("legacy CFS: %d/%d, %v", q, p, err)
	}
}

func TestCgroupWalkFailsOnUnreadableOrEscapingLimits(t *testing.T) {
	for name, setup := range map[string]func(root string) error{
		"malformed ancestor": func(root string) error {
			return os.WriteFile(filepath.Join(root, "a", "cpu.max"), []byte("bogus\n"), 0o600)
		},
		"oversized": func(root string) error {
			return os.WriteFile(filepath.Join(root, "a", "cpu.max"), []byte(strings.Repeat("1", 300)+" 100000\n"), 0o600)
		},
		"symlinked limit": func(root string) error {
			target := filepath.Join(t.TempDir(), "cpu.max")
			if err := os.WriteFile(target, []byte("max 100000\n"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, filepath.Join(root, "a", "cpu.max"))
		},
		"symlinked directory": func(root string) error {
			if err := os.RemoveAll(filepath.Join(root, "a", "b")); err != nil {
				return err
			}
			return os.Symlink(t.TempDir(), filepath.Join(root, "a", "b"))
		},
		"missing cgroup":  func(root string) error { return os.RemoveAll(filepath.Join(root, "a", "b")) },
		"directory limit": func(root string) error { return os.Mkdir(filepath.Join(root, "a", "cpu.max"), 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			root := cgroupTree(t, map[string]string{"a/b/cpu.max": "max 100000\n"})
			if err := setup(root); err != nil {
				t.Fatal(err)
			}
			if q, p, err := walkCgroupBandwidth(root, "a/b", true, acceptFS); err == nil {
				t.Fatalf("accepted %d/%d", q, p)
			}
		})
	}
	root := cgroupTree(t, map[string]string{"a/cpu.cfs_quota_us": "50000\n"})
	if _, _, err := walkCgroupBandwidth(root, "a", false, acceptFS); err == nil {
		t.Fatal("accepted CFS quota without a period")
	}
	if _, _, err := walkCgroupBandwidth(root, "a", true, verifyCgroupFS); err == nil {
		t.Fatal("accepted a non-cgroup mount point")
	}
}

func TestCgroupBandwidthCombinesHierarchiesFromMembership(t *testing.T) {
	v2 := cgroupTree(t, map[string]string{"a/b/cpu.max": "300000 100000\n"})
	v1 := cgroupTree(t, map[string]string{"a/cpu.cfs_quota_us": "250000\n", "a/cpu.cfs_period_us": "100000\n"})
	membership := "4:cpu,cpuacct:/a\n0::/a/b\n"
	mountinfo := "29 23 0:26 / " + v2 + " rw - cgroup2 cgroup2 rw\n35 29 0:30 / " + v1 + " rw - cgroup cgroup rw,cpu,cpuacct\n"
	q, p, err := cgroupBandwidth(membership, mountinfo, acceptFS)
	if err != nil || q != 250000 || p != 100000 {
		t.Fatalf("%d/%d, %v", q, p, err)
	}
	if n, _, err := VMCPUs(HostCPU{Logical: 16, Quota: q, Period: p}, 32); err != nil || n != 3 {
		t.Fatalf("2.5 visible CPUs selected %d, %v", n, err)
	}
	// A bind mount of the caller's subtree, listed first, must not hide the
	// stricter ancestor that the full mount still exposes.
	tree := cgroupTree(t, map[string]string{"a/cpu.max": "150000 100000\n", "a/b/cpu.max": "max 100000\n"})
	aliases := "40 1 0:26 /a/b " + filepath.Join(tree, "a", "b") + " rw - cgroup2 cgroup2 rw\n29 23 0:26 / " + tree + " rw - cgroup2 cgroup2 rw\n"
	if q, p, err := cgroupBandwidth("0::/a/b\n", aliases, acceptFS); err != nil || q != 150000 || p != 100000 {
		t.Fatalf("subtree alias hid the ancestor limit: %d/%d, %v", q, p, err)
	}
	if q, p, err := cgroupBandwidth("0::/a/b\n", "22 1 8:1 / / rw - ext4 /dev/sda1 rw\n", acceptFS); err != nil || p != 0 {
		t.Fatalf("unmounted hierarchy: %d/%d, %v", q, p, err)
	}
	if _, _, err := cgroupBandwidth("0::/elsewhere\n", "29 23 0:26 /a "+v2+" rw - cgroup2 cgroup2 rw\n", acceptFS); err == nil {
		t.Fatal("read limits outside the resolved mount")
	}
}
