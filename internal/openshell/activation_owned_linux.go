package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
)

// NativeActivation retains only this call's private namespace creation state.
// Close releases descriptors/lock, never an owned mount or a running workload.
// Failed/lost mutations retain uncertainty; no method replays authorization.
type NativeActivation struct {
	prepared                 PreparedRuntime
	cid                      string
	lock                     *namespacePinLock
	reservation              *namespacePinReservation
	directory                *nativePinDirectoryAttempt
	namespace                *nativePinAttempt
	partial                  *nativeUnboundPinRetirement
	failure                  error
	closed, cleanupAttempted bool
}

func (*NativeActivation) String() string   { return "OpenShell native activation [redacted]" }
func (*NativeActivation) GoString() string { return "OpenShell native activation [redacted]" }

func (a *NativeActivation) Close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	err := a.closeMountReferences()
	if a.lock != nil {
		err = errors.Join(err, a.lock.Close())
	}
	return err
}

func (a *NativeActivation) closeMountReferences() error {
	if a.namespace != nil {
		return a.namespace.Close()
	}
	if a.directory != nil {
		return a.directory.Close()
	}
	if a.reservation != nil {
		return a.reservation.Close()
	}
	return nil
}

func (a *NativeActivation) createOwnedEgress(ctx context.Context, peer *activationPeer, received *os.File) (proof activationProof, err error) {
	if a != nil && (a.closed || a.cleanupAttempted) {
		return proof, &NativeEffectsUncertain{Cause: errors.New("owned activation was consumed/closed; no creation retry")}
	}
	if a != nil && (a.lock != nil || a.reservation != nil) {
		return proof, &NativeEffectsUncertain{Cause: errors.New("owned activation namespace initialization was already attempted; no retry")}
	}
	if a == nil || peer == nil || received == nil {
		return proof, errors.New("owned activation lacks its unused live peer/namespace state")
	}
	if !a.prepared.Record.Launch.UnconfinedNetwork {
		return checkedActivationProof(a.prepared.Record, peer.identity, nil, nil)
	}
	a.cid = peer.target.ContainerID
	err = withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) error {
		if err := peer.revalidate(ctx); err != nil {
			return err
		}
		plan, err := discoverNativeEgressPlan(ctx, received, a.prepared.Record, peer.identity)
		if err != nil {
			return err
		}
		if err := peer.revalidate(ctx); err != nil {
			return err
		}
		scope, err := newNamespacePinScope(a.prepared.Record, peer.target, peer.start, peer.identity, kernel.mountID, kernel.bootID)
		if err != nil {
			return err
		}
		controlPath, err := layout.OpenShellControlDir()
		if err != nil {
			return err
		}
		control, err := openNativeGenerationDirectory(controlPath)
		if err != nil {
			return err
		}
		defer func() { _ = control.Close() }()
		a.lock, err = lockNamespacePinsAt(ctx, control, a.prepared.Record.Launch.Binding.Incarnation, 0, 0)
		if err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		if err := rejectNoInitializationAt(a.lock, a.prepared.Record); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		a.reservation, err = a.lock.reserve(scope, a.prepared.Record, a.cid, plan)
		if err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		a.directory, err = createNativePinDirectory(ctx, kernel, a.reservation, peer)
		if err != nil {
			return err
		}
		a.namespace, err = createNativeNamespacePin(ctx, kernel, a.directory, peer, received)
		if err != nil {
			return err
		}
		proof = a.namespace.proof
		return nil
	})
	return proof, err
}

// NativeActivationCleanup supplies fixed native infrastructure callbacks.
// VerifyOwned/VerifyInactive must check actual storage, original CID/record/
// launch/binds and current process or inactivity. VerifyRemaining checks the
// original runtime record/keys/storage after CID removal, never an absent-CID
// observation. Withdraw forwards before calling; callbacks do no socket/key
// deletion. Only Stop and RemoveContainerID may mutate the engine.
type NativeActivationCleanup struct {
	VerifyOwned       func(context.Context) error
	Stop              func(context.Context) error
	VerifyInactive    func(context.Context) error
	RemoveContainerID func(context.Context, string) error
	VerifyRemaining   func(context.Context) error
}

