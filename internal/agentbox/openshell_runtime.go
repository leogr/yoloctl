package agentbox

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

// openShellRuntimeEvidence checks recorded launch, versioned exact-build
// binds and separate OpenShell labels together. Native disk/ownership and
// artifact validation still happen at launch/readiness; these labels grant
// no authority and must never use the privileged endpoint's launcher keys.
func openShellRuntimeEvidence(d *container.Details) (openshell.HelperLaunch, bool) {
	args := d.Command
	if len(args) > 0 && args[0] == "/usr/local/bin/yoloctl-entrypoint" {
		args = args[1:]
	}
	if len(args) < 3 || args[0] != openshell.HelperBinary || args[1] != openshell.HelperCommand {
		return openshell.HelperLaunch{}, false
	}
	launch, err := openshell.ParseHelperLaunch(args[2:])
	if err != nil {
		return openshell.HelperLaunch{}, false
	}
	for key, want := range map[string]string{
		openshell.LabelProtocol: launch.Protocol, openshell.LabelQualification: launch.Qualification,
		openshell.LabelHelperSHA256: launch.HelperSHA256, openshell.LabelIncarnation: launch.Incarnation,
	} {
		if d.Labels[key] != want {
			return openshell.HelperLaunch{}, false
		}
	}
	controlRoot := d.Labels[openshell.LabelControlRoot]
	if len(controlRoot) > 4096 || !path.IsAbs(controlRoot) || path.Clean(controlRoot) != controlRoot || !strings.HasSuffix(controlRoot, "/runtime/openshell/containers/"+launch.Incarnation) {
		return openshell.HelperLaunch{}, false
	}
	artifacts, _ := openshell.RuntimeDirectory(launch.Qualification)
	helper, _ := openshell.HelperSourceDirectory(launch.HelperSHA256)
	want := map[string]container.DetailMount{
		openshell.HelperDirectory:    {Source: helper, Destination: openshell.HelperDirectory, ReadOnly: true},
		openshell.ArtifactProjection: {Source: artifacts, Destination: openshell.ArtifactProjection, ReadOnly: true},
		openshell.NativeProjection:   {Source: controlRoot, Destination: openshell.NativeProjection, ReadOnly: false},
	}
	seen := map[string]bool{}
	for _, mount := range d.Mounts {
		if expected, ok := want[mount.Destination]; ok {
			if seen[mount.Destination] || !sameNativeMount(mount, expected) {
				return openshell.HelperLaunch{}, false
			}
			seen[mount.Destination] = true
		} else if mount.Destination == "/.openshell" || strings.HasPrefix(mount.Destination, "/.openshell/") || strings.HasPrefix(mount.Destination, openshell.HelperDirectory+"/") {
			return openshell.HelperLaunch{}, false
		}
	}
	return launch, len(seen) == len(want)
}

func hasOpenShellRuntimeSignals(d *container.Details) bool {
	for _, key := range []string{openshell.LabelProtocol, openshell.LabelQualification, openshell.LabelHelperSHA256, openshell.LabelIncarnation, openshell.LabelControlRoot} {
		if _, ok := d.Labels[key]; ok {
			return true
		}
	}
	for _, arg := range d.Command {
		if arg == openshell.HelperBinary || arg == openshell.HelperCommand {
			return true
		}
	}
	for _, mount := range d.Mounts {
		if mount.Destination == openshell.NativeProjection || mount.Destination == openshell.ArtifactProjection || mount.Destination == openshell.HelperDirectory {
			return true
		}
	}
	return false
}

// sameNativeMount compares a reported bind with the expected one. Engines may
// report a symlinked or aliased VM path (Lima's data disk, for example), so
// sources also match when both resolve to the same real path.
func sameNativeMount(got, want container.DetailMount) bool {
	return got.Destination == want.Destination && got.ReadOnly == want.ReadOnly && sameNativePath(got.Source, want.Source)
}

func sameNativePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}
