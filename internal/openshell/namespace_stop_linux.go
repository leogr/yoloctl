package openshell

import (
	"bytes"
	"context"
	"errors"
	"time"
)

type stalePinStopHooks struct {
	beforeStop             func(context.Context) error
	stop                   func(context.Context) error
	verifyInactiveAndStale func(context.Context) error
}

// Different-boot completion concerns this new Stop only. Its single mutation
// is the current exact-CID engine Stop; the old kernel's policy/pin is never
// entered, deleted, unmounted or reconstructed from a recycled inode.
func runStalePinStop(ctx context.Context, h stalePinStopHooks) (p pinRetirementProgress, err error) {
	if h.beforeStop == nil || h.stop == nil || h.verifyInactiveAndStale == nil {
		return p, errors.New("incomplete different-boot Stop protocol")
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if err := h.beforeStop(ctx); err != nil {
		return p, err
	}
	p.stopAttempted = true
	if err := h.stop(ctx); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.stopAcknowledged = true
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := cleanup.Err(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	if err := h.verifyInactiveAndStale(cleanup); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.completed = true
	return p, nil
}

// This comparison is observation only. It neither manufactures an old effect
// ACK nor permits replacing/refilling missing metadata. Each input to the actual
// adapter below must come from its separately validated native reader.
func sameStalePinObservation(a, b *staleNamespacePinObservation) bool {
	if a == nil || b == nil || a.files == nil || b.files == nil || a.files.directory == nil || b.files.directory == nil || a.files.lock == nil || b.files.lock == nil {
		return false
	}
	x, y := a.files, b.files
	return a.currentBoot == b.currentBoot && canonicalUUID(a.currentBoot) && a.placeholder == b.placeholder && x.identity == y.identity && x.lock.identity == y.lock.identity && x.lock.incarnation == y.lock.incarnation && x.cid == y.cid && recordIdentity(x.record) == recordIdentity(y.record) && bytes.Equal(x.origin, y.origin) && bytes.Equal(x.current, y.current)
}

func stopStaleNativeNamespacePin(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string, owner nativePinStopHooks) (pinRetirementProgress, error) {
	if owner.owner == nil || owner.stop == nil || owner.inactive == nil {
		return pinRetirementProgress{}, errors.New("different-boot Stop lacks admitted current-owner checks")
	}
	var selected *staleNamespacePinObservation
	defer func() {
		if selected != nil {
			_ = selected.Close()
		}
	}()
	h := stalePinStopHooks{
		beforeStop: func(c context.Context) error {
			if err := owner.owner(c); err != nil {
				return err
			}
			var err error
			selected, err = observeStaleNamespacePin(c, kernel, lock, record, cid)
			return err
		},
		stop: owner.stop,
		verifyInactiveAndStale: func(c context.Context) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			current, err := observeStaleNamespacePin(c, kernel, lock, record, cid)
			if err != nil {
				return err
			}
			defer func() { _ = current.Close() }()
			if !sameStalePinObservation(selected, current) {
				return errors.New("different-boot namespace metadata/current mapping changed across Stop")
			}
			return nil
		},
	}
	return runStalePinStop(ctx, h)
}

// stopOwnedNativeNamespace is the shared private cold-retirement core for
// normal Stop and owned failed-create cleanup.
// Its caller must supply actual admitted storage/release/record/CID checks and
// acknowledgement-preserving engine Stop. owner/inactive callbacks are pure;
// they must not remove keys/sockets or require an old dead PID to remain live.
// A current running instance must retain the caller's independently selected
// process/start binding; metadata is never fresh launch authority.
func stopOwnedNativeNamespace(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string, owner nativePinStopHooks) (pinRetirementProgress, error) {
	if err := kernel.revalidate(ctx); err != nil {
		return pinRetirementProgress{}, err
	}
	if owner.owner == nil || owner.stop == nil || owner.inactive == nil || lock == nil || lock.uid != 0 || lock.gid != 0 {
		return pinRetirementProgress{}, errors.New("owned namespace Stop requires admitted native-root owner callbacks/lock")
	}
	if err := owner.owner(ctx); err != nil {
		return pinRetirementProgress{}, err
	}
	if err := lock.check(); err != nil {
		return pinRetirementProgress{}, err
	}
	if record.Launch.Binding.Incarnation != lock.incarnation || !namespacePinGenerationName(record.Generation) || !lowerHex(cid, 32) {
		return pinRetirementProgress{}, errors.New("owned namespace Stop lacks its exact incarnation/generation/CID")
	}
	noinit, found, err := readNoInitializationAt(lock, record, cid)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	if found {
		defer func() { _ = noinit.Close() }()
		return stopUninitializedNativeGeneration(ctx, kernel, lock, record, cid, owner, noinit)
	}
	raw, _, err := readPinFileAt(int(lock.directories[3].file.Fd()), record.Generation+".json", 0, 0, 4<<20)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	origin, err := decodeNamespacePinOrigin(raw, record, cid)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	relation, err := origin.Record.Scope.classifyBoot(kernel.bootID)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	if relation == pinDifferentBoot {
		return stopStaleNativeNamespacePin(ctx, kernel, lock, record, cid, owner)
	}
	if relation != pinSameBoot {
		return pinRetirementProgress{}, errors.New("owned namespace Stop has unknown kernel boot")
	}
	files, err := lock.read(record, cid)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	defer func() { _ = files.Close() }()
	return stopNativeNamespacePin(ctx, kernel, files, owner)
}
