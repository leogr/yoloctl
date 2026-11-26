package term

import (
	"testing"
	"time"
)

// The sweep's rule: one image or step-click write that nothing else ends
// stops at 2 minutes.
func TestChannelWriteFailSafeIsTwoMinutes(t *testing.T) {
	if channelWriteFailSafe != 2*time.Minute {
		t.Fatalf("fail-safe %s", channelWriteFailSafe)
	}
}
