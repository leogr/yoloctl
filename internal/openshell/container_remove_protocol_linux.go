package openshell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Private original-call protocol only. The future admitted adapter must build
// scope from COMPLETE qualified runtime + namespace inventories and independently
// prove storage/fixed CID/process/kernel ownership. No public caller or engine
// callback construction is supplied here; decoded metadata cannot mint ACKs.
type nativeContainerRemovalScope struct {
	cid, currentGeneration string
	records                []RuntimeRecord
}
type nativeContainerRemovalActions struct {
	qualify, stop, inactive func(context.Context) error
	// Status/socket observations can legitimately change during acknowledged
	// shutdown. Refresh must retain immutable ownership anchors; it is not a
	// blanket snapshot replacement or proof of Stop.
	refresh                            func(context.Context, *nativeContainerStopACK) error
	retire                             func(context.Context, *nativeContainerStopACK) error
	remove, remaining                  func(context.Context) error
	namespaceMetadata, runtimeMetadata func(context.Context, *nativeContainerRemoveACK) error
}
type nativeContainerRemovalAttempt struct {
	cid, currentGeneration                                             string
	records                                                            map[string]string
	active, stopAcknowledged, inactiveVerified, namespacesRetired      bool
	removeAttempted, removeAcknowledged, namespaceMetadataAcknowledged bool
}
type nativeContainerStopACK struct {
	attempt *nativeContainerRemovalAttempt
}
type nativeContainerRemoveACK struct {
	attempt *nativeContainerRemovalAttempt
}

func newNativeContainerRemovalAttempt(scope nativeContainerRemovalScope) (*nativeContainerRemovalAttempt, error) {
	if !lowerHex(scope.cid, 32) || !reservationGeneration(scope.currentGeneration) || len(scope.records) == 0 || len(scope.records) > 1024 {
		return nil, errors.New("native removal lacks bounded original records/current generation/full CID")
	}
	a := &nativeContainerRemovalAttempt{cid: scope.cid, currentGeneration: scope.currentGeneration, records: map[string]string{}, active: true}
	first := scope.records[0].Launch
	origin := generationOrigin(scope.records[0])
	for _, record := range scope.records {
		if err := record.Launch.Validate(); err != nil {
			return nil, err
		}
		if !reservationGeneration(record.Generation) || generationOrigin(record) != origin || record.Launch.HelperLaunch() != first.HelperLaunch() || record.Launch.UID != first.UID || record.Launch.GID != first.GID || record.Launch.WorkspaceReadOnly != first.WorkspaceReadOnly || record.Launch.UnconfinedNetwork != first.UnconfinedNetwork {
			return nil, errors.New("native removal records have incompatible original scope")
		}
		if _, duplicate := a.records[record.Generation]; duplicate {
			return nil, errors.New("native removal repeats an immutable generation")
		}
		a.records[record.Generation] = recordIdentity(record)
	}
	if _, found := a.records[scope.currentGeneration]; !found {
		return nil, errors.New("native removal omits the actual selected generation")
	}
	return a, nil
}
func (a *nativeContainerRemovalAttempt) covers(record RuntimeRecord, cid string) error {
	if a == nil || !a.active || a.cid != cid || a.records[record.Generation] != recordIdentity(record) {
		return errors.New("native cleanup ACK does not cover this original record/CID/attempt")
	}
	return nil
}
func (a *nativeContainerStopACK) covers(record RuntimeRecord, cid string) error {
	if a == nil || a.attempt == nil {
		return errors.New("missing original native Stop ACK")
	}
	attempt := a.attempt
	if err := attempt.covers(record, cid); err != nil {
		return err
	}
	if !attempt.stopAcknowledged || !attempt.inactiveVerified || attempt.removeAttempted {
		return errors.New("native Stop ACK is unavailable for retirement at this stage")
	}
	return nil
}
func (a *nativeContainerRemoveACK) permitsMetadata(record RuntimeRecord, cid string, runtime bool) error {
	if a == nil || a.attempt == nil {
		return errors.New("missing original native CID-removal ACK")
	}
	attempt := a.attempt
	if err := attempt.covers(record, cid); err != nil {
		return err
	}
	if !attempt.stopAcknowledged || !attempt.inactiveVerified || !attempt.namespacesRetired || !attempt.removeAcknowledged || runtime && !attempt.namespaceMetadataAcknowledged {
		return errors.New("native metadata removal lacks the ordered original acknowledgements")
	}
	return nil
}

// Complete original record/CID coverage, separately from current storage or
// remaining-consumer observation. Metadata cannot manufacture this token.
func (a *nativeContainerRemoveACK) coversInventory(i *NativeContainerInventory, cid string, runtime bool) error {
	if a == nil || a.attempt == nil || i == nil || i.root == nil || len(i.records) != len(a.attempt.records) || i.current.Generation != a.attempt.currentGeneration {
		return errors.New("native Remove ACK does not cover complete original inventory")
	}
	for _, record := range i.records {
		if err := a.permitsMetadata(record, cid, runtime); err != nil {
			return err
		}
	}
	return nil
}

