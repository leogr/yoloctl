package openshell

import (
	"context"
	"errors"
	"time"
)

// StopStartedGeneration retires only this activation's owned namespace after
// a fresh actual Stop ACK, preserving its CID/private files for a separately
// authorized fresh-generation rollback. false,nil means no reservation was
// ever made; the admitted caller still owns ordinary exact-CID Stop. Any error
// forbids that fallback. It never authorizes another generation or settles an
// older receipt, and cannot be repeated after failed cleanup/publication.
func (a *NativeActivation) StopStartedGeneration(ctx context.Context, owner NativeNamespaceStop) (handled bool, err error) {
	if a == nil {
		return false, nil
	}
	if a.closed || a.cleanupAttempted {
		return true, &NativeEffectsUncertain{Cause: errors.New("generation activation cleanup is closed/consumed; no retry")}
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(a.failure, &uncertain) {
		return true, a.failure
	}
	if a.reservation == nil {
		if a.lock != nil {
			return true, &NativeEffectsUncertain{Cause: errors.New("generation namespace initialization lacks its complete original reservation")}
		}
		return false, nil
	}
	if owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil {
		return true, errors.New("generation cleanup lacks actual admitted owner callbacks")
	}
	if err := nativePreparedIdentity(a.prepared); err != nil {
		return true, err
	}
	a.cleanupAttempted = true
	present := a.namespace != nil && a.namespace.progress.bindAcknowledged && a.namespace.progress.pinRecorded
	if present {
		if err := a.closeMountReferences(); err != nil {
			return true, &NativeEffectsUncertain{Cause: err}
		}
	}
	err = withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) error {
		stop := nativePinStopHooks{owner.VerifyOwned, owner.Stop, owner.VerifyInactive}
		if present {
			progress, err := stopOwnedNativeNamespace(ctx, kernel, a.lock, a.prepared.Record, a.cid, stop)
			if err != nil {
				return err
			}
			if !progress.completed {
				return &NativeEffectsUncertain{Cause: errors.New("generation namespace Stop lacks completed retirement")}
			}
			return nil
		}
		var err error
		a.partial, err = retireKnownUnboundNativePin(ctx, kernel, a.reservation, a.directory, a.namespace, stop)
		if err != nil {
			return err
		}
		if a.partial == nil || a.partial.failure != nil || !a.partial.progress.ready {
			return &NativeEffectsUncertain{Cause: errors.New("generation cleanup lacks its original completed partial retirement")}
		}
		complete, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := owner.VerifyInactive(complete); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		if err := verifyNamespaceRemovalMapping(complete, kernel, a.reservation.files, a.partial); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		if err := a.partial.publishKnownUnboundRetired(); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		return nil
	})
	return true, err
}
