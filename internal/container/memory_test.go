package container

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/resources"
)

func TestManagedRunRequiresPoolAndPreservesExplicitLimit(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("LIMA_HOME", t.TempDir())
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			t.Setenv("MEMORY_TEST_CALLS", log)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			budget, _ := resources.Workloads(4 * resources.GiB)
			reply := fmt.Sprintf("printf '%d\\nmax\\n0\\nMemTotal: 4194304 kB\\nMemAvailable: 100 kB\\n'", budget.Max)
			if missing {
				reply = "exit 1"
			}
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MEMORY_TEST_CALLS\"\ncase \"$*\" in\n*'cat /sys/fs/cgroup/'*) " + reply + ";;\n*'nerdctl --cgroup-manager systemd run --cgroup-parent yoloctl-workloads.slice'*) exit 0;;\n*) exit 37;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := New("trial")
			c.stdout, c.stderr = io.Discard, io.Discard
			for _, memory := range []string{"shared", "2g"} {
				err := c.Run(t.Context(), RunOptions{Name: "probe", Image: "test", Memory: memory, WorkloadPool: true})
				if (err != nil) != missing {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				if strings.Contains(string(raw), "nerdctl") {
					t.Fatal("unprotected workload launched", string(raw))
				}
			} else if strings.Contains(string(raw), "--memory shared") || strings.Count(string(raw), "--memory") != 1 || !strings.Contains(string(raw), "--memory 2147483648") {
				t.Fatal(string(raw))
			}
		})
	}
}

func TestInspectMemoryLimitsAndPoolCounters(t *testing.T) {
	d, err := parseInspect([]byte(`[{"Name":"box","HostConfig":{"Memory":2147483648,"CpuQuota":250000,"CpuPeriod":100000}}]`), "box")
	if err != nil || d.Memory != "2147483648" || d.CPUs != "2.5" {
		t.Fatal(d, err)
	}
	d, err = parseInspect([]byte(`[{"HostConfig":{"Memory":0,"CpuQuota":-1}}]`), "box")
	if err != nil || d.Memory != "0" || d.CPUs != "0" {
		t.Fatal(d, err)
	}
	m, err := parseWorkloadMemory("100\n200\n2700\n3000\nlow 0\nhigh 2\nmax 3\noom 1\noom_kill 1\noom_group_kill 0\n")
	if err != nil || m.Current != 100 || m.Peak != 200 || m.High == nil || *m.High != 2700 || m.Max != 3000 || m.Events["oom_kill"] != 1 {
		t.Fatal(m, err)
	}
	m, err = parseWorkloadMemory("100\n200\nmax\n3000\nhigh 0\noom_kill 2\n")
	if err != nil || m.High != nil || m.Max != 3000 || m.Events["oom_kill"] != 2 {
		t.Fatal("disabled throttle is not an unavailable pool", m, err)
	}
	encoded, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(encoded), `"high_bytes":null`) {
		t.Fatal("disabled throttle must be explicit in JSON", string(encoded), err)
	}
	for _, invalid := range []string{"", "1\n2\nmax\nmax\noom_kill 1\n", "1\n2\n-1\n4\noom_kill 1\n", "1\n2\nmax\n4\noom 1\n"} {
		if _, err := parseWorkloadMemory(invalid); err == nil {
			t.Fatal("accepted invalid counters", invalid)
		}
	}
}

func TestBuilderWaitsForLimaServiceRecovery(t *testing.T) {
	for _, failure := range []string{"", "start", "probe"} {
		t.Run("failure="+failure, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("LIMA_HOME", t.TempDir())
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			t.Setenv("MEMORY_TEST_CALLS", log)
			t.Setenv("MEMORY_TEST_FAILURE", failure)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			budget, _ := resources.Workloads(4 * resources.GiB)
			script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$MEMORY_TEST_CALLS"
case "$*" in
*'cat /sys/fs/cgroup/'*) printf '%d\nmax\n0\nMemTotal: 4194304 kB\nMemAvailable: 100 kB\n';;
*'systemctl start buildkit.service'*) test "$MEMORY_TEST_FAILURE" != start;;
*'buildctl debug workers'*) test "$MEMORY_TEST_FAILURE" != probe;;
*) exit 37;;
esac
`, budget.Max)
			if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := New("trial")
			c.stdout, c.stderr = io.Discard, io.Discard
			err := c.EnsureBuilder(t.Context())
			if (err != nil) != (failure != "") {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(raw)
			start := strings.Index(calls, "systemctl start buildkit.service")
			probe := strings.Index(calls, "buildctl debug workers")
			if start < 0 || (failure == "start" && probe >= 0) || (failure != "start" && probe <= start) {
				t.Fatal("did not wait for service before probing", calls)
			}
			if strings.Contains(calls, "restart buildkit") {
				t.Fatal("must not restart an active builder", calls)
			}
		})
	}
}