// RemoveCreatedContainer handles this call's owned unconfined namespace and
// exact CID removal. false,nil means no namespace reservation was ever made;
// the enclosing creator still owns its ordinary engine/runtime cleanup. Any
// error prevents that fallback. Runtime keys are the following caller step.
func (a *NativeActivation) RemoveCreatedContainer(ctx context.Context, owner NativeActivationCleanup) (handled bool, err error) {
	if a == nil {
		return false, nil
	}
	if a.closed || a.cleanupAttempted {
		return true, &NativeEffectsUncertain{Cause: errors.New("activation cleanup handle is closed/consumed; no retry")}
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(a.failure, &uncertain) {
		return true, a.failure
	}
	if a.reservation == nil {
		if a.lock != nil {
			return true, &NativeEffectsUncertain{Cause: errors.New("namespace initialization lacks its complete original reservation")}
		}
		return false, nil
	}
	if owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil || owner.RemoveContainerID == nil || owner.VerifyRemaining == nil {
		return true, errors.New("owned activation cleanup lacks actual native owner callbacks")
	}
	a.cleanupAttempted = true
	stop := nativePinStopHooks{owner.VerifyOwned, owner.Stop, owner.VerifyInactive}
	remove := namespaceContainerRemoveOwner{stop, owner.RemoveContainerID, owner.VerifyRemaining}
	// A present-pin path reopens under the retained ownership lock. Close our
	// own mounted NSFS and directory references before normal cold unmounts.
	present := a.namespace != nil && a.namespace.progress.bindAcknowledged && a.namespace.progress.pinRecorded
	if present {
		if err := a.closeMountReferences(); err != nil {
			return true, &NativeEffectsUncertain{Cause: err}
		}
	}
	err = withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) error {
		if !present {
			var err error
			a.partial, err = retireKnownUnboundNativePin(ctx, kernel, a.reservation, a.directory, a.namespace, stop)
			if err != nil {
				return err
			}
		}
		_, err := removeNativeNamespaceContainer(ctx, kernel, a.lock, a.prepared.Record, a.cid, remove, a.partial)
		return err
	})
	return true, err
}

// VerifyPreparedNativeRecord is read-only ownership evidence for the retained
// create-time generation, including after its CID was positively removed. It
// supplies neither container history nor cleanup/admission authority.
func VerifyPreparedNativeRecord(ctx context.Context, prepared PreparedRuntime) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := nativePreparedIdentity(prepared); err != nil {
		return err
	}
	root, err := openNativeGenerationDirectory(prepared.ContainerRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	owners := runtimeOwners{0, 0, int(prepared.Record.Launch.UID), int(prepared.Record.Launch.GID), int(SupervisorUID), int(SupervisorGID)}
	return verifyPreparedNativeRecordAt(root, prepared.Record, owners)
}

func verifyPreparedNativeRecordAt(root *os.File, record RuntimeRecord, owners runtimeOwners) error {
	if root == nil {
		return errors.New("prepared native record verifier lacks its retained root")
	}
	if err := verifyGenerationSelection(root, record, owners); err != nil {
		return err
	}
	generations, err := runtimeDirectoryAt(root, "generations", owners.rootUID, owners.rootGID)
	if err != nil {
		return err
	}
	defer func() { _ = generations.Close() }()
	generation, err := runtimeDirectoryAt(generations, record.Generation, owners.rootUID, owners.rootGID)
	if err != nil {
		return err
	}
	defer func() { _ = generation.Close() }()
	raw, err := runtimeReadAt(generation, "record.json", owners.rootUID, owners.rootGID, 1<<20)
	if err != nil {
		return err
	}
	want, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, want) {
		return errors.Join(err, errors.New("retained prepared generation record changed"))
	}
	return nil
}
