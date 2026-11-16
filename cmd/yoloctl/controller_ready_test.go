package main

import (
	"context"
	"errors"
	"testing"
	"time"

	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestControllerReadyBudgetAndRetry(t *testing.T) {
	if controllerReadyTimeout != 2*time.Minute {
		t.Fatal("unexpected deployment budget")
	}
	calls := 0
	err := waitControllerReady(t.Context(), func(ctx context.Context) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > vmhost.GuestProbeTimeout || !deadline.After(time.Now()) {
			t.Fatalf("per-attempt deadline %v", deadline)
		}
		if calls == 1 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("%v calls %d", err, calls)
	}
}

func TestControllerReadyCancellationWins(t *testing.T) {
	for _, before := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if before {
			cancel()
		}
		calls := 0
		err := waitControllerReady(ctx, func(context.Context) error { calls++; cancel(); return nil })
		cancel()
		expected := 1
		if before {
			expected = 0
		}
		if !errors.Is(err, context.Canceled) || calls != expected {
			t.Fatalf("%v calls %d", err, calls)
		}
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	err := waitControllerReady(ctx, func(context.Context) error { t.Fatal("called after deadline"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
