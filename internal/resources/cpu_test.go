package resources

import (
	"strings"
	"testing"
)

func TestVMCPUsUsesVisibleCapacityAndRoundsBandwidthUp(t *testing.T) {
	for _, tc := range []struct {
		name          string
		host          HostCPU
		maximum, want int
		clipped       bool
	}{
		{"affinity only", HostCPU{Logical: 8}, 0, 8, false},
		{"one CPU", HostCPU{Logical: 1}, 32, 1, false},
		{"fractional bandwidth rounds up", HostCPU{Logical: 16, Quota: 250000, Period: 100000}, 0, 3, false},
		{"whole bandwidth", HostCPU{Logical: 16, Quota: 200000, Period: 100000}, 0, 2, false},
		{"sub-CPU bandwidth keeps one vCPU", HostCPU{Logical: 4, Quota: 30000, Period: 100000}, 0, 1, false},
		{"bandwidth above affinity", HostCPU{Logical: 2, Quota: 800000, Period: 100000}, 0, 2, false},
		{"huge quota", HostCPU{Logical: 8, Quota: ^uint64(0), Period: 1}, 0, 8, false},
		{"adapter maximum", HostCPU{Logical: 64}, 32, 32, true},
		{"bandwidth below maximum", HostCPU{Logical: 64, Quota: 400000, Period: 100000}, 32, 4, false},
		{"no fixed maximum", HostCPU{Logical: 64}, 0, 64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clipped, err := VMCPUs(tc.host, tc.maximum)
			if err != nil || got != tc.want || clipped != tc.clipped {
				t.Fatalf("VMCPUs(%+v, %d) = %d, %v, %v; want %d, %v", tc.host, tc.maximum, got, clipped, err, tc.want, tc.clipped)
			}
		})
	}
	for _, host := range []HostCPU{{}, {Logical: -1}, {Logical: 4, Quota: 1}, {Logical: 4, Period: 100000}} {
		if _, _, err := VMCPUs(host, 0); err == nil {
			t.Fatalf("accepted invalid measurement %+v", host)
		}
	}
}

