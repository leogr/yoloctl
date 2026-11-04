package agentbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Exercise real inspect decoding/classification and the reconcile decision. The
// fake VM accepts only inventory reads; the recording applier exposes an unwanted
// recreate without risking a real container's private home.
func TestLiveInspectorDistinguishesWorkdirFromExtraProjectMounts(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		for _, reversed := range []bool{false, true} {
			name := "host"
			if ephemeral {
				name = "ephemeral"
			}
			if reversed {
				name += "/reversed"
			}
			t.Run(name, func(t *testing.T) {
				c := validConfig()
				if !ephemeral {
					c.Workdir = Workdir{Mode: WorkdirModeHost, Source: "/host/yoloctl", Canonical: "/host/yoloctl"}
				}
				c.Mounts = []Mount{
					{Source: "/host/airc", Target: "/workdir/host/airc", ReadOnly: true},
					{Source: "/host/data", Target: c.Workdir.ContainerPath() + "/data"},
				}
				mounts := buildMounts(c)
				if reversed {
					slices.Reverse(mounts)
				}
				inspector := fixtureLiveInspector(t, c, sandbox.EntrypointArgs(c.Workdir.ContainerPath(), false, "sleep", "infinity"), mounts)
				state, err := inspector.Inspect(t.Context(), c)
				if err != nil {
					t.Fatal(err)
				}
				if state.WorkdirSource != c.Workdir.MountSource() || state.WorkdirDest != c.Workdir.ContainerPath() || state.ReadOnlyWD {
					t.Fatalf("wrong primary workdir: source=%q target=%q readonly=%v", state.WorkdirSource, state.WorkdirDest, state.ReadOnlyWD)
				}
				if reason := extraMountsRecreateReason(c.Mounts, state.Mounts); reason != "" {
					t.Fatalf("extra project mounts were lost: %s; got %+v", reason, state.Mounts)
				}
				applier := &fakeApplier{}
				result, err := Reconcile(t.Context(), c, inspector, applier)
				if err != nil || result.ContainerCreated || result.ContainerRecreated || result.ContainerRestarted || len(applier.recreateCalls) != 0 {
					t.Fatalf("unchanged multi-project box did not converge: result=%+v error=%v", result, err)
				}

				for _, test := range []struct {
					name, reason string
					mutate       func(*Config)
				}{
					{"moved workdir", "workdir source", func(desired *Config) {
						desired.Workdir = Workdir{Mode: WorkdirModeHost, Source: "/host/new", Canonical: "/host/new"}
					}},
					{"primary read only", "workdir read-only", func(desired *Config) { desired.Capabilities = nil }},
					{"removed extra", "extra mounts", func(desired *Config) { desired.Mounts = desired.Mounts[:1] }},
					{"extra read only", "extra mount", func(desired *Config) { desired.Mounts[0].ReadOnly = false }},
					{"extra source", "extra mount", func(desired *Config) { desired.Mounts[0].Source = "/host/another" }},
				} {
					t.Run(test.name, func(t *testing.T) {
						desired := *c
						desired.Mounts = slices.Clone(c.Mounts)
						test.mutate(&desired)
						// Re-inspect against the edited declaration: using its new
						// path to identify the old primary would mask real drift.
						actual, err := inspector.Inspect(t.Context(), &desired)
						if err != nil {
							t.Fatal(err)
						}
						plan := Diff(&desired, actual, state.Image, state.PolicyContent)
						if !plan.NeedsRecreate || !strings.Contains(plan.RecreateReason, test.reason) {
							t.Fatalf("lost real drift: %+v", plan)
						}
					})
				}
			})
		}
	}
}

func TestLiveInspectorUncertainWorkdirDoesNotApply(t *testing.T) {
	c := validConfig()
	command := sandbox.EntrypointArgs(c.Workdir.ContainerPath(), false, "sleep", "infinity")
	for _, test := range []struct {
		name    string
		command []string
		mounts  []container.Mount
	}{
		{"missing command", nil, buildMounts(c)},
		{"unknown command", []string{"sh", "-c", "--workdir /workdir"}, buildMounts(c)},
		{"missing bind", command, buildMounts(c)[:4]},
		{"duplicate bind", command, append(buildMounts(c), buildMounts(c)[4])},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspector := fixtureLiveInspector(t, c, test.command, test.mounts)
			applier := &fakeApplier{}
			_, err := Reconcile(t.Context(), c, inspector, applier)
			if err == nil || !strings.Contains(err.Error(), "inspect container coder workdir") {
				t.Fatalf("expected attributed inspection refusal, got %v", err)
			}
			if !reflect.DeepEqual(applier, &fakeApplier{}) {
				t.Fatalf("uncertain workdir triggered runtime mutation: %+v", applier)
			}
		})
	}
}

func TestLiveWorkdirCommandBoundaries(t *testing.T) {
	binary := sandbox.BinaryDirContainerPath + "/openshell-sandbox"
	for _, test := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"supervised child arguments ignored", sandbox.EntrypointArgs("/workdir", true, "echo", "--workdir", "/workdir/other"), false},
		{"direct sandbox command", []string{binary, "--workdir", "/workdir"}, false},
		{"no workdir", []string{binary, "--", "--workdir", "/workdir"}, true},
		{"missing value", []string{binary, "--workdir"}, true},
		{"child delimiter as value", []string{binary, "--workdir", "--"}, true},
		{"duplicate argument", []string{binary, "--workdir", "/workdir", "--workdir", "/workdir"}, true},
		{"unknown flag", []string{binary, "--unknown", "value", "--workdir", "/workdir"}, true},
		{"path outside workdir", []string{binary, "--workdir", "/workdir-else"}, true},
		{"unclean path", []string{binary, "--workdir", "/workdir/../else"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, err := liveWorkdirMount(&container.Details{Command: test.args, Mounts: []container.DetailMount{{Source: "/source", Destination: "/workdir"}}})
			if (err != nil) != test.wantErr || (!test.wantErr && index != 0) {
				t.Fatalf("index=%d error=%v", index, err)
			}
		})
	}
}

func fixtureLiveInspector(t *testing.T, c *Config, command []string, mounts []container.Mount) LiveInspector {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKDIR_TEST_FIXTURE", bin)
	var inspectedMounts []map[string]any
	for _, mount := range mounts {
		inspectedMounts = append(inspectedMounts, map[string]any{"Source": mount.Source, "Destination": mount.Destination, "RW": !mount.ReadOnly})
	}
	var executable string
	var args []string
	if len(command) > 0 {
		executable, args = command[0], command[1:]
	}
	inspect, err := json.Marshal([]any{map[string]any{
		"Name": c.Name, "Path": executable, "Args": args,
		"Config": map[string]any{
			"Image": mustResolveImage(t, c), "WorkingDir": "/unrelated-image-default",
			"Labels": map[string]string{resources.WorkloadLabel: resources.WorkloadVersion},
		},
		"Mounts": inspectedMounts,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"inspect.json": inspect,
		"policy.yaml":  mustCompose(t, c),
		"limactl": []byte(`#!/bin/sh
case "$*" in
  *'nerdctl ps'*) printf '{"Names":"coder","Status":"Up"}\n' ;;
  *'nerdctl inspect'*) cat "$WORKDIR_TEST_FIXTURE/inspect.json" ;;
  *'sudo cat'*) cat "$WORKDIR_TEST_FIXTURE/policy.yaml" ;;
  *'systemctl list-units'*) exit 0 ;;
  *) exit 42 ;;
esac
`),
	} {
		if err := os.WriteFile(filepath.Join(bin, name), raw, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return LiveInspector{Client: container.New(c.Space)}
}
