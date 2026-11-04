package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestNativePolicyReadRouteNeverFallsBackAfterNativeSignalsOrFailure(t *testing.T) {
	for _, kind := range []string{"native", "native-read-failure", "missing-label", "missing-bind", "legacy-argv-native-label", "privileged-native", "legacy", "nil"} {
		t.Run(kind, func(t *testing.T) {
			d := openShellDetailsFixture(t)
			cause := errors.New("native storage unavailable")
			switch kind {
			case "missing-label":
				delete(d.Labels, openshell.LabelQualification)
			case "missing-bind":
				d.Mounts = d.Mounts[1:]
			case "legacy-argv-native-label":
				d.Command = sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")
			case "privileged-native":
				*d.Privileged = true
			case "legacy":
				d.Command = sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")
				d.Labels = nil
				d.Mounts = nil
			case "nil":
				d = nil
			}
			native, legacy := 0, 0
			policy, err := readInspectedRuntimePolicy(t.Context(), d, func(context.Context) ([]byte, error) {
				native++
				if kind == "native-read-failure" {
					return nil, cause
				}
				return []byte("native-selected-policy"), nil
			}, func(context.Context) ([]byte, error) { legacy++; return []byte("legacy-policy"), nil })
			switch kind {
			case "native":
				if err != nil || native != 1 || legacy != 0 || string(policy) != "native-selected-policy" {
					t.Fatal("native policy route lost selected data", err, native, legacy)
				}
			case "native-read-failure":
				if !errors.Is(err, cause) || native != 1 || legacy != 0 || policy != nil {
					t.Fatal("native failure fell back to legacy policy", err, native, legacy)
				}
			case "legacy":
				if err != nil || native != 0 || legacy != 1 || string(policy) != "legacy-policy" {
					t.Fatal("legacy policy route changed", err, native, legacy)
				}
			default:
				if err == nil || native != 0 || legacy != 0 || policy != nil {
					t.Fatal("mixed policy evidence authorized a fallback/read", err, native, legacy)
				}
			}
		})
	}
}

func TestNativePolicyReadRouteRequiresCompleteCallbacksAndLiveContext(t *testing.T) {
	called := 0
	read := func(context.Context) ([]byte, error) { called++; return nil, nil }
	d := openShellDetailsFixture(t)
	for _, callbacks := range [][2]func(context.Context) ([]byte, error){{nil, read}, {read, nil}} {
		if _, err := readInspectedRuntimePolicy(t.Context(), d, callbacks[0], callbacks[1]); err == nil || called != 0 {
			t.Fatal("incomplete policy route reached IO", err, called)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readInspectedRuntimePolicy(ctx, d, read, read); !errors.Is(err, context.Canceled) || called != 0 {
		t.Fatal("cancelled policy route reached IO", err, called)
	}
}

func TestNativePolicyReadActualLiveInspectorRefusesLegacyFileFallback(t *testing.T) {
	c := validConfig()
	d := openShellDetailsFixture(t)
	d.Name = c.Name
	d.ID = strings.Repeat("a", 64)
	inspector := fixtureLiveInspector(t, c, d.Command, nil)
	var mounts []map[string]any
	for _, mount := range d.Mounts {
		mounts = append(mounts, map[string]any{"Source": mount.Source, "Destination": mount.Destination, "RW": !mount.ReadOnly})
	}
	raw, err := json.Marshal([]any{map[string]any{"Id": d.ID, "Name": d.Name, "Path": d.Command[0], "Args": d.Command[1:], "Config": map[string]any{"Image": mustResolveImage(t, c), "Labels": d.Labels}, "HostConfig": map[string]any{"Privileged": false}, "Mounts": mounts}})
	if err != nil {
		t.Fatal(err)
	}
	bin := os.Getenv("WORKDIR_TEST_FIXTURE")
	if err := os.WriteFile(filepath.Join(bin, "inspect.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKDIR_TEST_FIXTURE/commands.log"
case "$*" in
 *'nerdctl ps'*) printf '{"Names":"coder","Status":"Up"}\n' ;;
 *'nerdctl inspect'*) cat "$WORKDIR_TEST_FIXTURE/inspect.json" ;;
 *'sudo cat'*) cat "$WORKDIR_TEST_FIXTURE/policy.yaml" ;;
 *'systemctl list-units'*) exit 0 ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := inspector.Inspect(t.Context(), c)
	commands, readErr := os.ReadFile(filepath.Join(bin, "commands.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil || !strings.Contains(err.Error(), "read policy file") || state.PolicyContent != nil || strings.Contains(string(commands), "sudo cat") || strings.Contains(string(commands), "systemctl list-units") {
		t.Fatal("actual LiveInspector used legacy fallback for native policy", err, string(commands))
	}
}
