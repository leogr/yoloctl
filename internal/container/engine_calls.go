package container

import (
	"context"
	"sync"
)

// EngineCalls records, for one command, the engine starts and stops it
// invoked through this client: the real boundaries box start's result policy
// decides from. The client marks a call right before invoking the engine,
// whatever the reply; nothing else marks it.
type EngineCalls struct {
	mu                sync.Mutex
	started           bool
	stoppedAfterStart bool
	parent            *EngineCalls // an enclosing record also sees these calls
}

type engineCallsKey struct{}

// WithEngineCalls makes this client's engine starts and stops under ctx
// recorded in calls, and in any record already under ctx: a nested step
// (a policy restart's launch) can tell its own calls apart while the
// enclosing operation (box start) still sees them.
func WithEngineCalls(ctx context.Context, calls *EngineCalls) context.Context {
	if parent := EngineCallsFrom(ctx); parent != nil && parent != calls && calls.parent == nil {
		calls.parent = parent
	}
	return context.WithValue(ctx, engineCallsKey{}, calls)
}

// EngineCallsFrom returns the record under ctx, or nil.
func EngineCallsFrom(ctx context.Context) *EngineCalls {
	calls, _ := ctx.Value(engineCallsKey{}).(*EngineCalls)
	return calls
}

// Started reports an engine start invoked under the recording context.
func (e *EngineCalls) Started() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

// StoppedAfterStart reports an engine stop invoked after the latest start,
// such as a failed start's own cleanup. A stop before it (a previous
// generation's, or one before a later start such as a policy restart's)
// does not count.
func (e *EngineCalls) StoppedAfterStart() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stoppedAfterStart
}

// MarkStart and MarkStop are called by the client at the engine boundary.
func (e *EngineCalls) MarkStart() {
	e.mu.Lock()
	e.started = true
	e.stoppedAfterStart = false // a new attempt: its own stops count afresh
	parent := e.parent
	e.mu.Unlock()
	if parent != nil {
		parent.MarkStart()
	}
}

func (e *EngineCalls) MarkStop() {
	e.mu.Lock()
	e.stoppedAfterStart = e.stoppedAfterStart || e.started
	parent := e.parent
	e.mu.Unlock()
	if parent != nil {
		parent.MarkStop()
	}
}

func markEngineStart(ctx context.Context) {
	if calls := EngineCallsFrom(ctx); calls != nil {
		calls.MarkStart()
	}
}

func markEngineStop(ctx context.Context) {
	if calls := EngineCallsFrom(ctx); calls != nil {
		calls.MarkStop()
	}
}
