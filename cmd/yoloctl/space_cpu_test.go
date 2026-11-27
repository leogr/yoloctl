package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func stubHostCPU(t *testing.T, host resources.HostCPU, err error) *int {
	t.Helper()
	calls := 0
	previous := readHostCPU
	readHostCPU = func(context.Context) (resources.HostCPU, error) {
		calls++
		return host, err
	}
	t.Cleanup(func() { readHostCPU = previous })
	return &calls
}

func TestSpaceCreateDefaultsToAutomaticCPUs(t *testing.T) {
	if got := newSpaceCreateCmd().Flag("cpus"); got == nil || got.DefValue != "auto" {
		t.Fatalf("new spaces must default to automatic CPUs: %+v", got)
	}
	root := newClientRootCmd()
	for verb, wantAuto := range map[string]bool{"create": true, "set": false} {
		cmd, _, err := root.Find([]string{"space", verb})
		if err != nil {
			t.Fatal(err)
		}
		fn, ok := cmd.GetFlagCompletionFunc("cpus")
		if !ok {
			t.Fatalf("space %s lacks CPU completion", verb)
		}
		choices, _ := fn(cmd, nil, "")
		if strings.Contains(strings.Join(choices, " "), "auto") != wantAuto {
			t.Fatalf("space %s CPU choices %v", verb, choices)
		}
	}
}

func TestAutomaticSpaceCPUsUseMeasuredCapacity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		host    resources.HostCPU
		want    int
		message string
	}{
		{"available", "lima", resources.HostCPU{Logical: 18}, 18, "VM CPUs: 18 (auto: 18 available logical CPUs; override with --cpus)"},
		{"cap", "firecracker", resources.HostCPU{Logical: 48}, 32, "VM CPUs: 32 (auto: 48 available logical CPUs, capped at 32 on Firecracker; override with --cpus)"},
		{"quota", "firecracker", resources.HostCPU{Logical: 18, Quota: 400000, Period: 100000}, 4, "VM CPUs: 4 (auto: 18 logical CPUs, limited to 4 by this process's CPU quota; override with --cpus)"},
		{"fractional quota", "lima", resources.HostCPU{Logical: 16, Quota: 250000, Period: 100000}, 3, "VM CPUs: 3 (auto: 16 logical CPUs, limited to 3 by this process's CPU quota (rounded up); override with --cpus)"},
		{"quota and cap", "firecracker", resources.HostCPU{Logical: 48, Quota: 4000000, Period: 100000}, 32, "VM CPUs: 32 (auto: 48 logical CPUs, limited to 40 by this process's CPU quota, capped at 32 on Firecracker; override with --cpus)"},
		{"Lima has no cap", "lima", resources.HostCPU{Logical: 64}, 64, "VM CPUs: 64 (auto: 64 available logical CPUs; override with --cpus)"},
		{"single CPU", "firecracker", resources.HostCPU{Logical: 1}, 1, "VM CPUs: 1 (auto: 1 available logical CPU; override with --cpus)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubHostCPU(t, tc.host, nil)
			wire, _ := captureCLIRecords(t)
			got, err := resolveSpaceCPUs(t.Context(), tc.backend, "auto")
			records := readCLIRecords(t, wire)
			if err != nil || got != tc.want || *calls != 1 || len(records) != 1 || records[0].Message != tc.message {
				t.Fatalf("%s %+v: %d, %v, log %+v; want %q", tc.backend, tc.host, got, err, records, tc.message)
			}
		})
	}
	stubHostCPU(t, resources.HostCPU{}, errors.New("cgroup unreadable"))
	if _, err := resolveSpaceCPUs(t.Context(), "lima", "auto"); err == nil || !strings.Contains(err.Error(), "--cpus") {
		t.Fatalf("measurement failure lacks the explicit remedy: %v", err)
	}
}

func TestExplicitSpaceCPUsNeverProbe(t *testing.T) {
	calls := stubHostCPU(t, resources.HostCPU{Logical: 2}, nil)
	wire, _ := captureCLIRecords(t)
	for _, tc := range []struct {
		value string
		want  int
		line  string
	}{
		{"4", 4, "VM CPUs: 4"},
		{"48", 48, "VM CPUs: 48"},
	} {
		got, err := resolveSpaceCPUs(t.Context(), "lima", tc.value)
		records := readCLIRecords(t, wire)
		if err != nil || got != tc.want || len(records) != 1 || records[0].Message != tc.line {
			t.Fatalf("explicit CPUs %s: %d, %v, log %+v", tc.value, got, err, records)
		}
	}
	for _, value := range []string{"0", "-1", "+2", "1.5", "", " 4", "auto ", "2147483648"} {
		if _, err := resolveSpaceCPUs(t.Context(), "lima", value); err == nil {
			t.Fatalf("accepted --cpus %q", value)
		}
	}
	if *calls != 0 {
		t.Fatalf("explicit CPUs measured the host %d times", *calls)
	}
}

// Creation refuses CPU values before image preparation, bootstrap or disk
// effects, so nothing remains to recover.
func TestSpaceCreateRefusesCPUsBeforeEffects(t *testing.T) {
	stubOperatorClient(t, nil)
	shortYoloctlHome(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	workdir := t.TempDir()
	cases := map[string][]string{
		"invalid":     {"--backend", "lima", "--cpus", "0"},
		"measurement": {"--backend", "lima"},
	}
	if _, err := vmhost.Driver("firecracker"); err == nil {
		cases["adapter bound"] = []string{"--backend", "firecracker", "--cpus", "33", "--vm-image", filepath.Join(t.TempDir(), "absent")}
	}
	stubHostCPU(t, resources.HostCPU{}, errors.New("cgroup unreadable"))
	for name, flags := range cases {
		args := append([]string{"space", "create", "cpucheck", "--workdir", workdir, "--account", "personal", "--memory", "4GiB"}, flags...)
		err := resourceCommand(t, args...)
		want := map[string]string{"invalid": "invalid --cpus", "measurement": "pass --cpus N", "adapter bound": "1–32 CPUs"}[name]
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", name, err)
		}
		if exists, err := spaceconfig.Exists("cpucheck"); err != nil || exists {
			t.Fatalf("%s left a bootstrap: %v, %v", name, exists, err)
		}
		dir, err := layout.SpaceBundleDir("cpucheck")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left space state: %v", name, err)
		}
	}
}

func TestNewBoxesDefaultToNoCPUCap(t *testing.T) {
	if got := newAgentboxCreateCmd().Flag("cpus"); got == nil || got.DefValue != "" {
		t.Fatalf("new boxes must omit a CPU cap by default: %+v", got)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, cpus := range []string{"", "0.5", "2", "0"} {
		opts := flagsTestOpts("cpu")
		opts.CPUs = cpus
		cfg, _, err := buildAgentboxConfig(t.Context(), opts)
		if err != nil || cfg.Resources.CPUs != cpus {
			t.Fatalf("declared CPUs %q became %+v, %v", cpus, cfg, err)
		}
	}
	// Cap changes and removal stay recreate drift (internal/agentbox/diff_test.go).
}

func TestCopyKeepsSourceCPUCapWithoutOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())
	for _, quota := range []string{"3", "0.5", ""} {
		src.Resources.CPUs = quota
		opts, _, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, src, "default", "dst")
		if err != nil || opts.CPUs != quota {
			t.Fatalf("copy of CPUs %q = %q, %v", quota, opts.CPUs, err)
		}
	}
}
