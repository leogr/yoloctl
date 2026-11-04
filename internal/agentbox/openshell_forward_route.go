package agentbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
)

// The existing mutation caller holds its native space/entity/port admission
// through dispatch and creation. Declared mode cannot select a fallback for
// partial or stale native launch evidence.
func dispatchRuntimePortForward(ctx context.Context, d *container.Details, c *Config, pf Port, native, legacy func(context.Context, port.Spec) error) error {
	if d == nil || !d.Running || d.Name != c.Name || d.Labels[LabelAgentbox] != c.Name {
		return errors.New("forward requires the selected running managed box")
	}
	evidence := ClassifyRuntime(d)
	if evidence.Mode == RuntimeModeUnknown {
		return fmt.Errorf("cannot select forward transport: %s", evidence.Reason)
	}
	if (evidence.Mode == RuntimeModePrivileged) != c.Privileged {
		return errors.New("declared and running box modes differ; recreate before forwarding")
	}
	spec := portSpec(c, pf)
	if hasOpenShellRuntimeSignals(d) {
		if evidence.Mode != RuntimeModeConfined {
			return errors.New("native OpenShell runtime has conflicting privilege evidence")
		}
		return native(ctx, spec)
	}
	return legacy(ctx, spec)
}
