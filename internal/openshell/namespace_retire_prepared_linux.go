package openshell

import (
	"context"
	"errors"
	"os"
	"time"
)

// Only prepareNativePinRetirement constructs this descriptor in production,
// after actual native kernel/owner/metadata/NSFS qualification. The metadata
// and incarnation lock are borrowed from the enclosing complete inventory;
// this handle owns only its checked namespace FD and one retirement attempt.
// It is not serializable and is never reconstructed from a phase or marker.
type nativePreparedPinRetirement struct {
	record    RuntimeRecord
	cid       string
	completed bool
	hooks     pinRetirementHooks
	release   func() error
	// Reads the SAME lexical namespace variable owned by release/retirement.
	// Observing it is not a Close ACK or permission to exempt the namespace.
	remainingNamespace      func() *os.File
	ready, consumed, closed bool
}

func (p *nativePreparedPinRetirement) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	p.ready = false
	p.hooks = pinRetirementHooks{}
	p.remainingNamespace = nil
	if p.release == nil {
		return nil
	}
	release := p.release
	p.release = nil
	return release()
}

// The parent first qualifies ALL retained generations, obtains its single
// original exact-CID Stop reply, independently proves inactivity and refreshes
// the full runtime inventory. This method consumes that live scoped ACK for
// one prequalified same-boot generation. It never invokes a second Stop or
// substitutes a no-op Stop callback. No metadata deletion is authorized here.
func (p *nativePreparedPinRetirement) afterContainerStop(ctx context.Context, ack *nativeContainerStopACK) (pinRetirementProgress, error) {
	if p == nil || p.closed || !p.ready || p.release == nil {
		return pinRetirementProgress{}, errors.New("native pin retirement was not prepared or is closed")
	}
	if p.consumed {
		return pinRetirementProgress{}, &NativeEffectsUncertain{Cause: errors.New("native pin retirement already attempted; no retry")}
	}
	if err := ack.covers(p.record, p.cid); err != nil {
		return pinRetirementProgress{}, err
	}
	p.consumed = true
	// The enclosing protocol already owns bounded cleanup after its actual
	// Stop ACK. Respect its cancellation/deadline; never extend it per member.
	cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	progress := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
	return continueAcknowledgedPinRetirement(cleanup, p.completed, p.hooks, progress)
}
