package agentbox

import (
	"context"
	"fmt"
)

// ReconcileResult summarizes what Reconcile did. Useful for dry-run
// output, logging, and tests asserting that Reconcile is idempotent
// (a second call against already-applied state returns an all-false
// result).
type ReconcileResult struct {
	ContainerCreated    bool
	ContainerRecreated  bool
	ContainerRestarted  bool
	PortForwardsAdded   []Port
	PortForwardsRemoved []Port
}

// HasChanges reports whether the result represents any work taken.
func (r ReconcileResult) HasChanges() bool {
	return r.ContainerCreated || r.ContainerRecreated || r.ContainerRestarted ||
		len(r.PortForwardsAdded) > 0 || len(r.PortForwardsRemoved) > 0
}

// Applier executes the per-strategy actions Diff selects. The split
// of Inspector + Applier + pure Diff is what makes Reconcile
// unit-testable: tests pass fakes, the live wiring (LiveInspector /
// LiveApplier built on internal/container + internal/port +
// internal/sandbox) lands separately.
//
// Create / Recreate / Restart are atomic per-strategy: each one
// handles port-forward teardown, policy file write, container
// stop/start, sandbox-socket wait, and port-forward re-apply
// internally. Reconcile doesn't call AddPortForward / RemovePortForward
// when those strategies fire — they're for the live-update-only path
// (no container disturbance).
type Applier interface {
	// Create brings up a new agentbox: writes the policy file,
	// runs the container, waits for the sandbox socket, and applies
	// every declared port forward in c.Ports.
	Create(ctx context.Context, c *Config, desiredPolicy []byte) error

	// Recreate replaces an existing agentbox: tears down current
	// port forwards, stops + removes the container, then does what
	// Create does. Used when an immutable container property changed
	// (image, resources, workdir read-only state, env, mounts).
	Recreate(ctx context.Context, c *Config, desiredPolicy []byte) error

	// Restart swaps the policy on a running container: snapshots the
	// current policy.yaml for rollback, tears down port forwards,
	// stops the container, writes the new policy, starts the
	// container, waits for the sandbox socket, and re-applies port
	// forwards. Used when policy content changed but nothing else
	// requires a recreate.
	Restart(ctx context.Context, c *Config, desiredPolicy []byte) error

	// AddPortForward installs one forward against a running
	// container. Used only in the live-update-only path.
	AddPortForward(ctx context.Context, c *Config, pf Port) error

	// RemovePortForward tears down one forward. Used only in the
	// live-update-only path.
	RemovePortForward(ctx context.Context, c *Config, pf Port) error
}

// Reconcile applies the desired state declared in c against the
// actual state observed via inspector, dispatching to applier for
// the per-strategy execution. Sequence:
//
//  1. Validate(c). Fail fast on invalid input before any I/O.
//  2. Inspect actual state from inspector.
//  3. Compose desired policy via composePolicy(c).
//  4. Diff(c, actual, desiredImage, desiredPolicy) selects the strategy.
//     Account CONTENT is not a diff input — only an account
//     RE-POINT (desired name vs recorded label) is, as a recreate reason.
//  5. Dispatch: Applier.Create / Recreate / Restart, or live-update
//     loop (RemovePortForward then AddPortForward).
//
// Idempotent: Reconcile against already-applied state returns an
// all-false ReconcileResult and performs no Applier calls.
//
// Concurrency safety (flock on the YAML path) is the caller's
// responsibility — Reconcile does not lock. The CLI layer that
// owns the YAML file will acquire the lock around the
// Load → mutate → Save → Reconcile cycle.
func Reconcile(ctx context.Context, c *Config, inspector StateInspector, applier Applier) (ReconcileResult, error) {
	return reconcile(ctx, c, inspector, applier, false)
}

// ReconcileForStart materializes missing containers and applies policy/port
// changes, but never replaces an existing container. Start/restart must preserve
// private container files even when an edit or faulty inspector reports drift.
func ReconcileForStart(ctx context.Context, c *Config, inspector StateInspector, applier Applier) (ReconcileResult, error) {
	return reconcile(ctx, c, inspector, applier, true)
}

