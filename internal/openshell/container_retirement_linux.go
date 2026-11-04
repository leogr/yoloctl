package openshell

import (
	"bytes"
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

type preparedNamespaceRetirement interface {
	afterContainerStop(context.Context, *nativeContainerStopACK) (pinRetirementProgress, error)
	Close() error
}

type nativeContainerRetirementEntry struct {
	record               RuntimeRecord
	action               preparedNamespaceRetirement
	pin                  *namespacePinFiles // borrowed original complete-inventory handle, if present
	sameBoot             bool
	origin, initialState []byte
	progress             pinRetirementProgress
}

// Built only inside the admitted native read scope. It composes actual
// preflight/refresh/retirement callbacks, not engine Stop/rm or deletion. The
// parent still supplies independent actual inactive/storage/process checks.
type nativeContainerRetirement struct {
	scope                                                  *nativeRemovalReadScope // borrowed; its locked-thread callback owns lifetime
	entries                                                []*nativeContainerRetirementEntry
	full                                                   *NativeContainerInventory
	ack                                                    *nativeContainerStopACK // only live inside the original retirement callback
	refreshAttempted, retirementAttempted, retired, closed bool
}

func (r *nativeContainerRetirement) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	var err error
	for n := len(r.entries) - 1; n >= 0; n-- {
		err = errors.Join(err, r.entries[n].action.Close())
	}
	r.entries = nil
	if r.full != nil {
		err = errors.Join(err, r.full.Close())
		r.full = nil
	}
	r.ack = nil
	r.scope = nil
	return err
}

// Every original generation is qualified before the enclosing protocol's one
// actual engine Stop. Missing/orphan/partial groups remain a pre-effect refusal.
// No new pin/origin/completion is initialized to make this preparation succeed.
func prepareNativeContainerRetirement(ctx context.Context, scope *nativeRemovalReadScope, inactive func(context.Context) error) (_ *nativeContainerRetirement, err error) {
	if scope == nil || inactive == nil {
		return nil, errors.New("complete retirement requires original native scope/inactivity callback")
	}
	if err := scope.check(ctx); err != nil {
		return nil, err
	}
	r := &nativeContainerRetirement{scope: scope}
	defer func() {
		if err != nil {
			_ = r.Close()
		}
	}()
	if !scope.selected.Launch.UnconfinedNetwork {
		return r, nil
	}
	pins := map[string]*namespacePinFiles{}
	noinit := map[string]*noInitializationFiles{}
	for _, f := range scope.namespaces.pins {
		if f == nil || f.lock != scope.lock || pins[f.record.Generation] != nil || f.currentPublication != nil || f.retiredPublication != nil {
			return nil, errors.New("retirement pin is not an original unique complete-inventory observation")
		}
		pins[f.record.Generation] = f
	}
	for _, f := range scope.namespaces.noInitialization {
		if f == nil || f.lock != scope.lock || noinit[f.marker.RecordSHA256] != nil {
			return nil, errors.New("retirement no-init selection is not an original unique observation")
		}
		noinit[f.marker.RecordSHA256] = f
	}
	owner := nativePinStopHooks{owner: scope.verifyOwned, inactive: inactive}
	for _, record := range scope.ownership.records() {
		e := &nativeContainerRetirementEntry{record: cloneRuntimeRecord(record)}
		f, completed := pins[record.Generation], noinit[recordIdentity(record)]
		if (f == nil) == (completed == nil) {
			return nil, errors.New("retirement generation has missing or conflicting namespace selection")
		}
		if completed != nil {
			e.action, err = prepareNativeNoInitializationRetirement(ctx, scope.kernel, completed, record, scope.cid, owner)
			delete(noinit, recordIdentity(record))
		} else {
			if recordIdentity(f.record) != recordIdentity(record) {
				return nil, errors.New("retirement pin changed original immutable record")
			}
			e.pin = f
			e.origin = bytes.Clone(f.origin)
			e.initialState = bytes.Clone(f.current)
			origin, decodeErr := decodeNamespacePinOrigin(f.origin, record, scope.cid)
			if decodeErr != nil {
				return nil, decodeErr
			}
			relation, decodeErr := origin.Record.Scope.classifyBoot(scope.kernel.bootID)
			if decodeErr != nil {
				return nil, decodeErr
			}
			switch relation {
			case pinSameBoot:
				e.sameBoot = true
				e.action, err = prepareNativePinRetirement(ctx, scope.kernel, f, owner)
			case pinDifferentBoot:
				e.action, err = prepareNativeStaleRetirement(ctx, scope.kernel, scope.lock, record, scope.cid, owner)
			default:
				return nil, errors.New("retirement generation has unknown kernel boot")
			}
			delete(pins, record.Generation)
		}
		if err != nil {
			return nil, err
		}
		r.entries = append(r.entries, e)
		// These checks run with the suffix's actual in-call progress, before
		// the next effect. The callbacks themselves retain their native checks.
		switch prepared := e.action.(type) {
		case *nativePreparedPinRetirement:
			check := prepared.hooks.check
			prepared.hooks.check = func(c context.Context, p pinRetirementProgress) error {
				if err := r.checkStage(c, e, p); err != nil {
					return err
				}
				return check(c, p)
			}
		case *nativePreparedObservationRetirement:
			verify := prepared.verify
			prepared.verify = func(c context.Context) error {
				if err := r.checkStage(c, e, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}); err != nil {
					return err
				}
				return verify(c)
			}
		}
	}
	if len(pins) != 0 || len(noinit) != 0 {
		return nil, errors.New("retirement omitted an original namespace group")
	}
	if err := scope.check(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *nativeContainerRetirement) refreshAfterStop(ctx context.Context, ack *nativeContainerStopACK) error {
	if r == nil || r.closed || r.scope == nil || r.scope.closed || r.scope.ownership == nil || r.scope.ownership.view == nil || r.refreshAttempted {
		return errors.New("native retirement refresh is closed or already attempted")
	}
	if err := ack.coversInventory(r.scope.ownership.view, r.scope.cid); err != nil {
		return err
	}
	r.refreshAttempted = true
	full, err := r.scope.ownership.afterStop(ctx, r.scope.release, r.scope.cid, ack)
	if err != nil {
		return err
	}
	r.full = full
	return nil
}

