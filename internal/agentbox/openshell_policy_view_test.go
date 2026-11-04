package agentbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestRuntimePolicyViewNeverHashesLegacyMirrorForNativeFailure(t *testing.T) {
	for _, kind := range []string{"native", "mixed-native", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			cfg := validConfig()
			inspector := fixtureLiveInspector(t, cfg, sandbox.EntrypointArgs(cfg.Workdir.ContainerPath(), false, "sleep", "infinity"), buildMounts(cfg))
			root := os.Getenv("WORKDIR_TEST_FIXTURE")
			if err := os.WriteFile(filepath.Join(root, "policy.yaml"), []byte("old-legacy-policy"), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "policy-view.log")
			t.Setenv("YOLOCTL_POLICY_VIEW_LOG", log)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_POLICY_VIEW_LOG"
case "$*" in
 *'sudo cat'*) cat "$WORKDIR_TEST_FIXTURE/policy.yaml" ;;
 *) exit 42 ;;
esac
`
			if err := os.WriteFile(filepath.Join(root, "limactl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			details := openShellDetailsFixture(t)
			details.Name = cfg.Name
			details.ID = strings.Repeat("a", 64)
			if kind == "mixed-native" {
				delete(details.Labels, openshell.LabelQualification)
			}
			if kind == "legacy" {
				details = &container.Details{Name: cfg.Name}
			}
			got, err := inspector.ReadRuntimePolicy(t.Context(), details)
			commands, readErr := os.ReadFile(log)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if kind == "legacy" {
				if err != nil || string(got) != "old-legacy-policy" || strings.Count(string(commands), "sudo cat") != 1 {
					t.Fatal("legacy policy view changed", err, string(commands))
				}
			} else if err == nil || got != nil || strings.Contains(string(commands), "sudo cat") {
				t.Fatal("native unavailable state adopted stale legacy policy", err, string(commands))
			}
		})
	}
}

func TestRuntimePolicyViewRejectsMissingEvidenceAndCancellation(t *testing.T) {
	for _, details := range []*container.Details{nil, {Name: ""}} {
		if _, err := (LiveInspector{}).ReadRuntimePolicy(t.Context(), details); err == nil {
			t.Fatal("missing evidence accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := validConfig()
	inspector := fixtureLiveInspector(t, cfg, nil, nil)
	if _, err := inspector.ReadRuntimePolicy(ctx, &container.Details{Name: cfg.Name}); err != context.Canceled {
		t.Fatal("cancellation reached IO", err)
	}
}
