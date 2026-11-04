//go:build linux

package workercheckpoint

import "testing"

// Ordinary physical transport ONLY; no handoff/SDK/native source constructed.
func TestObserveNilContextIsUnavailableNotLocalCancellation(t *testing.T) {
	peer, channel := pair(t)
	if _, err := channel.Observe(nil); err == nil || IsLocalCancellation(err) { //nolint:staticcheck // Exercise the explicit nil-context rejection without touching the transport.
		t.Fatal("nil context became a local cancellation", err)
	}
	// Invalid local request must not close/touch the original physical owner.
	permitBegin(t, channel, peer)
}
