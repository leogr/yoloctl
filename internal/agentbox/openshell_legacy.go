package agentbox

import (
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// LegacyRuntimeError refuses a new run of a box whose existing container runs
// the pre-native OpenShell runtime: such a box is never started or restarted
// again, and explicit recreation is its only route to a new run. It names the
// inspected box only and grants nothing; it does not need the retired binary.
type LegacyRuntimeError struct{ Space, Box string }

func (e *LegacyRuntimeError) Error() string {
	return "box " + e.Box + " predates the native runtime and cannot be started or restarted"
}

// RecreateCommand is the one command that moves the box to the native runtime.
func (e *LegacyRuntimeError) RecreateCommand() string {
	return "yoloctl box recreate " + e.Box + " --space " + e.Space
}

// LegacyRuntime reports whether inspected evidence is a pre-native confined
// launch: an existing container with no native runtime signals that is not a
// privileged launch. Mixed or incomplete native evidence stays native, and a
// privileged box uses yoloctl's own endpoint, never this classification.
func LegacyRuntime(details *container.Details) (bool, error) {
	if details == nil {
		return false, errors.New("legacy runtime classification lacks inspected evidence")
	}
	if hasOpenShellRuntimeSignals(details) {
		return false, nil
	}
	if details.Privileged != nil && *details.Privileged || ClassifyRuntime(details).AnyPrivileged {
		return false, nil
	}
	return true, nil
}

// Privileged boxes use yoloctl's own endpoint. They must not acquire binds to
// retired OpenShell programs/rego which new VM images intentionally omit.
func withoutLegacyOpenShellBinds(mounts []container.Mount) []container.Mount {
	result := make([]container.Mount, 0, len(mounts))
	for _, m := range mounts {
		if m.ReadOnly && (m.Source == sandbox.BinaryDirHostPath && m.Destination == sandbox.BinaryDirContainerPath || m.Source == sandbox.PolicyHostRegoPath && m.Destination == sandbox.PolicyContainerRegoPath) {
			continue
		}
		result = append(result, m)
	}
	return result
}
