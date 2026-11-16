package agentbox

import (
	"bytes"
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"golang.org/x/sys/unix"
)

// Caller holds space/entity/port admission and has ended its holder AFTER
// independent preflight. This repeats qualification before withdrawing
// forwards or stopping the runtime, then launches the saved policy once with
// the box's declared forwards. The previous policy is never launched again.
func (a LiveApplier) restartNativeGeneration(ctx context.Context, c *Config, policy []byte) error {
	selected, checkpoint, err := a.snapshotNativeGeneration(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = checkpoint.Close() }()
	copy := *c
	copy.Ports = append([]Port(nil), c.Ports...)
	c = &copy
	policy = bytes.Clone(policy)
	return completeNativePolicyRestart(ctx, policy, nativePolicyRestartActions{
		preflight: func(check context.Context) error {
			_, err := a.qualifyNativePolicyRestart(check, c, policy, selected, checkpoint)
			return err
		},
		withdraw: func(withdraw context.Context) error { return WithdrawNativeForwards(withdraw, a.Client, c.Name) },
		stop:     func(stop context.Context) error { return a.stopNativeRuntime(stop, c.Name, selected.ContainerID) },
		launch: func(start context.Context, next []byte) error {
			return a.startNativeStoppedGenerationWithCheckpoint(start, c, next, checkpoint, selected.ContainerID)
		},
	})
}

// Capture the complete observed numeric forward set before withdrawal (revive
// relaunches it). Ambiguous duplicate publications or mixed direct/ordinary
// evidence cannot prove a usable observed set.
func nativeObservedForwards(desired *Config, known []port.ForwardService) (*Config, error) {
	if desired == nil || desired.Privileged {
		return nil, errors.New("native observed forwards require an ordinary declaration")
	}
	result := *desired
	result.Ports = nil
	seen := map[int]bool{}
	for _, service := range known {
		if service.Spec.Agentbox != desired.Name {
			continue
		}
		spec := service.Spec
		native, err := port.OpenShellUnitName(spec)
		if err != nil || spec.Direct || seen[spec.HostPort] || service.OpenShell && service.Unit != native || !service.OpenShell && service.Unit != port.UnitName(spec) {
			return nil, errors.New("prior native forward inventory is ambiguous or has incompatible service identity")
		}
		seen[spec.HostPort] = true
		result.Ports = append(result.Ports, Port{Published: spec.HostPort, Target: spec.ContainerPort})
	}
	return &result, nil
}

// Read current selected identity/policy and qualify immutable caches before
// holder/forward/runtime effects. No per-container state is created or removed.
func (a LiveApplier) preflightNativePolicyRestart(ctx context.Context, c *Config, policy []byte) error {
	selected, checkpoint, err := a.snapshotNativeGeneration(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = checkpoint.Close() }()
	_, err = a.qualifyNativePolicyRestart(ctx, c, policy, selected, checkpoint)
	return err
}
func (a LiveApplier) qualifyNativePolicyRestart(check context.Context, c *Config, policy []byte, selected OpenShellRuntime, checkpoint *openshell.NativeGeneration) (*Config, error) {
	if c == nil || a.Client == nil || a.VerifyStorage == nil || checkpoint == nil || !nativeLowerHex(selected.ContainerID, 32) {
		return nil, errors.New("native policy qualification lacks original snapshot/current storage verifier")
	}
	if err := check.Err(); err != nil {
		return nil, err
	}
	if err := a.VerifyStorage(check); err != nil {
		return nil, err
	}
	if err := checkpoint.ValidatePolicy(policy); err != nil {
		return nil, err
	}
	if err := a.Client.RequireWorkloadPool(check); err != nil {
		return nil, err
	}
	known, err := port.Inventory(check, a.Client, "")
	if err != nil {
		return nil, err
	}
	observed, err := nativeObservedForwards(c, known)
	if err != nil {
		return nil, err
	}
	var other []port.ForwardService
	for _, service := range known {
		if service.Spec.Agentbox != c.Name {
			other = append(other, service)
		}
	}
	published := map[int]bool{}
	for _, pf := range c.Ports {
		if published[pf.Published] {
			return nil, errors.New("duplicate native published port before policy restart")
		}
		published[pf.Published] = true
	}
	for _, pf := range append(append([]Port(nil), c.Ports...), observed.Ports...) {
		spec := portSpec(c, pf)
		if _, err := port.OpenShellUnitName(spec); err != nil {
			return nil, err
		}
		if err := noNativeForwardConflict(other, spec); err != nil {
			return nil, err
		}
	}
	if len(c.Ports) > 0 || len(observed.Ports) > 0 {
		if err := preflightForwardCLI(check); err != nil {
			return nil, err
		}
	}
	if err := checkpoint.QualifyNativeArtifacts(check); err != nil {
		return nil, err
	}
	// Re-read actual fixed CID/current launch around artifact qualification.
	// These are current ownership observations, never acknowledgements.
	read := func(c context.Context) (OpenShellRuntime, *container.Details, error) {
		current, err := inspectOpenShellRuntime(c, a.Client, selected.Record.Launch.Binding.Box, selected.ContainerID)
		if err != nil {
			return OpenShellRuntime{}, nil, err
		}
		d, err := a.Client.Inspect(c, selected.ContainerID)
		return current, d, err
	}
	checks := nativeRuntimeStopChecks{selected: selected, read: read, resources: a.VerifyStorage}
	if err := checks.owned(check); err != nil {
		return nil, err
	}
	directory, err := openForwardSocketDirectory(c.Name)
	if errors.Is(err, unix.ENOENT) && !selected.Running {
		return observed, nil
	}
	if err != nil {
		return nil, err
	}
	if directory == nil {
		return nil, errors.New("native policy restart lacks its protected SSH directory")
	}
	return observed, directory.Close()
}
