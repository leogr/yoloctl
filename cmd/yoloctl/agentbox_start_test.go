package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Drive both Cobra commands through their real preflight, inspector and
// reconciliation paths. Only the VM transport and shared stop operations are
// substituted; forbidden container replacement fails the test even after start.
func TestBoxStartAndRestartPreserveExistingContainer(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		for _, drift := range []string{"before start", "after start", "none"} {
			t.Run(operation+"/"+drift, func(t *testing.T) {
				callsPath := setupExistingStartFixture(t, drift)
				stops := recordStartFixtureStops(t)
				cmd := newAgentboxStartCmd()
				if operation == "restart" {
					cmd = newAgentboxRestartCmd()
				}
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				cmd.SetArgs([]string{"coder", "--space", "default", "--no-attach", "--no-autoupdate"})
				err := cmd.Execute()
				if drift == "none" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "requires recreation (image:") || !strings.Contains(err.Error(), "box recreate coder --space default") {
					t.Fatalf("did not return actionable replacement refusal: %v; output=%s", err, &output)
				}
				if drift == "after start" && !strings.Contains(err.Error(), "post-start reconcile") {
					t.Fatalf("late refusal was not propagated: %v", err)
				}
				raw, readErr := os.ReadFile(callsPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				calls := string(raw)
				for _, forbidden := range []string{"nerdctl rm", "nerdctl run", "nerdctl build", "nerdctl stop", "npm install"} {
					if strings.Contains(calls, forbidden) {
						t.Fatalf("start replaced or updated container: %s", calls)
					}
				}
				if got, want := strings.Contains(calls, "nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"), drift != "before start"; got != want {
					t.Fatalf("started=%v want=%v; calls=%s", got, want, calls)
				}
				if strings.Contains(calls, "nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff") {
					verified := strings.Index(calls, "binary=/opt/openshell/bin/openshell-sandbox")
					if verified < 0 || verified > strings.Index(calls, "nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff") {
						t.Fatalf("legacy start skipped retained-byte verification: %s", calls)
					}
				}
				if operation == "restart" && drift != "before start" {
					if !slices.Equal(*stops, []string{"holder", "ports", "container", "session", "netns"}) {
						t.Fatalf("lost session-aware stop ordering: %v", *stops)
					}
				} else if len(*stops) != 0 {
					t.Fatalf("refusal/start tore down live session: %v", *stops)
				}
			})
		}
	}
}

func setupExistingStartFixture(t *testing.T, drift string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	workdir := t.TempDir()
	saveSpaceForTest(t, "default", workdir)
	cfg := &agentbox.Config{
		Name: "coder", Space: "default", Agent: "none", Account: "none", Image: "img:desired",
		Workdir:      agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: workdir, Canonical: workdir},
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	withCompletionResolver(t, []container.Container{{Name: cfg.Name, Image: cfg.Image, Status: "Exited (0)"}})
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("START_TEST_FIXTURE", bin)
	t.Setenv("START_TEST_DRIFT", drift)
	command := sandbox.EntrypointArgs(cfg.Workdir.ContainerPath(), false, "sleep", "infinity")
	for name, image := range map[string]string{"same.json": cfg.Image, "drift.json": "img:old"} {
		raw, err := json.Marshal([]any{map[string]any{
			"Id": strings.Repeat("f", 64), "Name": cfg.Name, "Path": command[0], "Args": command[1:],
			"Config": map[string]any{"Image": image, "Labels": map[string]string{agentbox.LabelAgent: "none", resources.WorkloadLabel: resources.WorkloadVersion}},
			"Mounts": []any{map[string]any{"Source": workdir, "Destination": cfg.Workdir.ContainerPath(), "RW": true}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "policy.yaml"), policy, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$START_TEST_FIXTURE/calls"
case "$*" in
  *'nerdctl ps'*) printf '{"Names":"coder","Status":"Exited (0)"}\n' ;;
  *'nerdctl inspect'*)
    if [ "$START_TEST_DRIFT" = 'before start' ] || { [ "$START_TEST_DRIFT" = 'after start' ] && [ -f "$START_TEST_FIXTURE/started" ]; }; then
      cat "$START_TEST_FIXTURE/drift.json"
    else
      cat "$START_TEST_FIXTURE/same.json"
    fi ;;
  *'sudo cat'*) cat "$START_TEST_FIXTURE/policy.yaml" ;;
  *'binary=/opt/openshell/bin/openshell-sandbox'*'sha256sum --check --status'*) exit 0 ;;
  *'nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'*) touch "$START_TEST_FIXTURE/started" ;;
  *' test -w '*|*'sudo mkdir -p '*|*'sudo rm -rf '*|*'sudo python3 -c '*|*'systemctl list-units'*) exit 0 ;;
  *) exit 42 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(bin, "calls")
}

func recordStartFixtureStops(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	oldHolder, oldPorts, oldContainer := lifecycleStopHolderUnit, lifecycleUnforwardAgentbox, lifecycleStopAgentboxContainer
	oldSession, oldNetns := lifecycleCleanupStoppedSessionState, lifecycleReapOrphanedNetns
	record := func(name string) func(context.Context, *container.Client, string) error {
		return func(context.Context, *container.Client, string) error {
			calls = append(calls, name)
			return nil
		}
	}
	lifecycleStopHolderUnit = record("holder")
	lifecycleUnforwardAgentbox = record("ports")
	lifecycleStopAgentboxContainer = record("container")
	lifecycleCleanupStoppedSessionState = record("session")
	lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error {
		calls = append(calls, "netns")
		return nil
	}
	t.Cleanup(func() {
		lifecycleStopHolderUnit, lifecycleUnforwardAgentbox, lifecycleStopAgentboxContainer = oldHolder, oldPorts, oldContainer
		lifecycleCleanupStoppedSessionState, lifecycleReapOrphanedNetns = oldSession, oldNetns
	})
	return &calls
}
