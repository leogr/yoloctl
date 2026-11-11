package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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
		backend string
		host    resources.HostCPU
		want    int
		basis   string
	}{
		{"lima", resources.HostCPU{Logical: 16, Quota: 250000, Period: 100000}, 3, "bandwidth 2.5"},
		{"firecracker", resources.HostCPU{Logical: 64}, 32, "firecracker maximum 32"},
		{"lima", resources.HostCPU{Logical: 64}, 64, "64 available logical CPUs"},
		{"firecracker", resources.HostCPU{Logical: 1}, 1, "1 available logical CPUs"},
	} {
		calls := stubHostCPU(t, tc.host, nil)
		var out bytes.Buffer
		t.Cleanup(bindCLITestLogger(t, &out))
		got, err := resolveSpaceCPUs(t.Context(), tc.backend, "auto", &out)
		if err != nil || got != tc.want || *calls != 1 || !strings.Contains(out.String(), "automatic") || !strings.Contains(out.String(), tc.basis) {
			t.Fatalf("%s %+v: %d, %v, %q", tc.backend, tc.host, got, err, out.String())
		}
	}
	stubHostCPU(t, resources.HostCPU{}, errors.New("cgroup unreadable"))
	if _, err := resolveSpaceCPUs(t.Context(), "lima", "auto", io.Discard); err == nil || !strings.Contains(err.Error(), "--cpus") {
		t.Fatalf("measurement failure lacks the explicit remedy: %v", err)
	}
}

func TestExplicitSpaceCPUsNeverProbe(t *testing.T) {
	calls := stubHostCPU(t, resources.HostCPU{Logical: 2}, nil)
	var out bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &out))
	if got, err := resolveSpaceCPUs(t.Context(), "lima", "48", &out); err != nil || got != 48 || !strings.Contains(out.String(), "explicit") {
		t.Fatalf("explicit oversubscription must be kept: %d, %v, %q", got, err, out.String())
	}
	for _, value := range []string{"0", "-1", "+2", "1.5", "", " 4", "auto ", "2147483648"} {
		if _, err := resolveSpaceCPUs(t.Context(), "lima", value, io.Discard); err == nil {
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
