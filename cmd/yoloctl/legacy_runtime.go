package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

// legacyRuntimeRefusal is the remedy for an operation that needed a new run of
// a pre-native confined box. It still matches *agentbox.LegacyRuntimeError.
type legacyRuntimeRefusal struct {
	msg    string
	legacy *agentbox.LegacyRuntimeError
}

func (e *legacyRuntimeRefusal) Error() string { return e.msg }
func (e *legacyRuntimeRefusal) Unwrap() error { return e.legacy }

// legacyRefusal says what a refused operation does with a running box.
type legacyRefusal int

const (
	// legacyStart never stops: a start, revive or chat server start that
	// finds the box running reports it running.
	legacyStart legacyRefusal = iota
	// legacyRestart stops the running box first (restart, restart-sessions).
	legacyRestart
	// legacyPolicyApply stops it first and says the saved policy stays.
	legacyPolicyApply
)

// refuseLegacyRun turns err, when it is the legacy-runtime refusal of an
// operation that needed a new run, into the remedy. A restart or policy apply
// first stops a running box through the shared session-aware stop; only a stop
// that a read confirms is reported as stopped, and a failed or unconfirmed
// stop offers no recreation. Any other error is returned unchanged.
func refuseLegacyRun(ctx context.Context, c *container.Client, space, name string, errw io.Writer, err error, kind legacyRefusal) error {
	var legacy *agentbox.LegacyRuntimeError
	if !errors.As(err, &legacy) {
		return err
	}
	lead := fmt.Sprintf("Box %s predates the native runtime and cannot be started or restarted.", name)
	saved := ""
	if kind == legacyPolicyApply {
		saved = " The saved policy was not reverted."
	}
	running, readErr := legacyRunning(ctx, c, name)
	if readErr != nil {
		return &legacyRuntimeRefusal{fmt.Sprintf("%s Its state could not be read: %v.%s", lead, readErr, saved), legacy}
	}
	if running && kind == legacyStart {
		return &legacyRuntimeRefusal{fmt.Sprintf("%s It is running. Recreate it to continue: %s.", lead, legacy.RecreateCommand()), legacy}
	}
	if running {
		if stopErr := stopAgentboxRuntime(ctx, c, space, name, errw); stopErr != nil {
			return &legacyRuntimeRefusal{fmt.Sprintf("%s Stopping it failed: %v.%s", lead, stopErr, saved), legacy}
		}
		still, readErr := legacyRunning(ctx, c, name)
		switch {
		case readErr != nil:
			return &legacyRuntimeRefusal{fmt.Sprintf("%s Its stop returned, but a read could not confirm that it stopped: %v.%s", lead, readErr, saved), legacy}
		case still:
			return &legacyRuntimeRefusal{fmt.Sprintf("%s It is still running after its stop.%s", lead, saved), legacy}
		}
	}
	return &legacyRuntimeRefusal{fmt.Sprintf("%s It is stopped. Recreate it to continue: %s.%s", lead, legacy.RecreateCommand(), saved), legacy}
}

// legacyRunning reads whether the box's container runs; an absent container
// is not running.
func legacyRunning(ctx context.Context, c *container.Client, name string) (bool, error) {
	details, err := c.Inspect(ctx, name)
	if err != nil {
		if isContainerGone(err) {
			return false, nil
		}
		return false, err
	}
	return details.Running, nil
}
