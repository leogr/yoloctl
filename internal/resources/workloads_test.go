package resources

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestWorkloadBudgetsAndKernelProbe(t *testing.T) {
	for _, tc := range []struct{ total, reserve uint64 }{{4 * GiB, GiB}, {8 * GiB, GiB}, {16 * GiB, 2 * GiB}, {GiB, GiB / 2}} {
		b, err := Workloads(tc.total)
		if err != nil || b.Reserve != tc.reserve || b.Max+b.Reserve != tc.total {
			t.Fatal(b, err)
		}
		unit := string(b.SliceUnit())
		if !strings.Contains(unit, "\nMemoryHigh=infinity\n") || !strings.Contains(unit, fmt.Sprintf("\nMemoryMax=%d\n", b.Max)) {
			t.Fatal("must retain the hard limit without early throttling", unit)
		}
		raw := fmt.Sprintf("%d\nmax\n0\nMemTotal: %d kB\nMemAvailable: 100 kB\n", b.Max, tc.total/1024)
		if err := ValidatePool(raw); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{strings.Replace(raw, "\n0\n", "\nmax\n", 1), strings.Replace(raw, fmt.Sprint(b.Max), "max", 1), "", raw + "MemTotal: 1 kB\n"} {
			if err := ValidatePool(invalid); err == nil {
				t.Fatalf("accepted invalid pool: %q", invalid)
			}
		}
		// Reject the previous policy too: it stalled allocations below the
		// hard limit for minutes, although the VM eventually contained OOM.
		throttled := strings.Replace(raw, "\nmax\n", fmt.Sprintf("\n%d\n", b.Max*9/10), 1)
		if err := ValidatePool(throttled); err == nil {
			t.Fatal("accepted early-throttling pool")
		}
	}
	b, err := Workloads(4*GiB - 17000*1024)
	if err != nil || b.Max%(1<<20) != 0 {
		t.Fatal("page rounding", b, err)
	}
}

func TestBuildkitWorkloadPlacement(t *testing.T) {
	raw := []byte("# preserved when unchanged\n[registry.'example.org']\nhttp=true\n[worker.containerd]\nenabled=true\nnamespace='default'\n[[worker.containerd.gcpolicy]]\nall=true\nmaxUsedSpace=123\n[worker.containerd.runtime.options]\nBinaryName='runc'\n")
	for _, systemd := range []bool{false, true} {
		next, err := BuildkitWorkloads(raw, systemd)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Registry map[string]struct {
				HTTP bool `toml:"http"`
			} `toml:"registry"`
			Worker struct {
				Containerd struct {
					Parent   string `toml:"defaultCgroupParent"`
					GCPolicy []struct {
						Max int `toml:"maxUsedSpace"`
					} `toml:"gcpolicy"`
					Runtime struct {
						Name    string
						Options struct {
							SystemdCgroup bool
							BinaryName    string
						}
					} `toml:"runtime"`
				} `toml:"containerd"`
			} `toml:"worker"`
		}
		if err := toml.Unmarshal(next, &c); err != nil {
			t.Fatal(err)
		}
		w := c.Worker.Containerd
		wantParent := WorkloadGroup
		if systemd {
			wantParent = WorkloadSlice + ":buildkit:"
		}
		if w.Parent != wantParent || w.Runtime.Options.SystemdCgroup != systemd || w.Runtime.Name != "io.containerd.runc.v2" || w.Runtime.Options.BinaryName != "runc" || !c.Registry["example.org"].HTTP || len(w.GCPolicy) != 1 || w.GCPolicy[0].Max != 123 {
			t.Fatalf("lost configuration: %s", next)
		}
		commented := append([]byte("# comment\n"), next...)
		again, err := BuildkitWorkloads(commented, systemd)
		if err != nil || string(again) != string(commented) {
			t.Fatal("non-idempotent configuration", err)
		}
	}
	for _, invalid := range []string{"", "[worker.containerd]\nenabled=true\nnamespace='other'", "[worker.containerd]\nenabled=true\nnamespace='default'\n[worker.containerd.runtime]\nname='other'"} {
		if _, err := BuildkitWorkloads([]byte(invalid), true); err == nil {
			t.Fatal("accepted unsupported config", invalid)
		}
	}
}

func TestBoxLimits(t *testing.T) {
	for input, want := range map[string]int64{"": 0, "shared": 0, "0": 0, "2g": 2 << 30, "2048m": 2 << 30, "1.5GiB": 1536 << 20, "512mb": 512 << 20, "123": 123} {
		if got, err := BoxMemory(input); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"-1", "NaN", "2junk", "1e99", "9999999999999999999999g"} {
		if _, err := BoxMemory(input); err == nil {
			t.Fatal("invalid memory accepted", input)
		}
	}
	for input, want := range map[string]int64{"": 0, "0": 0, "2": 200000, "2.0": 200000, "0.5": 50000} {
		if got, err := CPUQuota(input); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"-1", "NaN", "Inf", "1e99", "0.000001"} {
		if _, err := CPUQuota(input); err == nil {
			t.Fatal("invalid CPU limit accepted", input)
		}
	}
}