// Exactly one fresh Stop and one nonforced full-CID Remove belong to this
// invocation; no callback is retried. As on the old path, each step's own
// result decides: a failure up to the rm ACK stops the removal with that plain
// error, and after the rm ACK each cleanup step that fails is a warning naming
// it while the remaining cleanup continues. A typed lost-result error
// (NativeEffectsUncertain) from any step still propagates unchanged.
// Forwards/holders are the enclosing caller's separately acknowledged prefix.
func completeNativeContainerRemoval(ctx context.Context, scope nativeContainerRemovalScope, a nativeContainerRemovalActions) error {
	if a.qualify == nil || a.stop == nil || a.inactive == nil || a.refresh == nil || a.retire == nil || a.remove == nil || a.remaining == nil || a.namespaceMetadata == nil || a.runtimeMetadata == nil {
		return errors.New("native container removal requires complete original-call wiring")
	}
	attempt, err := newNativeContainerRemovalAttempt(scope)
	if err != nil {
		return err
	}
	defer func() { attempt.active = false }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.qualify(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.stop(ctx); err != nil {
		return fmt.Errorf("native removal Stop failed; no retry: %w", err)
	}
	attempt.stopAcknowledged = true
	stopACK := &nativeContainerStopACK{attempt}
	// Continue only this original owned cleanup after a positive Stop ACK,
	// retaining admission values with a finite bound after client cancellation.
	// Each step is one wait with its own fail-safe; nothing bounds the
	// sequence as a whole. A step that its fail-safe cut off reports that
	// context's error with the step's own, read after the call, because a
	// killed engine process returns its exit error, not a context error.
	cleanup := context.WithoutCancel(ctx)
	step := func(stage string, run func(context.Context) error) error {
		check, cancel := context.WithTimeout(cleanup, runtimeFailSafe)
		defer cancel()
		if err := run(check); err != nil {
			if cut := check.Err(); cut != nil {
				err = errors.Join(err, cut)
			}
			return fmt.Errorf("native removal %s: %w", stage, err)
		}
		return nil
	}
	// notice reports a failed cleanup step after the rm ACK; only a typed
	// lost-result error is returned instead.
	notice := func(err error) error {
		if err == nil {
			return nil
		}
		var uncertain *NativeEffectsUncertain
		if errors.As(err, &uncertain) {
			return err
		}
		nativeRemovalNotice(err)
		return nil
	}
	// The inactivity and retirement callbacks contain multiple independently
	// bounded requests. Pass the detached parent through to those waits.
	if err := a.inactive(cleanup); err != nil {
		return fmt.Errorf("native removal inactivity: %w", err)
	}
	attempt.inactiveVerified = true
	if err := step("immutable ownership/status refresh", func(check context.Context) error { return a.refresh(check, stopACK) }); err != nil {
		return err
	}
	if err := a.retire(cleanup, stopACK); err != nil {
		return fmt.Errorf("native removal all-generation namespace retirement: %w", err)
	}
	attempt.namespacesRetired = true
	attempt.removeAttempted = true // retirement ACK can no longer be reused
	if err := step("CID removal", a.remove); err != nil {
		// A remove whose own fail-safe cut it off may have happened: its
		// reply is lost, so the outcome is uncertain, and it is never retried.
		if errors.Is(err, context.DeadlineExceeded) {
			return &NativeEffectsUncertain{Cause: err}
		}
		return err
	}
	attempt.removeAcknowledged = true
	if err := notice(step("remaining consumers", a.remaining)); err != nil {
		return err
	}
	removeACK := &nativeContainerRemoveACK{attempt}
	// Each metadata verifier has its own storage wait. Keep the original
	// remove ACK and the detached parent throughout the cleanup loops.
	if err := a.namespaceMetadata(cleanup, removeACK); err != nil {
		// Private runtime metadata removal depends on this step's ACK.
		return notice(errors.Join(fmt.Errorf("native removal namespace metadata removal: %w", err), errors.New("native removal kept private runtime metadata")))
	}
	attempt.namespaceMetadataAcknowledged = true
	if err := a.runtimeMetadata(cleanup, removeACK); err != nil {
		return notice(fmt.Errorf("native removal private runtime metadata removal: %w", err))
	}
	return nil
}

// nativeRemovalNotice names cleanup left behind after the container was
// removed; a later create of the same box name gets fresh runtime state.
var nativeRemovalNotice = func(err error) {
	slog.Warn(fmt.Sprintf("%v", err), slog.String("actor", "box"), slog.String("executor", "openshell"))
}
