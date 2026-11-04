package agentbox

import (
	"errors"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
)

// Agent observation: agent boxes get a per-box capped tmpfs, mounted by the
// controller in the VM and bound read-write at observe.ContainerDir when the
// container is created. A container created before this binding reports
// observation as unsupported until its normal recreate; the missing bind is
// never a recreate reason.

// ErrObservationBind marks a container whose observation bind inspect
// positively reports as foreign or read-only: like an unverified root, it is
// refused before anything starts, never treated as a missing report.
var ErrObservationBind = errors.New("agent observation bind differs from its controller root; explicit box recreate required")

// The root mount is replaceable so unprivileged tests never mount.
var (
	ensureObservationRoot = observe.EnsureRoot
	removeObservationRoot = observe.RemoveRoot
)

func addObservationRunOptions(c *Config, opts *container.RunOptions) error {
	if !c.HasAgent() {
		return nil
	}
	dir, err := layout.AgentObserveDir(c.Name)
	if err != nil {
		return err
	}
	for _, m := range opts.Mounts {
		if chatTargetsOverlap(m.Destination, observe.ContainerDir) {
			return errors.New("ordinary mounts cannot overlap the agent observation directory " + observe.ContainerDir)
		}
	}
	if err := ensureObservationRoot(dir); err != nil {
		return err
	}
	opts.Mounts = append(opts.Mounts, container.Mount{Source: dir, Destination: observe.ContainerDir})
	return nil
}

// ObservationBound reports whether a container binds the box's observation
// root. A bind from another source or a read-only one is an error, never
// adopted.
func ObservationBound(c *Config, details *container.Details) (bool, error) {
	dir, err := layout.AgentObserveDir(c.Name)
	if err != nil {
		return false, err
	}
	for _, m := range details.Mounts {
		if m.Destination != observe.ContainerDir {
			continue
		}
		if m.Source != dir || m.ReadOnly {
			return false, ErrObservationBind
		}
		return true, nil
	}
	return false, nil
}

// PrepareObservationRuntime mounts or verifies the root of a container that
// binds it before that container starts again, including after a VM reboot
// emptied /run. Containers without the bind are left alone.
func PrepareObservationRuntime(c *Config, details *container.Details) error {
	if bound, err := ObservationBound(c, details); err != nil || !bound {
		return err
	}
	dir, err := layout.AgentObserveDir(c.Name)
	if err != nil {
		return err
	}
	return ensureObservationRoot(dir)
}

// RemoveObservationRuntime unmounts and removes a destroyed box's root after
// its container is gone. A box that never had one is not an error.
func RemoveObservationRuntime(name string) error {
	dir, err := layout.AgentObserveDir(name)
	if err != nil {
		return err
	}
	return removeObservationRoot(dir)
}
