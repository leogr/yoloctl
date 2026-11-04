package openshell

import (
	"context"
	"errors"
	"time"
)

// These two cold cases have no current kernel mutation to repeat: an
// independently completed no-init generation, or a different-boot generation
// whose old namespace is gone and whose current protected mapping is bare.
// Actual constructors below qualify that distinction before Stop. Metadata
// alone never supplies this invocation's ACK, nor cleanup/deletion authority.
type nativePreparedObservationRetirement struct {
	record  RuntimeRecord
	cid     string
	verify  func(context.Context) error
	release func() error
	// Borrowed original different-boot observation; release is sole owner.
	originalStaleFiles      *namespacePinFiles
	ready, consumed, closed bool
}

func (p *nativePreparedObservationRetirement) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	p.ready = false
	p.verify = nil
	p.originalStaleFiles = nil
	if p.release == nil {
		return nil
	}
	release := p.release
	p.release = nil
	return release()
}

func (p *nativePreparedObservationRetirement) afterContainerStop(ctx context.Context, ack *nativeContainerStopACK) (pinRetirementProgress, error) {
	if p == nil || p.closed || !p.ready || p.verify == nil {
		return pinRetirementProgress{}, errors.New("cold namespace retirement lacks its original prepared observation")
	}
	if p.consumed {
		return pinRetirementProgress{}, &NativeEffectsUncertain{Cause: errors.New("cold namespace retirement already attempted; no retry")}
	}
	if err := ack.covers(p.record, p.cid); err != nil {
		return pinRetirementProgress{}, err
	}
	p.consumed = true
	progress := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
	finish, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := finish.Err(); err != nil {
		return progress, &NativeEffectsUncertain{Cause: err}
	}
	if err := p.verify(finish); err != nil {
		return progress, &NativeEffectsUncertain{Cause: err}
	}
	progress.completed = true
	return progress, nil
}

// selected is borrowed from the complete original namespace inventory. It is
// not created or repaired here, and this handle never closes the parent's FD.
func prepareNativeNoInitializationRetirement(ctx context.Context, kernel *kernelInfrastructure, selected *noInitializationFiles, record RuntimeRecord, cid string, owner nativePinStopHooks) (*nativePreparedObservationRetirement, error) {
	if kernel == nil || selected == nil || selected.lock == nil || selected.lock.uid != 0 || selected.lock.gid != 0 || owner.owner == nil || owner.inactive == nil {
		return nil, errors.New("no-init retirement requires actual native root selection/owner callbacks")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if err := owner.owner(ctx); err != nil {
		return nil, err
	}
	check := func(c context.Context) error {
		if err := checkUninitializedStopSelection(selected, record, cid, kernel.bootID, kernel.mountID); err != nil {
			return err
		}
		return verifyNoInitializationKernel(c, kernel, selected.lock, record)
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &nativePreparedObservationRetirement{record: cloneRuntimeRecord(record), cid: cid, ready: true, verify: func(c context.Context) error {
		if err := owner.inactive(c); err != nil {
			return err
		}
		return check(c)
	}}, nil
}

// The protected stale reader refuses every current mount/NSFS leaf before
// opening the original regular placeholder. No old inode selects a new netns.
func prepareNativeStaleRetirement(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string, owner nativePinStopHooks) (*nativePreparedObservationRetirement, error) {
	if kernel == nil || lock == nil || lock.uid != 0 || lock.gid != 0 || owner.owner == nil || owner.inactive == nil {
		return nil, errors.New("different-boot retirement requires actual native root selection/owner callbacks")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if err := owner.owner(ctx); err != nil {
		return nil, err
	}
	selected, err := observeStaleNamespacePin(ctx, kernel, lock, record, cid)
	if err != nil {
		return nil, err
	}
	return &nativePreparedObservationRetirement{record: cloneRuntimeRecord(record), cid: cid, ready: true, release: selected.Close, originalStaleFiles: selected.files, verify: func(c context.Context) error {
		if err := owner.inactive(c); err != nil {
			return err
		}
		current, err := observeStaleNamespacePin(c, kernel, lock, record, cid)
		if err != nil {
			return err
		}
		defer func() { _ = current.Close() }()
		if !sameStalePinObservation(selected, current) {
			return errors.New("different-boot mapping changed across original container Stop")
		}
		return nil
	}}, nil
}
