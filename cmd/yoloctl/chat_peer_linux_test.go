//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// The chat peer check finds a box's process in the workload pool of the
// guest's own service manager: the systemd slice on Lima, the microvm group
// on Firecracker. Before, it always used the microvm group, so every Lima peer
// was refused as outside the pool.
func TestChatPeerUsesTheGuestWorkloadPool(t *testing.T) {
	id := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	proc := t.TempDir()
	for pid, cgroup := range map[string]string{
		"11": "0::/yoloctl.slice/" + resources.WorkloadSlice + "/nerdctl-" + id + ".scope\n",
		"12": "0::" + resources.WorkloadGroup + "/" + id + "\n",
		"13": "0::/yoloctl.slice/" + resources.WorkloadSlice + "/nerdctl-" + other + ".scope\n",
		"14": "0::/system.slice/sshd.service\n",
	} {
		if err := os.MkdirAll(filepath.Join(proc, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, pid, "cgroup"), []byte(cgroup), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		manager string
		pid     int
		ok      bool
		why     string
	}{
		{"systemd", 11, true, "a Lima box process in the systemd slice"},
		{"", 11, true, "an unset manager is systemd"},
		{"microvm", 12, true, "a Firecracker box process in the microvm group"},
		{"microvm", 11, false, "the slice is outside the microvm pool"},
		{"systemd", 12, false, "the microvm group is outside the systemd pool"},
		{"systemd", 13, false, "another container's process"},
		{"systemd", 14, false, "a process outside any box"},
	} {
		_, err := chatPeerCollector(tc.manager, proc, t.TempDir()).BoxCgroup(spacetelemetry.Target{PID: tc.pid, ID: id})
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.why, err)
		}
	}
}

func TestWorkloadPoolFollowsTheServiceManager(t *testing.T) {
	if got := workloadPoolRel("microvm"); got != strings.TrimPrefix(resources.WorkloadGroup, "/") {
		t.Fatalf("microvm pool %q", got)
	}
	for _, manager := range []string{"systemd", ""} {
		if got := workloadPoolRel(manager); got != "yoloctl.slice/"+resources.WorkloadSlice {
			t.Fatalf("%q pool %q", manager, got)
		}
	}
}
