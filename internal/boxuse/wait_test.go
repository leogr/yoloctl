package boxuse

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitSettlesAfterLatestChange(t *testing.T) {
	const settle = 40 * time.Millisecond
	for _, tc := range []struct {
		name  string
		reset bool
	}{
		{name: "single change"},
		{name: "second change", reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newState()
				publishChange := func() time.Time {
					s.mu.Lock()
					defer s.mu.Unlock()
					s.screen.Revision++
					s.notify()
					return time.Now()
				}
				lastChange := publishChange()

				type result struct {
					finished time.Time
					timedOut bool
					err      error
				}
				done := make(chan result, 1)
				go func() {
					timedOut, err := s.wait(context.Background(), 0, time.Second, settle)
					done <- result{time.Now(), timedOut, err}
				}()
				synctest.Wait()
				if tc.reset {
					time.Sleep(settle / 2)
					lastChange = publishChange()
				}

				got := <-done
				if got.err != nil || got.timedOut {
					t.Fatalf("wait did not settle: timed out = %t, error = %v", got.timedOut, got.err)
				}
				if elapsed := got.finished.Sub(lastChange); elapsed < settle {
					t.Fatalf("wait returned %v after the latest change, want at least %v", elapsed, settle)
				}
			})
		})
	}
}