// CheckStart refuses recreate-only drift before a start/restart changes runtime
// state. Callers must still use ReconcileForStart afterward: the inspected state
// may change between preflight and application.
func CheckStart(ctx context.Context, c *Config, inspector StateInspector) error {
	plan, _, err := planReconcile(ctx, c, inspector)
	if err != nil {
		return err
	}
	return checkStartPlan(c, plan)
}

func checkStartPlan(c *Config, plan DiffPlan) error {
	if !plan.NeedsRecreate {
		return nil
	}
	return StartRecreateError(c, plan.RecreateReason)
}

// StartRecreateError is the start/restart refusal for recreate-only drift.
func StartRecreateError(c *Config, reason string) error {
	return fmt.Errorf("box %q requires recreation (%s); start/restart preserves the existing container: inspect with yoloctl box inspect %s --space %s, then explicitly use yoloctl box recreate %s --space %s if replacement is intended",
		c.Name, reason, c.Name, c.Space, c.Name, c.Space)
}

func planReconcile(ctx context.Context, c *Config, inspector StateInspector) (DiffPlan, []byte, error) {
	if err := c.Validate(); err != nil {
		return DiffPlan{}, nil, fmt.Errorf("validate config: %w", err)
	}
	actual, err := inspector.Inspect(ctx, c)
	if err != nil {
		return DiffPlan{}, nil, fmt.Errorf("inspect actual state: %w", err)
	}
	desiredPolicy, err := ComposePolicy(c)
	if err != nil {
		return DiffPlan{}, nil, err
	}
	desiredImage, _, err := ResolvedImage(c)
	if err != nil {
		return DiffPlan{}, nil, err
	}
	return Diff(c, actual, desiredImage, desiredPolicy), desiredPolicy, nil
}

func reconcile(ctx context.Context, c *Config, inspector StateInspector, applier Applier, preserveContainer bool) (ReconcileResult, error) {
	var result ReconcileResult
	plan, desiredPolicy, err := planReconcile(ctx, c, inspector)
	if err != nil {
		return result, err
	}
	if preserveContainer {
		if err := checkStartPlan(c, plan); err != nil {
			return result, err
		}
	}
	switch {
	case plan.NeedsCreate:
		if err := applier.Create(ctx, c, desiredPolicy); err != nil {
			return result, fmt.Errorf("create container: %w", err)
		}
		result.ContainerCreated = true
		result.PortForwardsAdded = plan.PortForwardsToAdd
	case plan.NeedsRecreate:
		if err := applier.Recreate(ctx, c, desiredPolicy); err != nil {
			return result, fmt.Errorf("recreate container: %w", err)
		}
		result.ContainerRecreated = true
		result.PortForwardsAdded = plan.PortForwardsToAdd
		result.PortForwardsRemoved = plan.PortForwardsToRemove
	case plan.NeedsRestart:
		if err := applier.Restart(ctx, c, desiredPolicy); err != nil {
			return result, fmt.Errorf("restart container: %w", err)
		}
		result.ContainerRestarted = true
		result.PortForwardsAdded = plan.PortForwardsToAdd
		result.PortForwardsRemoved = plan.PortForwardsToRemove
	default:
		// Live-update only: just port-forward diffs. Remove
		// first so a "change container port for same host port"
		// rebuild doesn't collide on the host listener.
		for _, pf := range plan.PortForwardsToRemove {
			if err := applier.RemovePortForward(ctx, c, pf); err != nil {
				return result, fmt.Errorf("remove port forward %d:%d: %w", pf.Published, pf.Target, err)
			}
			result.PortForwardsRemoved = append(result.PortForwardsRemoved, pf)
		}
		for _, pf := range plan.PortForwardsToAdd {
			if err := applier.AddPortForward(ctx, c, pf); err != nil {
				return result, fmt.Errorf("add port forward %d:%d: %w", pf.Published, pf.Target, err)
			}
			result.PortForwardsAdded = append(result.PortForwardsAdded, pf)
		}
	}
	return result, nil
}
