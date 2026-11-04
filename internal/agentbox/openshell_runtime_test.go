package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func openShellDetailsFixture(t *testing.T) *container.Details {
	t.Helper()
	launch := openshell.HelperLaunch{Protocol: openshell.HelperProtocol, Workdir: "/workdir/home/me/project", Qualification: strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), Incarnation: strings.Repeat("c", 32)}
	command, err := launch.Args()
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := openshell.HelperSourceDirectory(launch.HelperSHA256)
	artifacts, _ := openshell.RuntimeDirectory(launch.Qualification)
	control := "/mnt/native/runtime/openshell/containers/" + launch.Incarnation
	privileged := false
	return &container.Details{Command: append([]string{sandbox.EntrypointShimPath}, command...), Privileged: &privileged,
		Labels: map[string]string{openshell.LabelProtocol: launch.Protocol, openshell.LabelQualification: launch.Qualification, openshell.LabelHelperSHA256: launch.HelperSHA256, openshell.LabelIncarnation: launch.Incarnation, openshell.LabelControlRoot: control},
		Mounts: []container.DetailMount{{Source: helper, Destination: openshell.HelperDirectory, ReadOnly: true}, {Source: artifacts, Destination: openshell.ArtifactProjection, ReadOnly: true}, {Source: control, Destination: openshell.NativeProjection}, {Source: "/host/project", Destination: launch.Workdir, ReadOnly: true}},
	}
}

func TestOpenShellHelperClassificationRequiresCompleteConsistentEvidence(t *testing.T) {
	d := openShellDetailsFixture(t)
	if evidence := ClassifyRuntime(d); evidence.Mode != RuntimeModeConfined || evidence.AnyPrivileged || evidence.Protocol != openshell.HelperProtocol {
		t.Fatal(evidence)
	}
	index, err := liveWorkdirMount(d)
	if err != nil || index != 3 {
		t.Fatal("lost helper's real workdir", index, err)
	}
	for name, mutate := range map[string]func(*container.Details){
		"missing-runtime-privilege": func(d *container.Details) { d.Privileged = nil },
		"runtime-is-privileged":     func(d *container.Details) { *d.Privileged = true },
		"wrong-qualification":       func(d *container.Details) { d.Labels[openshell.LabelQualification] = strings.Repeat("d", 64) },
		"missing-incarnation":       func(d *container.Details) { delete(d.Labels, openshell.LabelIncarnation) },
		"unsigned-image-helper":     func(d *container.Details) { d.Mounts = d.Mounts[1:] },
		"writable-helper":           func(d *container.Details) { d.Mounts[0].ReadOnly = false },
		"mutable-artifacts":         func(d *container.Details) { d.Mounts[1].Source = "/opt/openshell/bin" },
		"issuer-exposed": func(d *container.Details) {
			d.Mounts[2].Source = "/mnt/native/runtime/openshell"
			d.Labels[openshell.LabelControlRoot] = d.Mounts[2].Source
		},
		"duplicate-bind": func(d *container.Details) { d.Mounts = append(d.Mounts, d.Mounts[0]) },
		"extra-auth-bind": func(d *container.Details) {
			d.Mounts = append(d.Mounts, container.DetailMount{Source: "/tmp/auth", Destination: openshell.NativeProjection + "/current.json"})
		},
		"legacy-argv-new-labels": func(d *container.Details) { d.Command = sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity") },
		"incomplete-flags":       func(d *container.Details) { d.Command = d.Command[:len(d.Command)-1] },
	} {
		t.Run(name, func(t *testing.T) {
			d := openShellDetailsFixture(t)
			mutate(d)
			if evidence := ClassifyRuntime(d); evidence.Mode != RuntimeModeUnknown {
				t.Fatal("inconsistent runtime was adopted", evidence)
			}
		})
	}
}

func TestOpenShellLabelsNeverImplyPrivilegeAndLegacyRouteSurvives(t *testing.T) {
	d := openShellDetailsFixture(t)
	for _, key := range []string{openshell.LabelProtocol, openshell.LabelQualification, openshell.LabelHelperSHA256, openshell.LabelIncarnation, openshell.LabelControlRoot} {
		if key == LabelLauncher || key == LabelLauncherSHA256 {
			t.Fatal("OpenShell helper reused privileged launcher labels")
		}
	}
	if ClassifyRuntime(d).AnyPrivileged {
		t.Fatal("confined infrastructure claims privilege")
	}
	d.Command = sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")
	d.Labels = nil
	d.Mounts = nil
	if evidence := ClassifyRuntime(d); evidence.Mode != RuntimeModeConfined || evidence.AnyPrivileged {
		t.Fatal("legacy launch was lost", evidence)
	}
}
