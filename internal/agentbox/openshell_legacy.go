package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// CheckRetainedLegacyStart is read-only preflight, not launch/admission. Actual
// native and explicit privileged commands never require the retired binary.
func CheckRetainedLegacyStart(ctx context.Context, details *container.Details, check func(context.Context) error) error {
	if ctx == nil || details == nil {
		return errors.New("legacy start lacks inspected runtime evidence")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if details.Privileged != nil && *details.Privileged {
		return nil
	}
	args := details.Command
	if len(args) > 0 && args[0] == sandbox.EntrypointShimPath {
		args = args[1:]
	}
	if len(args) == 0 || args[0] != sandbox.BinaryDirContainerPath+"/openshell-sandbox" {
		return nil
	}
	if check == nil {
		return container.ErrLegacyOpenShellUnavailable
	}
	return check(ctx)
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
