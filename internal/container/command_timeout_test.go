package container

import (
	"context"
	"testing"
	"time"
)

// These tests assert command semantics, not how quickly a loaded host starts
// the shell/Python fixture. Keep the parent deadline/cancellation and assert
// the real production budget at the call site before applying a test watchdog.
func fixtureCommandWatchdog(t *testing.T, c *Client, expected time.Duration) {
	t.Helper()
	calls := 0
	c.newTimeout = func(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
		calls++
		if budget != expected {
			t.Errorf("production budget changed: got %v, want %v", budget, expected)
		}
		return context.WithTimeout(ctx, 30*time.Second)
	}
	t.Cleanup(func() {
		if calls != 1 {
			t.Errorf("expected one bounded probe, got %d", calls)
		}
	})
}

func TestCommandTimeoutDefaultsAndParentCancellation(t *testing.T) {
	c := New("fixture")
	if c.newTimeout != nil {
		t.Fatal("normal clients must use production timeout construction")
	}
	for _, budget := range []time.Duration{3 * time.Second, 5 * time.Second} {
		before := time.Now()
		ctx, cancel := c.withTimeout(t.Context(), budget)
		after := time.Now()
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(before.Add(budget)) || deadline.After(after.Add(budget)) {
			t.Fatal("default budget changed", deadline, budget)
		}
		cancel()
		if ctx.Err() != context.Canceled {
			t.Fatal("cancel did not propagate", ctx.Err())
		}
	}
	parentDeadline := time.Now().Add(time.Second)
	earlier, stopEarlier := context.WithDeadline(t.Context(), parentDeadline)
	child, stopChild := c.withTimeout(earlier, 5*time.Second)
	if deadline, ok := child.Deadline(); !ok || !deadline.Equal(parentDeadline) {
		t.Fatal("earlier parent deadline lost", deadline)
	}
	stopChild()
	stopEarlier()

	parent, stop := context.WithCancel(t.Context())
	ctx, cancel := c.withTimeout(parent, 3*time.Second)
	defer cancel()
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal("parent cancellation lost", ctx.Err())
	}
}
