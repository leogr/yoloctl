package port

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func forwardCommandFixture(t *testing.T, units string, d *container.Details) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script, err := os.ReadFile("testdata/limactl-forward-fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limactl"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_FORWARD_DIR", dir)
	files := map[string][]byte{"units": []byte(units), "containers.json": nil, "inspect.json": []byte("[]"), "target-id": nil}
	if d != nil {
		files["target-id"] = []byte(d.ID)
		files["containers.json"], err = json.Marshal(container.Container{ID: d.ID, Name: d.Name})
		if err != nil {
			t.Fatal(err)
		}
		files["inspect.json"], err = json.Marshal([]any{map[string]any{
			"Id": d.ID, "Name": "/" + d.Name, "Path": d.Command[0], "Args": d.Command[1:],
			"Config": map[string]any{"Labels": d.Labels}, "State": map[string]any{"Running": d.Running},
			"HostConfig": map[string]any{"Privileged": d.Privileged},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func forwardCommands(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "commands.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func legacyForwardDetails() *container.Details {
	privileged := false
	return &container.Details{ID: strings.Repeat("a", 64), Name: "web", Running: true, Privileged: &privileged,
		Labels: map[string]string{"yoloctl.agentbox": "web"}, Command: sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")}
}

func TestPortServiceRemovalUsesRetainedTransportAndCurrentLegacyEvidence(t *testing.T) {
	spec := Spec{Agentbox: "web", HostPort: 8080, ContainerPort: 80}
	native, err := OpenShellUnitName(spec)
	if err != nil {
		t.Fatal(err)
	}
	legacy := UnitName(spec)
	for _, mode := range []string{"legacy", "native", "privileged", "stopped", "absent"} {
		t.Run(mode, func(t *testing.T) {
			d := legacyForwardDetails()
			switch mode {
			case "native":
				d.Command = []string{openshell.HelperBinary, openshell.HelperCommand}
			case "privileged":
				*d.Privileged = true
			case "stopped":
				d.Running = false
			case "absent":
				d = nil
			}
			dir := forwardCommandFixture(t, legacy+" loaded failed failed\n"+native+" loaded activating auto-restart\n", d)
			client := container.New("fixture")
			services, err := ListServices(t.Context(), client, "web")
			if err != nil || len(services) != 2 || services[0].Unit != native || !services[0].OpenShell || services[1].Unit != legacy || services[1].OpenShell {
				t.Fatal("mixed retained service view lost identity", services, err)
			}
			// Desired Direct deliberately disagrees with some actual routes.
			// Both exact mappings must be removed using their retained unit.
			spec.Direct = true
			if err := Unforward(t.Context(), client, spec); err != nil {
				t.Fatal(err)
			}
			commands := forwardCommands(t, dir)
			if strings.Count(commands, "systemctl stop "+native+"\n") != 1 || strings.Count(commands, "systemctl stop "+legacy+"\n") != 1 {
				t.Fatal("actual service was missed or stopped twice", commands)
			}
			if strings.Contains(commands, "nerdctl exec") != (mode == "legacy") || strings.Contains(commands, "is-active") {
				t.Fatal("cleanup entered a different runtime or substituted stop state", commands)
			}
			if mode == "legacy" && (strings.Count(commands, "nerdctl inspect web\n") != 1 || !strings.Contains(commands, "nerdctl exec "+d.ID+" iptables -t nat -D ")) {
				t.Fatal("legacy cleanup repeated lookup or lost its inspected target", commands)
			}
		})
	}
}

func TestPortNativeStopFailureRemainsUncertainThroughActualAdapter(t *testing.T) {
	unit := "yoloctl-openshell-port-web-8080-80.service"
	dir := forwardCommandFixture(t, unit+" loaded failed failed\n", nil)
	t.Setenv("YOLOCTL_TEST_FORWARD_STOP_EXIT", "23")
	err := UnforwardAgentbox(t.Context(), container.New("fixture"), "web")
	var uncertain *NativeEffectsUncertain
	var exit *exec.ExitError
	// A failed stop reply with a live context is a plain error, never hidden
	// and never uncertain (only a lost reply is).
	if err == nil || errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("failed stop result was hidden or misclassified", err)
	}
	commands := forwardCommands(t, dir)
	if strings.Count(commands, "systemctl stop "+unit+"\n") != 1 || strings.Contains(commands, "is-active") || strings.Contains(commands, "nerdctl") {
		t.Fatal("stop retried, state substituted, or native cleanup entered a box", commands)
	}
}

func TestPortRemovalRefusesUnavailableOrMalformedInventoryBeforeEffects(t *testing.T) {
	for _, raw := range []string{"yoloctl-port-web-08080-80.service\n", "yoloctl-openshell-port-web-8080-80.service\n"} {
		t.Run(strings.TrimSpace(raw), func(t *testing.T) {
			dir := forwardCommandFixture(t, raw, nil)
			if strings.HasPrefix(raw, OpenShellUnitPrefix) {
				// A usable view must not replace the separately required
				// complete inventory before native effects.
				t.Setenv("YOLOCTL_TEST_FORWARD_LIST_FAIL_AT", "2")
			}
			if err := UnforwardAgentbox(t.Context(), container.New("fixture"), "web"); err == nil {
				t.Fatal("uncertain inventory was treated as empty")
			}
			if commands := forwardCommands(t, dir); strings.Contains(commands, "systemctl stop") || strings.Contains(commands, "nerdctl") {
				t.Fatal("effect preceded inventory validation", commands)
			}
		})
	}
}

func TestPortLegacyCleanupRefusesUnknownOrMixedRuntime(t *testing.T) {
	for _, mode := range []string{"unknown", "mixed", "missing-privilege", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			d := legacyForwardDetails()
			switch mode {
			case "unknown":
				d.Command = []string{"/bin/sleep", "infinity"}
			case "mixed":
				d.Labels[openshell.LabelProtocol] = openshell.HelperProtocol
			case "missing-privilege":
				d.Privileged = nil
			case "wrong-owner":
				d.Labels["yoloctl.agentbox"] = "other"
			}
			dir := forwardCommandFixture(t, "yoloctl-port-web-8080-80.service\n", d)
			if err := UnforwardAgentbox(t.Context(), container.New("fixture"), "web"); err == nil {
				t.Fatal("unknown current runtime accepted for legacy cleanup")
			}
			commands := forwardCommands(t, dir)
			if strings.Contains(commands, "nerdctl exec") || strings.Contains(commands, "systemctl stop") {
				t.Fatal("effect in an unproved current runtime", commands)
			}
		})
	}
}
