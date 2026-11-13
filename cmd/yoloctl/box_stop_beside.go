package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

// trySpaceOperationLockFn takes the space's operation lock once, without
// waiting; tests replace it.
var trySpaceOperationLockFn = func(ctx context.Context, space string) (func(), error) {
	return spacelock.TryAcquire(ctx, space, true)
}

// stopBoxBesideFn is the busy path; wrapper tests replace it.
var stopBoxBesideFn = stopBoxBeside

// besideSteps are the engine operations of a box stop beside another
// operation, injectable for tests. read selects the box's container by exact
// name and ownership labels; an absent container is "" without an error.
type besideSteps struct {
	read func(context.Context) (id string, running bool, err error)
	stop func(context.Context, string) error
}

func liveBesideSteps(c *container.Client, space, name string) besideSteps {
	return besideSteps{
		read: func(ctx context.Context) (string, bool, error) {
			d, err := c.Inspect(ctx, name)
			if err != nil {
				if isContainerGone(err) {
					return "", false, nil
				}
				return "", false, err
			}
			if err := container.ValidateContainerID(d.ID); err != nil {
				return "", false, err
			}
			if d.Name != name || d.Labels[agentbox.LabelAgentbox] != name || d.Labels[agentbox.LabelSpace] != space {
				return "", false, fmt.Errorf("container %s named %s is not box %s's container in space %s", shortID(d.ID), name, name, space)
			}
			return d.ID, d.Running, nil
		},
		stop: func(ctx context.Context, id string) error { return c.StopContainerID(ctx, id, false) },
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// stopBoxBeside is `box stop` while another operation holds the space's
// lock. It never waits for that operation and never ends it: after the
// caller's authorization and its gate (footprint: the named box only), it
// selects the box's container once, T, stops T by its full ID when T runs,
// and reports what a final read of the box shows. It performs no cleanup and
// no chat-role, declaration, session or port change; the next stop or start
// removes what the last run left. gated reports that its effects were
// authorized, after which its own completion is recorded.
func stopBoxBeside(cmd *cobra.Command, name string, steps besideSteps) (gated bool, err error) {
	ctx := cmd.Context()
	if err := validateAgentboxName(name); err != nil {
		return false, err
	}
	if !currentControllerPrincipal(ctx).Admin() {
		if err := revalidateOperatorFn(ctx); err != nil {
			return false, err
		}
	}
	if controllerRequestID(ctx) != "" {
		if err := controller.BeginScopedEffects(ctx, boxOnlyIntent(name)); err != nil {
			return false, err
		}
	}
	return true, stopTargetBeside(ctx, name, steps)
}

// stopTargetBeside is the busy path's effect and its result: one target, at
// most one engine stop of it, and a separate final observation.
func stopTargetBeside(ctx context.Context, name string, steps besideSteps) error {
	target, running, err := steps.read(ctx)
	if err != nil {
		return err
	}
	var engineErr error
	stopped := false
	if target != "" && running {
		stopped = true
		engineErr = steps.stop(ctx, target)
	}
	current, currentRunning, err := steps.read(ctx)
	if err != nil {
		if engineErr != nil {
			return errors.Join(fmt.Errorf("container stop of %s: %w", shortID(target), engineErr), fmt.Errorf("read the box after the stop: %w", err))
		}
		return fmt.Errorf("read the box after the stop: %w", err)
	}
	diagnostic := ""
	if engineErr != nil {
		diagnostic = fmt.Sprintf("\n  container stop of %s: %v", shortID(target), engineErr)
	}
	targeted := "this stop found no running container to stop"
	if stopped {
		targeted = "this stop targeted container " + shortID(target)
	}
	switch {
	case current == "" && target == "":
		cliNotice(logging.Box, "box %s stopped: it has no container", name)
	case current == "":
		cliNotice(logging.Box, "box %s stopped: container %s was removed meanwhile", name, shortID(target))
	case current == target && currentRunning:
		return fmt.Errorf("stop failed: box %s is still running (container %s)%s", name, shortID(target), diagnostic)
	case current == target:
		cliNotice(logging.Box, "box %s stopped (container %s)", name, shortID(target))
	case currentRunning:
		return fmt.Errorf("box %s is running as replacement container %s; %s%s", name, shortID(current), targeted, diagnostic)
	case target == "":
		cliNotice(logging.Box, "box %s stopped: replacement container %s is not running", name, shortID(current))
	default:
		cliNotice(logging.Box, "box %s stopped: container %s was replaced by %s, which is not running", name, shortID(target), shortID(current))
	}
	if engineErr != nil {
		cliWarn(logging.Box, "box %s: container stop of %s reported: %v", name, shortID(target), engineErr)
	}
	cliWarn(logging.Box, "box %s: the last run's session holder, forwards, sockets and session files are left for the next stop or start, because another operation is running in the space", name)
	return nil
}
