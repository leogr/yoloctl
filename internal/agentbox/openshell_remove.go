package agentbox

import (
	"context"
	"errors"
	"sync"
)

// NativeContainerRemoval owns the read-only preparation and one native removal
// attempt. Close is part of the result; a later idempotent Close retains its
// first error. It never deletes declarations, host projects or account state.
type NativeContainerRemoval struct {
	mu                sync.Mutex
	remove            func(context.Context) error
	close             func() error
	attempted, closed bool
	closeErr          error
}

func (r *NativeContainerRemoval) Remove(ctx context.Context) error {
	if r == nil {
		return errors.New("native removal preparation unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.attempted || r.remove == nil {
		return errors.New("native removal already consumed or unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.attempted = true
	return r.remove(ctx)
}
func (r *NativeContainerRemoval) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	if r.close != nil {
		r.closeErr = r.close()
	}
	return r.closeErr
}

// PrepareNativeContainerRemoval classifies actual inspected evidence before
// caller holder/forward effects. False selects only the unchanged legacy or
// privileged route; partial native evidence is an error, never a fallback.
func (a LiveApplier) PrepareNativeContainerRemoval(ctx context.Context, name string, force bool) (*NativeContainerRemoval, bool, error) {
	if a.Client == nil {
		return nil, false, errors.New("removal requires a container client")
	}
	details, err := a.Client.Inspect(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if !hasOpenShellRuntimeSignals(details) {
		return nil, false, nil
	}
	if ClassifyRuntime(details).Mode != RuntimeModeConfined {
		return nil, true, errors.New("native removal refuses mixed or incomplete runtime evidence")
	}
	r, err := a.prepareNativeContainerRemoval(ctx, name, details.ID, force)
	return r, true, err
}
