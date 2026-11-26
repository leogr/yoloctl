package operator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// An endpoint that is never served ends the wait with today's error: at the
// fail-safe, at an earlier caller deadline, or at the caller's cancel.
func TestWaitForEndpointEndsWithFailSafeOrCaller(t *testing.T) {
	if endpointWaitFailSafe != 2*time.Minute {
		t.Fatal("endpoint wait fail-safe changed", endpointWaitFailSafe)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	e := Endpoint{Box: "fixture", Incarnation: hex.EncodeToString(nonce[:])}
	wait := func(ctx context.Context, want error) {
		t.Helper()
		start := time.Now()
		err := waitForEndpoint(ctx, e)
		if !errors.Is(err, want) || !strings.HasPrefix(err.Error(), "wait for operator endpoint: ") || time.Since(start) > 20*time.Second {
			t.Fatal("unserved endpoint wait did not end as before", err, time.Since(start))
		}
	}
	caller, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	wait(caller, context.DeadlineExceeded)
	cancelled, cancelNow := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancelNow)
	wait(cancelled, context.Canceled)

	old := endpointWaitFailSafe
	endpointWaitFailSafe = 200 * time.Millisecond
	defer func() { endpointWaitFailSafe = old }()
	wait(t.Context(), context.DeadlineExceeded)
}
