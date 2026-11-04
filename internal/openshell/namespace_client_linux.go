package openshell

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/layout"
)

// NativeNamespaceStop belongs to the already admitted native lifecycle caller.
// VerifyOwned and VerifyInactive are read-only actual storage/record/CID/bind
// checks; the latter also proves current inactivity. Stop preserves the exact
// engine response. No callback deletes endpoints, keys, or generation material.
type NativeNamespaceStop struct {
	VerifyOwned    func(context.Context) error
	Stop           func(context.Context) error
	VerifyInactive func(context.Context) error
}

// StopNativeNamespace retires the selected unconfined generation's owned pin
// and policy after a fresh exact-CID Stop ACK. Recorded older controller/release
// generations are valid observation, never fresh launch authority. Missing or
// partial cold ownership fails before Stop; there is no engine-only fallback.
// The caller retains storage/entity admission and withdraws forwards first.
func StopNativeNamespace(ctx context.Context, record RuntimeRecord, release QualifiedRelease, cid string, owner NativeNamespaceStop) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil || !lowerHex(cid, 32) || !record.Launch.UnconfinedNetwork {
		return errors.New("native namespace Stop requires an unconfined generation, exact CID and admitted owner callbacks")
	}
	if err := nativeGenerationIdentity(record, release); err != nil {
		return err
	}
	if err := owner.VerifyOwned(ctx); err != nil {
		return err
	}
	return withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) error {
		controlPath, err := layout.OpenShellControlDir()
		if err != nil {
			return err
		}
		control, err := openNativeGenerationDirectory(controlPath)
		if err != nil {
			return err
		}
		defer func() { _ = control.Close() }()
		lock, err := lockExistingNamespacePinsAt(ctx, control, record.Launch.Binding.Incarnation, 0, 0)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		progress, err := stopOwnedNativeNamespace(ctx, kernel, lock, record, cid, nativePinStopHooks{owner.VerifyOwned, owner.Stop, owner.VerifyInactive})
		if err != nil {
			return err
		}
		if !progress.completed {
			return &NativeEffectsUncertain{Cause: errors.New("native namespace Stop lacks complete acknowledged retirement")}
		}
		return nil
	})
}