func TestHostCPUDescribeNamesExactBandwidth(t *testing.T) {
	got := HostCPU{Logical: 16, Quota: 250000, Period: 100000}.Describe()
	for _, want := range []string{"16 available logical CPUs", "caller-visible cgroup CPU bandwidth 2.5", "250000/100000µs"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if got := (HostCPU{Logical: 4}).Describe(); strings.Contains(got, "bandwidth") {
		t.Fatal(got)
	}
}

func TestTighterComparesExactRatios(t *testing.T) {
	if !tighter(1, 2, 2, 3) || tighter(2, 3, 1, 2) || tighter(1, 2, 2, 4) {
		t.Fatal("ratio comparison")
	}
	if !tighter(^uint64(0)-1, ^uint64(0), 1, 1) {
		t.Fatal("overflowing cross product")
	}
}

func TestParseProcCgroupSelectsV2AndLegacyCPU(t *testing.T) {
	got, err := parseProcCgroup("12:cpuset:/a\n4:cpu,cpuacct:/user.slice/x:y\n1:name=systemd:/s\n0::/user.slice/session.scope\n")
	if err != nil || got.v2 != "/user.slice/session.scope" || got.v1CPU != "/user.slice/x:y" {
		t.Fatalf("%+v, %v", got, err)
	}
	if got, err := parseProcCgroup(""); err != nil || got != (procCgroups{}) {
		t.Fatalf("empty membership: %+v, %v", got, err)
	}
	for _, raw := range []string{
		"0::/a\n0::/b\n",
		"3:cpu:/a\n4:cpu,cpuacct:/b\n",
		"0::relative\n",
		"garbage\n",
		"0::/a\n\n1:name=x:/b\n",
		":cpu:/a\n",
	} {
		if _, err := parseProcCgroup(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestParseMountInfoKeepsCPUHierarchies(t *testing.T) {
	raw := strings.Join([]string{
		`22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw`,
		`29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:4 - cgroup2 cgroup2 rw,nsdelegate`,
		`35 29 0:30 / /sys/fs/cgroup/cpu,cpuacct rw,nosuid shared:12 - cgroup cgroup rw,cpu,cpuacct`,
		`36 29 0:31 / /sys/fs/cgroup/cpuset rw shared:13 - cgroup cgroup rw,cpuset`,
		`40 1 0:32 /docker/abc /mnt/with\040space rw - cgroup2 cgroup2 rw`,
	}, "\n") + "\n"
	got, err := parseMountInfo(raw)
	want := []cgroupMount{
		{root: "/", point: "/sys/fs/cgroup", v2: true},
		{root: "/", point: "/sys/fs/cgroup/cpu,cpuacct"},
		{root: "/docker/abc", point: "/mnt/with space", v2: true},
	}
	if err != nil || len(got) != len(want) {
		t.Fatalf("%+v, %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mount %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, line := range []string{
		"no separator here",
		`29 23 0:26 / /sys/fs/cgroup rw - cgroup2`,
		`29 23 0:26 / /sys/fs/cgroup\04 rw - cgroup2 cgroup2 rw`,
		`29 23 0:26 / /sys/fs/cgroup\999 rw - cgroup2 cgroup2 rw`,
		`29 23 0:26 relative /sys/fs/cgroup rw - cgroup2 cgroup2 rw`,
	} {
		if _, err := parseMountInfo(line + "\n"); err == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}

func TestCgroupDirResolvesOnlyBeneathVisibleMounts(t *testing.T) {
	host := []cgroupMount{{root: "/", point: "/sys/fs/cgroup", v2: true}, {root: "/", point: "/v1/cpu"}}
	for _, tc := range []struct {
		mounts     []cgroupMount
		v2         bool
		member     string
		point, rel string
	}{
		{host, true, "/user.slice/a.scope", "/sys/fs/cgroup", "user.slice/a.scope"},
		{host, true, "/", "/sys/fs/cgroup", ""},
		{host, false, "/system.slice", "/v1/cpu", "system.slice"},
		{[]cgroupMount{{root: "/docker/abc", point: "/sys/fs/cgroup", v2: true}}, true, "/docker/abc", "/sys/fs/cgroup", ""},
		{[]cgroupMount{{root: "/x", point: "/b", v2: true}, {root: "/", point: "/a", v2: true}}, true, "/x/y", "/a", "x/y"},
	} {
		point, rel, visible, err := cgroupDir(tc.mounts, tc.v2, tc.member)
		if err != nil || !visible || point != tc.point || rel != tc.rel {
			t.Fatalf("%s: %q %q %v %v", tc.member, point, rel, visible, err)
		}
	}
	if _, _, visible, err := cgroupDir(host[1:], true, "/a"); err != nil || visible {
		t.Fatalf("unmounted hierarchy must be invisible, not an error: %v %v", visible, err)
	}
	container := []cgroupMount{{root: "/docker/abc", point: "/sys/fs/cgroup", v2: true}}
	for _, member := range []string{"/docker/abcdef", "/other", "/docker/abc/../def", "/..", "/a/"} {
		if _, _, _, err := cgroupDir(container, true, member); err == nil {
			t.Fatalf("resolved %q outside the visible mount", member)
		}
	}
}

func TestParseCgroupBandwidthFiles(t *testing.T) {
	if q, p, limited, err := parseCPUMax("max 100000\n"); err != nil || limited || q != 0 || p != 100000 {
		t.Fatal("unlimited cpu.max", q, p, limited, err)
	}
	if q, p, limited, err := parseCPUMax("250000 100000\n"); err != nil || !limited || q != 250000 || p != 100000 {
		t.Fatal("limited cpu.max", q, p, limited, err)
	}
	for _, raw := range []string{"", "250000\n", "0 100000\n", "250000 0\n", "abc 100000\n", "1 2 3\n", "250000 100000", "-5 100000\n", "max max\n", "1 1\n\n"} {
		if _, _, _, err := parseCPUMax(raw); err == nil {
			t.Fatalf("accepted cpu.max %q", raw)
		}
	}
	if _, p, limited, err := parseCFS("-1\n", "100000\n"); err != nil || limited || p != 100000 {
		t.Fatal("unlimited CFS", limited, err)
	}
	if q, p, limited, err := parseCFS("50000\n", "100000\n"); err != nil || !limited || q != 50000 || p != 100000 {
		t.Fatal("limited CFS", q, p, limited, err)
	}
	for _, pair := range [][2]string{{"0\n", "100000\n"}, {"-2\n", "100000\n"}, {"x\n", "100000\n"}, {"50000\n", "0\n"}, {"50000\n", "-1\n"}, {"50000\n", ""}} {
		if _, _, _, err := parseCFS(pair[0], pair[1]); err == nil {
			t.Fatalf("accepted CFS %q", pair)
		}
	}
}
