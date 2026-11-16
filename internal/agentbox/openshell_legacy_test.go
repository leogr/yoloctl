package agentbox

import (
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Route classification from actual inspected evidence: a pre-native confined
// launch is legacy; native (complete or mixed evidence) and privileged never
// are, and the retired binary is not consulted to refuse.
func TestLegacyRuntimeClassifiesOnlyPreNativeConfinedLaunches(t *testing.T) {
	legacy := &container.Details{Command: sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")}
	native := &container.Details{Command: []string{sandbox.EntrypointShimPath, openshell.HelperBinary, openshell.HelperCommand}, Labels: map[string]string{openshell.LabelProtocol: "1"}}
	mixed := &container.Details{Command: legacy.Command, Labels: map[string]string{openshell.LabelControlRoot: "/x"}}
	privileged := *legacy
	yes := true
	privileged.Privileged = &yes
	for _, c := range []struct {
		name    string
		details *container.Details
		want    bool
	}{{"legacy confined", legacy, true}, {"native", native, false}, {"mixed native evidence", mixed, false}, {"privileged", &privileged, false}} {
		got, err := LegacyRuntime(c.details)
		if err != nil || got != c.want {
			t.Fatalf("%s: legacy=%v err=%v", c.name, got, err)
		}
	}
	if _, err := LegacyRuntime(nil); err == nil {
		t.Fatal("missing evidence classified")
	}
	e := &LegacyRuntimeError{Space: "work", Box: "coder"}
	if e.Error() != "box coder predates the native runtime and cannot be started or restarted" || e.RecreateCommand() != "yoloctl box recreate coder --space work" {
		t.Fatal(e.Error(), e.RecreateCommand())
	}
}

func TestPrivilegedRetirementPreservesExtraAndSimilarMounts(t *testing.T) {
	c := privilegedConfig()
	extra := []Mount{{Source: "/workdir/project", Target: "/project", ReadOnly: true}, {Source: "/workdir/custom", Target: sandbox.BinaryDirContainerPath + "-other", ReadOnly: true}}
	c.Mounts = extra
	opts, err := buildRunOptions(c, "img", &EndpointBinary{SHA256: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range opts.Mounts {
		if mount.Source == sandbox.BinaryDirHostPath || mount.Source == sandbox.PolicyHostRegoPath {
			t.Fatal("privileged launch still requires retired VM runtime", mount)
		}
	}
	for _, wanted := range extra {
		found := false
		for _, mount := range opts.Mounts {
			if mount.Source == wanted.Source && mount.Destination == wanted.Target && mount.ReadOnly == wanted.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Fatal("unrelated configured mount removed", wanted)
		}
	}
}
