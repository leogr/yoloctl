package controller

import (
	"context"
	"errors"
	"os"
	"testing"
)

type contextAnchorFixture struct {
	received                  context.Context
	legacyCalls, contextCalls int
}

func (f *contextAnchorFixture) Check() error { f.legacyCalls++; return nil }
func (*contextAnchorFixture) DuplicatePIDNamespace() (*os.File, error) {
	return nil, errors.New("fixture has no source/namespace")
}
func (f *contextAnchorFixture) CheckContext(ctx context.Context) error {
	f.contextCalls++
	f.received = ctx
	return ctx.Err()
}

type legacyAnchorContextFixture struct {
	calls  int
	cancel context.CancelCauseFunc
	cause  error
}

func (f *legacyAnchorContextFixture) Check() error {
	f.calls++
	if f.cancel != nil {
		f.cancel(f.cause)
	}
	return nil
}
func (*legacyAnchorContextFixture) DuplicatePIDNamespace() (*os.File, error) {
	return nil, errors.New("fixture has no source/namespace")
}
func TestWorkerPIDAnchorOriginalContextDispatchAndLegacyFallback(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	source := &contextAnchorFixture{}
	if err := checkWorkerPIDAnchor(ctx, source); err != nil || source.received != ctx || source.contextCalls != 1 || source.legacyCalls != 0 {
		t.Fatal("original context dropped", err)
	}
	legacy := &legacyAnchorContextFixture{}
	if err := checkWorkerPIDAnchor(ctx, legacy); err != nil || legacy.calls != 1 {
		t.Fatal("legacy non-context fixture fallback changed", err)
	}
	cause := errors.New("original caller canceled")
	legacy.cancel, legacy.cause = cancel, cause
	if err := checkWorkerPIDAnchor(ctx, legacy); !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatal("cancellation became healthy after legacy Check", err)
	}
	before := source.contextCalls
	if err := checkWorkerPIDAnchor(ctx, source); !errors.Is(err, cause) || source.contextCalls != before {
		t.Fatal("canceled caller touched source", err)
	}
}
