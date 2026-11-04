package main

import (
	"context"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var lifecycleTryNativeStop = tryNativeAgentboxStop

// Inspect before holder or forward effects. A native Stop has its own strict
// endpoint/generation cleanup; it must not fall through to warning-only holder
// handling, a second Stop or the generic orphan-netns reaper.
func tryNativeAgentboxStop(ctx context.Context, c *container.Client, name string, stopContainer bool) (bool, error) {
	if c == nil || !spaceenv.Native() {
		return false, nil
	}
	details, err := c.Inspect(ctx, name)
	if err != nil {
		if isContainerGone(err) {
			return true, nil // old path: a missing container has nothing to stop
		}
		return false, err
	}
	native, err := agentbox.NativeRuntimeRoute(details)
	if err != nil || !native {
		return native, err
	}
	err = completeNativeCallerStop(ctx, stopContainer,
		func(check context.Context) error { return withdrawNativeHolder(check, c, name) },
		func(check context.Context) error { return agentbox.WithdrawNativeForwards(check, c, name) },
		func(check context.Context) error { return stopManagedAgentboxContainer(check, c, name) },
		func(check context.Context) error { return lifecycleCleanupStoppedSessionState(check, c, name) })
	return true, err
}

func completeNativeCallerStop(ctx context.Context, stopContainer bool, holder, forwards, stop, sessionState func(context.Context) error) error {
	steps := []struct {
		name string
		run  func(context.Context) error
	}{{"session holder", holder}, {"retained forwards", forwards}}
	if stopContainer {
		steps = append(steps, struct {
			name string
			run  func(context.Context) error
		}{"native container", stop})
	}
	steps = append(steps, struct {
		name string
		run  func(context.Context) error
	}{"session state", sessionState})
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("stop %s: %w", step.name, err)
		}
	}
	return nil
}
