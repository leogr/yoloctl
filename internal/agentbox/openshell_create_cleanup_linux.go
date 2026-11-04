package agentbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

func nativeInactiveCleanupObservation(d *container.Details) bool {
	if d == nil || d.Running || d.PID != 0 || d.AmbiguousTaskStateObserved || !d.InactiveTaskFieldsObserved {
		return false
	}
	switch d.Status {
	case "created", "exited", "stopped":
		return true
	case "":
		return d.EmptyTaskStateObserved
	default:
		return false
	}
}

// An observation is not proof the task never ran. The fresh creator supplies
// its separate Create ACK/no-Start-attempt history before using this predicate.
func nativeUnstartedCleanupObservation(d *container.Details) bool {
	return nativeInactiveCleanupObservation(d) && (d.Status == "created" || d.Status == "" && d.EmptyTaskStateObserved)
}

func validateNativeCleanupState(d *container.Details, id string, target openshell.ActivationTarget, started bool) error {
	if d == nil || d.ID != id || target.ContainerID != "" && target.ContainerID != id {
		return errors.New("cleanup container differs from create acknowledgement")
	}
	if d.AmbiguousTaskStateObserved {
		return errors.New("cleanup refuses ambiguous inspected task state")
	}
	if !started {
		if !nativeUnstartedCleanupObservation(d) {
			return errors.New("unstarted creation unexpectedly ran or lacks explicit task observation")
		}
		if d.StartedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, d.StartedAt)
			if err != nil || !at.IsZero() {
				return errors.New("unstarted creation has prior launch history")
			}
		}
		return nil
	}
	if d.Running {
		at, err := time.Parse(time.RFC3339Nano, d.StartedAt)
		if err != nil || at.IsZero() || d.Status != "running" || d.PID <= 1 {
			return errors.New("running cleanup lacks a valid current launch observation")
		}
		if target.ContainerID != "" && (!at.Equal(target.StartedAt) || d.PID != target.PID) {
			return errors.New("cleanup container start identity changed")
		}
		return nil
	}
	if !nativeInactiveCleanupObservation(d) {
		return errors.New("inactive cleanup lacks explicit current task observation")
	}
	// Missing/zero history is not counter-evidence to this call's Start ACK.
	// Present malformed or changed history still refuses before Stop/effects.
	if d.StartedAt != "" {
		at, err := time.Parse(time.RFC3339Nano, d.StartedAt)
		if err != nil || target.ContainerID != "" && !at.IsZero() && !at.Equal(target.StartedAt) {
			return errors.New("inactive cleanup container start identity changed")
		}
	}
	return nil
}

type nativeCreatedCleanupChecks struct {
	inspect   func(context.Context) (*container.Details, error)
	remaining func(context.Context) error
	id        string
	target    openshell.ActivationTarget
	started   bool
}

func (c nativeCreatedCleanupChecks) owned(ctx context.Context) error {
	if c.inspect == nil || c.remaining == nil {
		return errors.New("native cleanup lacks actual owner/record verifiers")
	}
	d, err := c.inspect(ctx)
	if err != nil {
		return err
	}
	if err := validateNativeCleanupState(d, c.id, c.target, c.started); err != nil {
		return err
	}
	return c.remaining(ctx)
}

func (c nativeCreatedCleanupChecks) inactive(ctx context.Context) error {
	if c.inspect == nil || c.remaining == nil {
		return errors.New("native cleanup lacks actual inactive/record verifiers")
	}
	d, err := c.inspect(ctx)
	if err != nil {
		return err
	}
	if !nativeInactiveCleanupObservation(d) {
		return errors.New("acknowledged native stop lacks its exact inactive task observation")
	}
	if err := validateNativeCleanupState(d, c.id, c.target, c.started); err != nil {
		return err
	}
	return c.remaining(ctx)
}

// Shared by the actual failed-create callback and its adapter regressions.
// Namespace owners handle their own fresh Stop/rm; no-namespace cases retain
// the direct path. No error may become a direct-cleanup fallback or rm ACK.
func cleanupNativeCreatedContainerID(ctx context.Context, c nativeCreatedCleanupChecks, activation *openshell.NativeActivation, stop func(context.Context) error, remove func(context.Context, string) error) error {
	if stop == nil || remove == nil {
		return errors.New("native cleanup lacks actual engine callbacks")
	}
	if err := c.owned(ctx); err != nil {
		return err
	}
	handled, err := activation.RemoveCreatedContainer(ctx, openshell.NativeActivationCleanup{
		VerifyOwned: c.owned, Stop: stop, VerifyInactive: c.inactive,
		RemoveContainerID: func(removeCtx context.Context, selected string) error {
			if selected != c.id {
				return errors.New("namespace cleanup selected a different CID")
			}
			return remove(removeCtx, selected)
		},
		VerifyRemaining: c.remaining,
	})
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	// Each step's own error decides (decision #3): a plain error, which the
	// failed create reports as a named warning. Only a typed lost-result error
	// from a callback stays uncertain.
	if c.started {
		if err := stop(ctx); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	if err := c.inactive(ctx); err != nil {
		return fmt.Errorf("inactive check: %w", err)
	}
	if err := remove(ctx, c.id); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	if err := c.remaining(ctx); err != nil {
		return fmt.Errorf("leftover check: %w", err)
	}
	return nil
}
