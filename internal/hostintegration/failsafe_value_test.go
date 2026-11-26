package hostintegration

import (
	"testing"
	"time"
)

// The sweep's rule: a single wait that nothing else ends stops at 2 minutes.
func TestFailSafeIsTwoMinutes(t *testing.T) {
	if failSafe != 2*time.Minute {
		t.Fatalf("fail-safe %s", failSafe)
	}
}
