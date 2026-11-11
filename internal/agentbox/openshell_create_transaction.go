package agentbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// nativeCreateProgress records only acknowledgements obtained continuously
// inside this admitted call. Metadata/current state cannot set these fields.
// No credentials, namespace descriptor, supplied PID or executable is retained.
type nativeCreateProgress struct {
	Phase       string
	Reserved    bool
	Created     bool
	ContainerID string
	Prepared    bool
	Started     bool
	Activated   bool
	Ready       bool
}

// nativeCreateActions is private infrastructure wiring, not a request schema
// or a command/endpoint injection interface. The caller owns current storage,
// space/entity/port admission and all independent ownership checks. preflight
// verifies prerequisites before reserve; each cleanup must prove the complete
// undo of its own acknowledged effects, or return an error without adoption.
type nativeCreateActions struct {
	preflight func(context.Context) error
	reserve   func(context.Context) error
	create    func(context.Context) (string, error)
	inspect   func(context.Context) (string, error)
	prepare   func(context.Context) error
	start     func(context.Context) error
	activate  func(context.Context) error
	ready     func(context.Context) error
	forwards  func(context.Context) error
	cleanup   func(context.Context, nativeCreateProgress) error
}

func (a nativeCreateActions) complete() bool {
	return a.preflight != nil && a.reserve != nil && a.create != nil && a.inspect != nil && a.prepare != nil &&
		a.start != nil && a.activate != nil && a.ready != nil && a.forwards != nil && a.cleanup != nil
}

// completeNativeContainerCreation is the startup-only transaction boundary.
// Nothing deferred after success ties PID1 to this worker/client's lifetime.
// It cannot prove completion from a later inactive/missing runtime observation,
// reconnect activation, retry create/start, or guess a cleanup target.
func completeNativeContainerCreation(ctx context.Context, actions nativeCreateActions) error {
	if !actions.complete() {
		return errors.New("native creation requires complete admitted transaction wiring")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := actions.preflight(ctx); err != nil {
		return err
	}
	progress := nativeCreateProgress{}
	fail := func(cause error) error {
		var uncertain *NativeEffectsUncertain
		if errors.As(cause, &uncertain) {
			// A nested forward/activation action may already have an unknown
			// result. Do not start more mutations in an attempt to settle it.
			return cause
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		if err := actions.cleanup(cleanup, progress); err != nil {
			// As on the old path, the cleanup step's own failure is a named
			// warning; the create keeps its plain original error, and a later
			// create of the same name gets a fresh incarnation.
			var uncertain *NativeEffectsUncertain
			if errors.As(err, &uncertain) || cleanup.Err() != nil {
				// A typed lost result, or a cleanup cut off by its own bound
				// while an engine call may have run, stays uncertain.
				return &NativeEffectsUncertain{Cause: errors.Join(cause, err)}
			}
			nativeCreateCleanupNotice(fmt.Errorf("failed create left owned state behind: %w", err))
		}
		// This is a known failed outcome after proved owned cleanup, never
		// a claim that the original startup succeeded or was replayed.
		return cause
	}
	stages := []struct {
		name      string
		run       func(context.Context) error
		ack       *bool
		uncertain bool
	}{
		{"reserve", actions.reserve, &progress.Reserved, true},
		{"create", func(ctx context.Context) error {
			id, err := actions.create(ctx)
			if err != nil {
				return err
			}
			if len(id) != 64 {
				return errors.New("create acknowledgement lacks its complete container ID")
			}
			for _, c := range id {
				if c < '0' || c > '9' && c < 'a' || c > 'f' {
					return errors.New("create acknowledgement has a noncanonical container ID")
				}
			}
			progress.ContainerID = id
			return nil
		}, &progress.Created, true},
		{"inspect", func(ctx context.Context) error {
			id, err := actions.inspect(ctx)
			if err != nil {
				return err
			}
			if id != progress.ContainerID {
				return errors.New("inspected container differs from this call's create acknowledgement")
			}
			return nil
		}, nil, false},
		{"prepare", actions.prepare, &progress.Prepared, false},
		{"start", actions.start, &progress.Started, true},
		{"activate", actions.activate, &progress.Activated, false},
		{"ready", actions.ready, &progress.Ready, false},
		{"forwards", actions.forwards, nil, false},
	}
	for _, stage := range stages {
		progress.Phase = stage.name
		if err := ctx.Err(); err != nil {
			if progress.Reserved {
				return fail(err)
			}
			return err
		}
		if err := stage.run(ctx); err != nil {
			// Only a genuinely lost reply (cancelled or timed out before the
			// engine answered) is uncertain; an engine error is a plain failure.
			// A cancelled engine process returns its exit error (signal:
			// killed), not a context sentinel; it may already have reached
			// the engine. So cancellation DURING the call is a lost reply.
			if stage.uncertain && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
				// Reservation may have published partial bookkeeping; create
				// or start may have happened without its reply. No cleanup,
				// read-state substitution or automatic second action follows.
				return &NativeEffectsUncertain{Cause: fmt.Errorf("native %s was not acknowledged; do not retry automatically: %w", stage.name, err)}
			}
			return fail(err)
		}
		if stage.ack != nil {
			*stage.ack = true
		}
	}
	return nil
}

// nativeCreateCleanupNotice reports cleanup left behind by a failed create;
// tests replace it.
var nativeCreateCleanupNotice = func(err error) {
	slog.Warn(fmt.Sprintf("%v", err), slog.String("actor", "box"), slog.String("executor", "openshell"))
}
