package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Private original-call wiring only. These callbacks are not a request or
// authority endpoint; actual native factory construction is reviewed with it.
type nativeGenerationStartActions struct {
	preflight    func(context.Context) error
	stopPrevious func(context.Context) error
	prepare      func(context.Context) (*nativeStartedGeneration, error)
	beforeStart  func(context.Context, *nativeStartedGeneration) error
	start        func(context.Context, *nativeStartedGeneration) error
	activate     func(context.Context, *nativeStartedGeneration) error
	ready        func(context.Context, *nativeStartedGeneration) error
	forwards     func(context.Context, *nativeStartedGeneration) error
	cleanup      func(context.Context, *nativeStartedGeneration) error
}

// Failed/lost preparation, Start or nested effect result halts every suffix.
// A known post-ACK failure gets one bounded cleanup of this original attempt;
// no CID removal or automatic second launch/rollback is supplied here.
func completeNativeGenerationStart(ctx context.Context, a nativeGenerationStartActions) error {
	if a.preflight == nil || a.stopPrevious == nil || a.prepare == nil || a.beforeStart == nil || a.start == nil || a.activate == nil || a.ready == nil || a.forwards == nil || a.cleanup == nil {
		return errors.New("native generation Start requires complete admitted wiring")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.preflight(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.stopPrevious(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	g, err := a.prepare(ctx)
	if err != nil {
		// PrepareNext distinguishes its pure refusals from every failed/lost
		// preparation effect. Preserve that classification rather than turn
		// a pre-effect policy/storage refusal into invented uncertainty.
		return err
	}
	if g != nil && g.failure != nil {
		var uncertain *NativeEffectsUncertain
		if errors.As(g.failure, &uncertain) {
			return g.failure
		}
		return errors.Join(g.failure, errors.New("fresh generation result contains prior failure state"))
	}
	if g == nil || !nativeLowerHex(g.cid, 32) || g.prepared.Record.Generation == "" || g.startAttempted || g.startAcknowledged || g.activationAttempted || g.activation != nil || g.cleanupAttempted {
		return errors.New("generation preparation lacks its original fresh known result")
	}
	fail := func(cause error) error {
		g.failure = cause
		var uncertain *NativeEffectsUncertain
		if errors.As(cause, &uncertain) {
			return cause
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		if err := a.cleanup(cleanup, g); err != nil {
			// As on the old path, the cleanup step's own failure is a named
			// warning and the start keeps its plain error. Only a genuinely
			// lost cleanup reply stays uncertain.
			if nativeLostReply(err) || cleanup.Err() != nil {
				return &NativeEffectsUncertain{Cause: errors.Join(cause, err)}
			}
			nativeStartCleanupNotice(fmt.Errorf("failed start left generation state behind: %w", err))
		}
		return cause
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := a.beforeStart(ctx, g); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	g.startAttempted = true // State comes only from this exact engine invocation.
	if err := a.start(ctx, g); err != nil {
		// Only a lost reply (cancelled or timed out) is uncertain. An engine
		// error means the container did not start: a plain failure, as on
		// the old path; the next start prepares a fresh generation. A
		// cancelled engine process returns its exit error, not a context
		// sentinel, so cancellation during the call also counts as lost.
		if nativeLostReply(err) || ctx.Err() != nil {
			g.failure = &NativeEffectsUncertain{Cause: fmt.Errorf("native generation Start was not acknowledged; do not retry: %w", err)}
			return g.failure
		}
		g.failure = fmt.Errorf("native generation Start failed: %w", err)
		return g.failure
	}
	g.startAcknowledged = true
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	g.activationAttempted = true
	err = a.activate(ctx, g)
	if g.activation == nil {
		return fail(&NativeEffectsUncertain{Cause: errors.Join(err, errors.New("invoked native activation lost its original handle/result"))})
	}
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := a.ready(ctx, g); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := a.forwards(ctx, g); err != nil {
		return fail(err)
	}
	return nil
}

// nativeLostReply reports a reply that never arrived (cancelled or timed out):
// the only failure that stays uncertain (leogr's rule).
func nativeLostReply(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// nativeStartCleanupNotice reports state left behind by a failed start; tests
// replace it.
var nativeStartCleanupNotice = func(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "warning: %v\n", err)
}
