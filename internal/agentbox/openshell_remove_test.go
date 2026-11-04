package agentbox

import (
	"context"
	"errors"
	"testing"
)

func TestNativeRemovalHandleIsSingleUseAndRetainsCloseFailure(t *testing.T) {
	want := errors.New("original close failure")
	calls, closes := 0, 0
	r := &NativeContainerRemoval{remove: func(context.Context) error { calls++; return nil }, close: func() error { closes++; return want }}
	if err := r.Remove(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(t.Context()); err == nil {
		t.Fatal("remove replayed")
	}
	firstClose, secondClose := r.Close(), r.Close()
	if !errors.Is(firstClose, want) || !errors.Is(secondClose, want) || calls != 1 || closes != 1 {
		t.Fatal("original close result lost or effect replayed", calls, closes)
	}
}
func TestNativeRemovalCanceledBeforeFirstEffect(t *testing.T) {
	called := false
	r := &NativeContainerRemoval{remove: func(context.Context) error { called = true; return nil }}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(r.Remove(ctx), context.Canceled) || called {
		t.Fatal("canceled remove reached effect")
	}
	if _, _, err := (LiveApplier{}).PrepareNativeContainerRemoval(t.Context(), "fixture", false); err == nil {
		t.Fatal("missing client accepted")
	}
}
