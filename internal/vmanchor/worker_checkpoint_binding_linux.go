//go:build linux

package vmanchor

import (
	"context"
	"errors"
	"runtime"

	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

// BindCheckpoint binds physical ownership to a GENUINE original handoff, ONCE.
// Channel.Take alone grants no source permission; no numeric/identity/path or
// callback constructor exists here. CLI transfers TakeCheckpoint's original
// File to this physical owner and removes its former direct Close defer.
// This object borrows Channel; it owns ONLY its existing process/ns pins.
func (w *WorkerHandoff) BindCheckpoint(ctx context.Context, c *workercheckpoint.Channel) error {
	if w == nil {
		return errors.New("missing genuine original worker handoff")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w.mu.Lock()
	if err := w.check(); err != nil {
		w.mu.Unlock()
		return err
	}
	if w.checkpointBindingAttempted {
		w.mu.Unlock()
		return errors.New("original checkpoint binding already attempted")
	}
	w.checkpointBindingAttempted = true
	expected, parent := w.checkpoint, w.controller.identity.PID
	w.mu.Unlock()
	// No w.mu across context-aware admission/physical I/O. A second source
	// waiter must not block behind a mutex held by a prior parent exchange.
	observed, err := c.Observe(ctx)
	if err == nil {
		err = matchCheckpoint(expected, parent, observed)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if check := w.check(); check != nil {
		return check
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		w.failed = err
		return err
	}
	// Never rebaseline after any failed attempt/expiry/concurrent close.
	w.checkpointOwner = c
	return nil
}
func matchCheckpoint(expected workerDescriptor, parent int, actual workercheckpoint.Identity) error {
	if expected.FD != 4 || actual.FD != expected.FD || actual.Device != expected.Device || actual.Inode != expected.Inode || actual.Mode != expected.Mode || actual.PeerPID != int32(parent) || actual.PeerUID != 0 || actual.PeerGID != 0 {
		return errors.New("physical checkpoint differs from original handoff descriptor/creator")
	}
	return nil
}
func (w *WorkerHandoff) boundOriginal() (c *workercheckpoint.Channel, expected workerDescriptor, parent int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = w.check(); err != nil {
		return
	}
	if !w.checkpointBindingAttempted || w.checkpointOwner == nil {
		err = errors.New("genuine original checkpoint not bound")
		return
	}
	return w.checkpointOwner, w.checkpoint, w.controller.identity.PID, nil
}
func (w *WorkerHandoff) failCheckpoint(err error) error {
	return w.finishCheckpoint(err, false)
}
func (w *WorkerHandoff) finishCheckpoint(err error, localCancellation bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if check := w.check(); check != nil {
		joined := errors.Join(err, check)
		if !w.closed {
			w.failed = joined
		} // never erase known typed parent uncertainty
		return joined
	}
	if err != nil && !localCancellation {
		w.failed = err
	}
	return err
}

// CheckParent performs actual read-only opcode16 over the SAME physical owner.
// It does NOT accept a success callback/proxy/backend. It creates no SDK grant,
// reservation/nonce/receipt ACK or source replacement. SDK installation remains
// separate and must bracket its own actual source/publisher/storage checks.
func (w *WorkerHandoff) CheckParent(ctx context.Context, box string) error {
	if w == nil {
		return errors.New("missing genuine original worker handoff")
	}
	if ctx == nil {
		return errors.New("missing original checkpoint context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c, expected, parent, err := w.boundOriginal()
	if err != nil {
		return err
	}
	observed, err := c.Observe(ctx)
	if err == nil {
		err = matchCheckpoint(expected, parent, observed)
	}
	if err == nil {
		err = c.CheckPIDAnchor(ctx, box)
	}
	if err == nil {
		observed, err = c.Observe(ctx)
		if err == nil {
			err = matchCheckpoint(expected, parent, observed)
		}
	}
	localCancellation := workercheckpoint.IsLocalCancellation(err)
	if err == nil && ctx.Err() != nil {
		// All physical operations have settled. This final local caller check
		// cannot turn cancellation into a lost original stream. Original pins
		// are still checked below even on this non-poisoning outcome.
		err = errors.Join(ctx.Err(), context.Cause(ctx))
		localCancellation = true
	}
	// Original loss/expiry/refusal is sticky. No later File or handoff can
	// repair this object. Refusal does not itself Close the shared channel, so
	// existing uncertain/completion/result handling still has its sole owner.
	// An untouched canceled waiter/local metadata check returns its cause but
	// does not poison a healthy original. Active exchange cancellation closes
	// the stream and is not a local-cancellation outcome.
	return w.finishCheckpoint(err, localCancellation)
}
