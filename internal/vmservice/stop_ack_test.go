package vmservice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// This selects the production Lima command adapter with a local executable
// fixture, never limactl/VM/systemctl itself. The actual argv path is tested.
func limaStopFixture(t *testing.T, state, exit string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script, err := os.ReadFile("testdata/limactl-stop-fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limactl"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "commands.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_SERVICE_LOG", log)
	t.Setenv("YOLOCTL_TEST_SERVICE_STATE", state)
	t.Setenv("YOLOCTL_TEST_STOP_EXIT", exit)
	return log
}

func TestStopAcknowledgedPreservesFailureDespiteCoarseLimaState(t *testing.T) {
	const name = "yoloctl-openshell-port-fixture-8080-80.service"
	for _, state := range []string{"inactive", "failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			log := limaStopFixture(t, state, "23")
			client := container.New("fixture")
			// Control: the unchanged legacy adapter really suppresses this
			// failed stop using the later state. This is the R1 regression.
			if err := Stop(t.Context(), client, name); err != nil {
				t.Fatal("legacy control did not exercise state fallback", err)
			}
			if err := os.WriteFile(log, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			err := StopAcknowledged(t.Context(), client, name)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatal("actual failed stop acknowledgement was suppressed", err)
			}
			commands, readErr := os.ReadFile(log)
			if readErr != nil || strings.Count(string(commands), "systemctl stop ") != 1 || strings.Contains(string(commands), "is-active") {
				t.Fatal("stop repeated or later state substituted", string(commands), readErr)
			}
		})
	}
}

func TestStopAcknowledgedPreservesSuccessfulLimaAck(t *testing.T) {
	log := limaStopFixture(t, "unknown", "0")
	if err := StopAcknowledged(t.Context(), container.New("fixture"), "yoloctl-openshell-port-fixture-8080-80.service"); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || strings.Count(string(commands), "systemctl stop ") != 1 || strings.Contains(string(commands), "is-active") {
		t.Fatal("successful acknowledgement changed", string(commands), err)
	}
}

func TestNativePortStartRetainsFailedUnitForAcknowledgedCleanup(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "OpenShell"}[native], func(t *testing.T) {
			log := limaStopFixture(t, "failed", "0")
			name := "yoloctl-port-fixture-8080-80.service"
			if native {
				name = "yoloctl-openshell-port-fixture-8080-80.service"
			}
			if err := Start(t.Context(), container.New("fixture"), Unit{Name: name, Description: "fixture", Restart: true, Args: []string{"/unused-fixture"}}); err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(commands), "systemd-run --unit "+name) || strings.Contains(string(commands), "--collect") == native {
				t.Fatal("new failed-unit ownership lost or legacy collection changed", string(commands), err)
			}
		})
	}
}