func (r *nativeContainerRetirement) retireAfterStop(ctx context.Context, ack *nativeContainerStopACK) error {
	if r == nil || r.closed || r.scope == nil || r.scope.closed || r.full == nil || r.retirementAttempted {
		return errors.New("native retirement requires original full refresh and one live attempt")
	}
	if err := ack.coversInventory(r.full, r.scope.cid); err != nil {
		return err
	}
	r.retirementAttempted = true
	r.ack = ack
	defer func() { r.ack = nil }()
	if err := r.checkStage(ctx, nil, pinRetirementProgress{}); err != nil {
		return err
	}
	for _, e := range r.entries {
		progress, err := e.action.afterContainerStop(ctx, ack)
		if err != nil {
			return err
		}
		if !progress.completed {
			return errors.New("native generation retirement omitted completion")
		}
		e.progress = progress
		if err := r.checkStage(ctx, nil, pinRetirementProgress{}); err != nil {
			return err
		}
	}
	r.retired = true
	return nil
}

func (r *nativeContainerRetirement) checkStage(ctx context.Context, active *nativeContainerRetirementEntry, p pinRetirementProgress) error {
	if r == nil || r.closed || r.scope == nil || r.full == nil || r.ack == nil {
		return errors.New("retirement stage lacks original live ACK/full inventory")
	}
	if err := r.ack.coversInventory(r.full, r.scope.cid); err != nil {
		return err
	}
	if err := r.scope.checkRuntime(ctx); err != nil {
		return err
	}
	if err := r.full.Check(ctx); err != nil {
		return err
	}
	if !r.scope.selected.Launch.UnconfinedNetwork {
		if len(r.entries) != 0 {
			return errors.New("confined retirement acquired namespace actions")
		}
		return verifyConfinedNamespaceAbsence(r.scope.control, r.scope.controlIdentity, r.scope.selected.Launch.Binding.Incarnation)
	}
	return compareCurrentRetirementNamespaceStage(ctx, r.scope.namespaces, r.entries, active, p, r.scope.lock.directories[4].identity.mount)
}

// Shared complete stage read used by the actual parent's independently
// admitted/runtime-checked callback above and owned-file composition fixtures.
func compareCurrentRetirementNamespaceStage(ctx context.Context, original *namespaceGenerationInventory, entries []*nativeContainerRetirementEntry, active *nativeContainerRetirementEntry, p pinRetirementProgress, nativeMount uint64) error {
	if original == nil {
		return errors.New("namespace stage lacks original inventory")
	}
	next, err := original.readCurrent(ctx)
	if err != nil {
		return err
	}
	defer next.Close() // No transient bind FD may survive an actual unmount.
	return compareRetirementNamespaceStage(original, next, entries, active, p, nativeMount)
}

func publicationInventoryMember(publication *namespacePinPublication, mount uint64, uid, gid int) (namespaceInventoryMember, error) {
	if publication == nil || publication.identity.device == 0 || publication.identity.inode == 0 || publication.identity.mount == 0 || mount == 0 {
		return namespaceInventoryMember{}, errors.New("missing original successful publication identity")
	}
	id := publication.identity
	id.mount = mount
	return namespaceInventoryMember{identity: id, mode: unix.S_IFREG | 0600, uid: uint32(uid), gid: uint32(gid), links: 1, size: int64(len(publication.raw))}, nil
}
