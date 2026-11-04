package openshell

import (
	"context"
	"errors"
	"fmt"
)

// NativeContainerRemoval belongs to the admitted infrastructure lifecycle
// caller. Each read proves current storage plus actual fixed-CID/record/bind
// ownership or complete remaining consumers. VerifyRemaining cannot inspect an
// already removed CID or depend on private records intentionally being deleted.
// Endpoints have separate original descriptor/registration ownership; their
// withdrawal callback must preserve every failed/lost response, never sweep by
// box name. Holders and forwards are the separately acknowledged caller prefix.
type NativeContainerRemoval struct {
	VerifyOwned       func(context.Context) error
	Stop              func(context.Context) error
	VerifyInactive    func(context.Context) error
	RemoveContainerID func(context.Context, string) error
	VerifyRemaining   func(context.Context) error
	WithdrawEndpoints func(context.Context) error
}

// RemoveNativeContainer composes existing native proofs and original-call
// effects for the ENTIRE selected incarnation, including historical generations.
// It is not failed-create rollback, a per-generation Stop or recovery/adoption.
// One Stop and one nonforced full-CID rm belong to this invocation. Replacement
// artifacts/image/ENTRYPOINT must be qualified by the caller before admission
// to destructive recreate, and the caller must withhold completion on error.
func RemoveNativeContainer(ctx context.Context, selected RuntimeRecord, release QualifiedRelease, cid string, owner NativeContainerRemoval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil || owner.RemoveContainerID == nil || owner.VerifyRemaining == nil || owner.WithdrawEndpoints == nil {
		return errors.New("native container removal requires complete actual owner/engine/remaining/endpoint callbacks")
	}
	err := withNativeRemovalReadScope(ctx, selected, release, cid, owner.VerifyOwned, func(scope *nativeRemovalReadScope) (err error) {
		retirement, err := prepareNativeContainerRetirement(ctx, scope, owner.VerifyInactive)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, retirement.Close()) }()
		var namespace *nativeNamespaceCleanup
		var runtime *nativeRuntimeCleanup
		original := nativeContainerRemovalScope{cid: cid, currentGeneration: selected.Generation, records: scope.ownership.records()}
		actions := nativeContainerRemovalActions{
			qualify:  scope.check,
			stop:     owner.Stop,
			inactive: owner.VerifyInactive,
			refresh:  retirement.refreshAfterStop,
			retire: func(c context.Context, ack *nativeContainerStopACK) error {
				if err := retirement.retireAfterStop(c, ack); err != nil {
					return err
				}
				var err error
				namespace, err = prepareNativeNamespaceCleanup(c, retirement, ack)
				if err != nil {
					return err
				}
				// Both exact snapshots precede rm while the original Stop token
				// is live; neither can reconstruct lost effects afterward.
				runtime, err = prepareNativeRuntimeCleanup(c, namespace, ack)
				return err
			},
			remove: func(c context.Context) error {
				if !retirement.retired || namespace == nil || runtime == nil || runtime.namespace != namespace {
					return errors.New("native rm lacks original complete retirement/metadata snapshots")
				}
				return owner.RemoveContainerID(c, cid)
			},
			remaining: owner.VerifyRemaining,
			namespaceMetadata: func(c context.Context, ack *nativeContainerRemoveACK) error {
				return namespace.removeAfterACK(c, ack, owner.VerifyRemaining)
			},
			runtimeMetadata: func(c context.Context, ack *nativeContainerRemoveACK) error {
				if err := ack.coversInventory(retirement.full, cid, true); err != nil {
					return err
				}
				if namespace == nil || !namespace.completed || namespace.failure != nil {
					return errors.New("native endpoint withdrawal lacks original namespace removal completion")
				}
				// Leftover re-checks are notices, never conditions (decision #3):
				// endpoint withdrawal and private metadata removal each run and
				// report their own result.
				for _, check := range []func() error{
					func() error { return scope.checkInfrastructure(c, owner.VerifyRemaining) },
					func() error { return retirement.full.Check(c) },
					func() error { return verifyNativeNamespaceCleanupBare(c, scope) },
					func() error { return runtime.checkClearedNamespaces(c) },
				} {
					if err := check(); err != nil {
						nativeRemovalNotice(fmt.Errorf("native removal leftover check: %w", err))
					}
				}
				return errors.Join(owner.WithdrawEndpoints(c), runtime.removeAfterACK(c, ack, owner.VerifyRemaining))
			},
		}
		return completeNativeContainerRemoval(ctx, original, actions)
	})
	// Each step's own error decides (decision #3); only a typed lost-result
	// error is uncertain.
	return err
}
