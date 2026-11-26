package agentbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Stop preserves the actual runtime's teardown route. Existing holder and
// forward cleanup remain the enclosing lifecycle caller's responsibility.
func (a LiveApplier) Stop(ctx context.Context, name string) error {
	if a.Client == nil {
		return errors.New("runtime stop requires a container client")
	}
	details, err := a.Client.Inspect(ctx, name)
	if err != nil {
		return err
	}
	return stopInspectedRuntime(ctx, details,
		func(ctx context.Context) error { return a.stopNativeRuntime(ctx, name, details.ID) },
		func(ctx context.Context) error { return sandbox.StopContainerID(ctx, a.Client, name, details.ID) })
}

// StopRuntimeForResult is `box stop`'s result-mode engine stop: one stop of
// the container the caller's first read selected, by its full ID, on its own
// route. The name is never resolved again, so a replacement that appeared
// since that read is never the target. On the native route the forward
// precondition and the endpoint removal are left to the caller's steps.
func (a LiveApplier) StopRuntimeForResult(ctx context.Context, name, id string) error {
	if a.Client == nil {
		return errors.New("runtime stop requires a container client")
	}
	if err := container.ValidateContainerID(id); err != nil {
		return err
	}
	details, err := a.Client.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if details.ID != id || details.Name != name || details.Labels[LabelAgentbox] != name || details.Labels[LabelSpace] != a.Client.Env() {
		return fmt.Errorf("container %s is no longer box %s's container", shortContainerID(id), name)
	}
	if !details.Running {
		return nil
	}
	return stopInspectedRuntime(ctx, details,
		func(ctx context.Context) error { return a.stopNativeRuntimeForResult(ctx, name, id) },
		func(ctx context.Context) error { return sandbox.StopContainerID(ctx, a.Client, name, id) })
}

// ResultLeftover is a result-mode stop step that did its effect but left
// part of its work, named by What, for the next stop or start.
type ResultLeftover struct {
	What  string
	Cause error
}

func (e *ResultLeftover) Error() string { return e.What + ": " + e.Cause.Error() }
func (e *ResultLeftover) Unwrap() error { return e.Cause }

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func stopInspectedRuntime(ctx context.Context, details *container.Details, native, legacy func(context.Context) error) error {
	if details == nil || native == nil || legacy == nil {
		return errors.New("runtime stop requires inspected route evidence")
	}
	if hasOpenShellRuntimeSignals(details) {
		if ClassifyRuntime(details).Mode != RuntimeModeConfined {
			return errors.New("native stop refuses mixed or incomplete runtime evidence")
		}
		return native(ctx)
	}
	return legacy(ctx)
}

// stopCheckFailSafe ends a check after an acknowledged stop. The check is
// detached from its caller on purpose, so it finishes after Ctrl+C too; this
// is its only bound. Tests shorten it.
var stopCheckFailSafe = 2 * time.Minute

// A failed engine reply is never settled with later state or a second stop.
// After its ACK, a bounded same-admission verifier can confirm inactive state
// and remove only its protected endpoint. Cancellation retains context values.
func completeNativeRuntimeStop(ctx context.Context, stop, verify func(context.Context) error) error {
	if stop == nil || verify == nil {
		return errors.New("native stop requires complete acknowledgement wiring")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stop(ctx); err != nil {
		return &NativeEffectsUncertain{Cause: fmt.Errorf("native container stop was not acknowledged; do not retry automatically: %w", err)}
	}
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopCheckFailSafe)
	defer cancel()
	if err := verify(check); err != nil {
		return &NativeEffectsUncertain{Cause: fmt.Errorf("acknowledged native stop cleanup remains unproved: %w", err)}
	}
	return nil
}

// completeNativeStopRoute preserves the selected runtime's network mode.
// Namespace retirement owns Stop for unconfined generations. Its failure must
// never fall back to another Stop, endpoint deletion or legacy cleanup.
func completeNativeStopRoute(ctx context.Context, unconfined bool, stop, inactive, namespace, endpoint func(context.Context) error) error {
	if stop == nil || inactive == nil || endpoint == nil || unconfined && namespace == nil {
		return errors.New("native Stop route lacks complete acknowledgement/cleanup callbacks")
	}
	finish := func(check context.Context) error {
		if err := inactive(check); err != nil {
			return err
		}
		return endpoint(check)
	}
	if !unconfined {
		return completeNativeRuntimeStop(ctx, stop, finish)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := namespace(ctx); err != nil {
		return err
	}
	return completeNativeStopEndpoint(ctx, inactive, endpoint)
}

func completeNativeStopEndpoint(ctx context.Context, inactive, endpoint func(context.Context) error) error {
	if inactive == nil || endpoint == nil {
		return errors.New("native Stop endpoint completion lacks its pure ownership checks")
	}
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopCheckFailSafe)
	defer cancel()
	return finishNativeStopEndpoint(check, inactive, endpoint)
}

func finishNativeStopEndpoint(check context.Context, inactive, endpoint func(context.Context) error) error {
	if err := inactive(check); err != nil {
		return &NativeEffectsUncertain{Cause: fmt.Errorf("acknowledged native namespace Stop inactivity remains unproved: %w", err)}
	}
	if err := endpoint(check); err != nil {
		return &NativeEffectsUncertain{Cause: fmt.Errorf("acknowledged native namespace Stop endpoint cleanup remains unproved: %w", err)}
	}
	return nil
}

// PreflightRestart qualifies a later explicit stop+fresh-start against the
// current declaration. Policy reconciliation supplies its exact raw policy via
// PreflightPolicyRestart instead. Labelled legacy route behavior is preserved.
func (a LiveApplier) PreflightRestart(ctx context.Context, c *Config) error {
	prepared, _, err := a.PrepareNativeStart(ctx, c, false)
	defer func() { _ = prepared.Close() }()
	return err
}
