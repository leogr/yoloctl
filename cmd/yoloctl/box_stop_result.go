package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// boxStopResult is what `box stop` and the rollback of `box start` end with:
// the container state read after one attempt, and what the attempt left.
type boxStopResult struct {
	Running bool
	// Leftovers are things still there, with why; they never change the
	// result. Notes explain a stop that did not take (shown only then).
	Leftovers, Notes []string
}

// boxStopSteps are the operations of the result-mode stop, injectable for
// tests. read reports a missing container as absent, never as an error.
type boxStopSteps struct {
	read           func(context.Context) (present, running, native bool, err error)
	holder         func(context.Context, bool) error
	listedHolder   func(context.Context) error // start: only a unit the holder inventory lists
	forwards       func(context.Context, bool) error
	stop           func(context.Context) error
	endpoint       func(context.Context) error
	sessionState   func(context.Context) error
	reapNamespaces func(context.Context) error
}

func liveBoxStopSteps(c *container.Client, spaceName, name string, errw io.Writer) boxStopSteps {
	applier := agentbox.LiveApplier{Client: c, VerifyStorage: controller.VerifyRuntimeStorage}
	return boxStopSteps{
		read: func(ctx context.Context) (bool, bool, bool, error) {
			details, err := c.Inspect(ctx, name)
			if err != nil {
				if isContainerGone(err) {
					return false, false, false, nil
				}
				return false, false, false, err
			}
			native := false
			if spaceenv.Native() {
				if native, err = agentbox.NativeRuntimeRoute(details); err != nil {
					return false, false, false, err
				}
			}
			return true, details.Running, native, nil
		},
		holder: func(ctx context.Context, native bool) error {
			if native {
				return withdrawNativeHolder(ctx, c, name)
			}
			return session.StopHolderUnit(ctx, c, name)
		},
		listedHolder: func(ctx context.Context) error { return withdrawNativeHolder(ctx, c, name) },
		forwards: func(ctx context.Context, native bool) error {
			if native {
				return agentbox.WithdrawNativeForwards(ctx, c, name)
			}
			return port.UnforwardAgentbox(ctx, c, name)
		},
		stop:           func(ctx context.Context) error { return applier.StopRuntimeForResult(ctx, name) },
		endpoint:       func(ctx context.Context) error { return applier.RemoveStoppedEndpoint(ctx, name) },
		sessionState:   func(ctx context.Context) error { return lifecycleCleanupStoppedSessionState(ctx, c, name) },
		reapNamespaces: func(ctx context.Context) error { return lifecycleReapOrphanedNetns(ctx, spaceName, errw) },
	}
}

// stopAgentboxForResult is `box stop`'s policy (and the rollback of `box
// start`'s): each step once, in today's order, a failing step recorded and the
// next one run, and the result taken from a read of the container at the end.
// No step's error, and so no typed uncertainty marker, leaves it: only the
// final read's error does, when the box's state cannot be read.
func stopAgentboxForResult(ctx context.Context, steps boxStopSteps) (boxStopResult, error) {
	var result boxStopResult
	present, running, native, err := steps.read(ctx)
	if err != nil {
		return result, err // nothing was changed
	}
	leftover := func(what string, err error) {
		if err != nil {
			result.Leftovers = append(result.Leftovers, fmt.Sprintf("%s: %v", what, err))
		}
	}
	leftover("session holder", steps.holder(ctx, native))
	leftover("port forwards", steps.forwards(ctx, native))
	if present && running {
		// One engine stop; on unconfined_network the namespace retirement
		// performs it. It no longer waits for the forwards: their check
		// guards the endpoint removal below.
		if err := steps.stop(ctx); err != nil {
			result.Notes = append(result.Notes, fmt.Sprintf("container stop: %v", err))
		}
	}
	if native && present {
		leftover("endpoint socket", steps.endpoint(ctx))
	}
	leftover("session state", steps.sessionState(ctx))
	if !native {
		// The generic route reaps orphan namespaces; a native route's
		// retirement already did, and the generic reaper is not its own.
		leftover("orphan network namespaces", steps.reapNamespaces(ctx))
	}
	present, running, _, err = steps.read(ctx)
	if err != nil {
		return result, fmt.Errorf("read the container of the box after the stop: %v", err)
	}
	result.Running = present && running
	return result, nil
}

// runBoxStop is the `box stop` command: stopped (exit 0) with its leftovers,
// or a failure when the container still runs.
func runBoxStop(ctx context.Context, name string, steps boxStopSteps) error {
	result, err := stopAgentboxForResult(ctx, steps)
	if err != nil {
		return err
	}
	if result.Running {
		return fmt.Errorf("stop failed: box %s is still running%s", name, joinStopLines(result.Notes, result.Leftovers))
	}
	cliNotice(logging.Box, "box %s stopped", name)
	for _, line := range result.Leftovers {
		cliWarn(logging.Box, "box %s: still there: %s", name, line)
	}
	return nil
}

func joinStopLines(groups ...[]string) string {
	var lines []string
	for _, g := range groups {
		lines = append(lines, g...)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n  " + strings.Join(lines, "\n  ")
}

type startSubmittedKey struct{}

// withStartSubmitted lets the start helpers report that this call submitted
// an engine start, which only box start's result policy asks for.
func withStartSubmitted(ctx context.Context, submitted *bool) context.Context {
	return context.WithValue(ctx, startSubmittedKey{}, submitted)
}

// markStartSubmitted is called right before an engine start is submitted.
func markStartSubmitted(ctx context.Context) {
	if submitted, ok := ctx.Value(startSubmittedKey{}).(*bool); ok {
		*submitted = true
	}
}

// removeStartLeftovers removes what a previous run left before box start
// starts its stopped container: the session holder unit when the holder
// inventory lists it (today's exact unit name) and, on the native
// route, the endpoint socket a container that stopped without a stop command
// left, each under its own checks. One that cannot be removed fails the
// start here; the box stays stopped.
func removeStartLeftovers(ctx context.Context, steps boxStopSteps) error {
	present, running, native, err := steps.read(ctx)
	if err != nil {
		return fmt.Errorf("start failed: read the container: %v", err)
	}
	if !present || running {
		return nil // the start's own checks decide
	}
	if err := steps.listedHolder(ctx); err != nil {
		return fmt.Errorf("start failed: the session holder of a previous run could not be removed: %v", err)
	}
	if native {
		if err := steps.endpoint(ctx); err != nil {
			return fmt.Errorf("start failed: the endpoint socket of a previous run could not be removed: %v", err)
		}
	}
	return nil
}

// settleStartForResult decides box start from a read after its one attempt.
// Before an engine start was submitted the box is still stopped and the
// start's error stands. After it, a failure or a container that is not
// running is rolled back with box stop's policy, which ends with its own
// read; no error carries an uncertainty marker out.
func settleStartForResult(ctx context.Context, name string, startErr error, submitted bool, steps boxStopSteps) error {
	if !submitted {
		return startErr
	}
	if startErr == nil {
		present, running, _, err := steps.read(ctx)
		switch {
		case err != nil:
			startErr = fmt.Errorf("read the container after the start: %v", err)
		case !present || !running:
			startErr = fmt.Errorf("the container is not running after the start")
		default:
			return nil
		}
	}
	reason := startErr.Error()
	result, err := stopAgentboxForResult(ctx, steps)
	switch {
	case err != nil:
		return fmt.Errorf("start failed: %s; the rollback could not read the box: %v", reason, err)
	case result.Running:
		return fmt.Errorf("start failed: %s; box %s is still running", reason, name)
	}
	return fmt.Errorf("start failed: %s", reason)
}
