package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// legacyRuntimeRefusal is the remedy for an operation that needed a new run of
// a pre-native confined box. It still matches *agentbox.LegacyRuntimeError.
type legacyRuntimeRefusal struct {
	msg    string
	legacy *agentbox.LegacyRuntimeError
	// stopped: a read after the refusal (and after its own stop, if any)
	// showed the box stopped. Only then is recreation offered.
	stopped bool
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
		return &legacyRuntimeRefusal{fmt.Sprintf("%s Its state could not be read: %v.%s", lead, readErr, saved), legacy, false}
	}
	if running && kind == legacyStart {
		return &legacyRuntimeRefusal{fmt.Sprintf("%s It is running. Recreate it to continue: %s.", lead, legacy.RecreateCommand()), legacy, false}
	}
	if running {
		if stopErr := stopAgentboxRuntime(ctx, c, space, name, errw); stopErr != nil {
			return &legacyRuntimeRefusal{fmt.Sprintf("%s Stopping it failed: %v.%s", lead, stopErr, saved), legacy, false}
		}
		still, readErr := legacyRunning(ctx, c, name)
		switch {
		case readErr != nil:
			return &legacyRuntimeRefusal{fmt.Sprintf("%s Its stop returned, but a read could not confirm that it stopped: %v.%s", lead, readErr, saved), legacy, false}
		case still:
			return &legacyRuntimeRefusal{fmt.Sprintf("%s It is still running after its stop.%s", lead, saved), legacy, false}
		}
	}
	return &legacyRuntimeRefusal{fmt.Sprintf("%s It is stopped. Recreate it to continue: %s.%s", lead, legacy.RecreateCommand(), saved), legacy, true}
}

// remedyLegacyRecreate is the typed remedy a worker's outcome record carries
// for a known pre-native refusal whose box a read showed stopped.
const remedyLegacyRecreate = "legacy-recreate"

// outcomeRemedy returns the typed remedy attribute for an outcome record of
// err: only a known pre-native refusal with a confirmed stopped box, never an
// uncertain result; otherwise none.
func outcomeRemedy(err error) []slog.Attr {
	var refusal *legacyRuntimeRefusal
	if err == nil || cliUncertain(err) || !errors.As(err, &refusal) || !refusal.stopped {
		return nil
	}
	return []slog.Attr{slog.String(prettylog.KeyRemedy, remedyLegacyRecreate)}
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

// legacyRuntimeFirst classifies an existing container's actual runtime before
// the generic recreate-drift refusals, so a pre-native box always takes its
// own remedy (a restart stops it) instead of a drift refusal that leaves it as
// it is. It only reads: an absent container, a failed read, and native or
// uncertain native evidence return nil and leave the existing checks to
// decide; native drift preservation is unchanged.
// legacyRuntimeFirst is actualLegacyRuntimeFirst; fixtures that model a
// native box through seams replace it.
var legacyRuntimeFirst = actualLegacyRuntimeFirst

func actualLegacyRuntimeFirst(ctx context.Context, c *container.Client, space, name string) error {
	details, err := c.Inspect(ctx, name)
	if err != nil {
		return nil
	}
	if native, err := agentbox.NativeRuntimeRoute(details); err != nil || native {
		return nil
	}
	if legacy, err := agentbox.LegacyRuntime(details); err != nil || !legacy {
		return nil
	}
	return &agentbox.LegacyRuntimeError{Space: space, Box: name}
}

// legacyRemedyFlag marks the one box recreate the host sends after a person
// accepted the pre-native recreation offer.
const legacyRemedyFlag = "legacy-remedy"

// exactRunStopSteps binds box start's result-policy rollback to the exact run
// this operation started (container.ExpectedRuns.Started, kept across the
// operation's own stops). A read showing another container, or a running
// container in another run (container.SameRun), is refused before any cleanup
// effect, as is an unknown or incomplete started identity; the failure is
// reported, never hidden. A stopped read of the started container is that
// replacement after its stop (by the rollback or the attempt's own cleanup).
func exactRunStopSteps(steps boxStopSteps, name string) boxStopSteps {
	read := steps.read
	steps.read = func(ctx context.Context) (boxTarget, error) {
		target, err := read(ctx)
		if err != nil || !target.Present {
			return target, err
		}
		started, ok := container.ExpectedRunsFrom(ctx).Started(name)
		switch {
		case !ok || started.ID == "" || started.StartedAt == "":
			return boxTarget{}, fmt.Errorf("the identity of the run this recreate started is unknown or incomplete, so box %s was not stopped", name)
		case target.ID != started.ID:
			return boxTarget{}, fmt.Errorf("box %s is now another container than the replacement this recreate started, so it was not stopped", name)
		case target.Running && !container.SameRun(started, target.Run):
			return boxTarget{}, fmt.Errorf("box %s runs another run of its container than the one this recreate started, so it was not stopped", name)
		}
		return target, nil
	}
	return steps
}
