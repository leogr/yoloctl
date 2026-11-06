package term

import (
	"context"
	"testing"
	"time"
)

// Native snapshot setup in the real child is a fixture cost, not a three-second
// product guarantee. Only the affected readiness checks use this watchdog.
func awaitManagedFixture(t *testing.T, label string, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out: %s: %v", label, ctx.Err())
		case <-tick.C:
		}
	}
}
