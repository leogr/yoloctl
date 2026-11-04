package agentbox

import (
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
	"gopkg.in/yaml.v3"
)

func TestOperatorGrantPolicyAndRecreateBoundary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, grant := range []string{"absent", "feature", "capability"} {
		t.Run(grant, func(t *testing.T) {
			cfg := &Config{Name: "worker", Space: "work", Image: "img:1", Workdir: NewEphemeralWorkdir("worker")}
			switch grant {
			case "feature":
				cfg.Features = []string{operator.Capability}
			case "capability":
				cfg.Capabilities = []string{operator.Capability}
			}
			raw, err := ComposePolicy(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var policy policyDoc
			if err := yaml.Unmarshal(raw, &policy); err != nil {
				t.Fatal(err)
			}
			enabled := grant != "absent"
			if slices.Contains(policy.FilesystemPolicy.ReadOnly, operator.HelperMount) != enabled ||
				slices.Contains(policy.FilesystemPolicy.ReadWrite, operator.Mount) != enabled {
				t.Fatal("policy disagrees with operator grant")
			}
			for _, forbidden := range []string{"/run/yoloctl", "/run/yoloctl/controller", "/var/lib/yoloctl"} {
				if slices.Contains(policy.FilesystemPolicy.ReadOnly, forbidden) || slices.Contains(policy.FilesystemPolicy.ReadWrite, forbidden) {
					t.Fatalf("operator exposes infrastructure root %s", forbidden)
				}
			}
			actual := ActualState{Exists: true, Image: cfg.Image, ReadOnlyWD: true, OperatorEnabled: !enabled, PolicyContent: raw}
			if plan := Diff(cfg, actual, cfg.Image, raw); !plan.NeedsRecreate {
				t.Fatalf("endpoint grant change only updates policy: %+v", plan)
			}
			actual.OperatorEnabled = enabled
			if plan := Diff(cfg, actual, cfg.Image, raw); plan.HasChanges() {
				t.Fatalf("unchanged operator churns: %+v", plan)
			}
		})
	}
}
