package openshell

import (
	"encoding/json"
	"errors"
)

// A terminal shape alone grants nothing. It selects schema2 only when an
// independently protected completion marker exists, or for the original
// known-unbound publisher below after its actual positive cleanup ACKs.
func knownUnboundTerminalObservation(state namespacePinRecord) bool {
	if state.PinMountID != 0 || state.ReadbackSHA256 != "" {
		return false
	}
	// The original protocol can persist its bind attempt, then fail a pure
	// check before setting bindAttempted. Only the original successful cleanup
	// token may publish this state; a cold unmarked attempt still grants none.
	return (state.Phase == pinPlanned || state.Phase == pinDirectoryBindAttempt) && state.DirectoryMountID == 0 || state.Phase == pinPartialUnmounted && state.DirectoryMountID != 0
}

// publishKnownUnboundRetired preserves the CID while making this original
// in-call cleanup independently verifiable by later teardown. It cannot be
// called successfully from a phase, a failed/lost mutation or a cold record.
// The actual adapter repeats inactive/bare native mapping proof before entry.
func (p *nativeUnboundPinRetirement) publishKnownUnboundRetired() error {
	if p == nil || p.source.reservation == nil || p.source.reservation.files == nil {
		return errors.New("known-unbound completion lacks its original retirement token")
	}
	if p.completionAttempted {
		return &NativeEffectsUncertain{Cause: errors.New("known-unbound completion was already attempted; no retry")}
	}
	r := p.source.reservation
	if p.failure != nil {
		return p.failure
	}
	if p.removalAttempted || !r.cleanupAttempted || !p.progress.stopAttempted || !p.progress.stopAcknowledged || !p.progress.ready || p.progress.privateAttempted != p.progress.privateAcknowledged || p.progress.unmountAttempted != p.progress.unmountAcknowledged {
		return errors.New("known-unbound completion lacks complete original cleanup acknowledgements")
	}
	f := r.files
	if err := f.checkUnchanged(); err != nil {
		return err
	}
	var state namespacePinRecord
	if err := json.Unmarshal(f.current, &state); err != nil {
		return err
	}
	if !knownUnboundTerminalObservation(state) || state.DirectoryMountID != p.source.directoryMount || p.source.directoryMount == 0 && (p.progress.privateAttempted || p.progress.unmountAttempted) || p.source.directoryMount != 0 && !p.progress.unmountAcknowledged {
		return errors.New("known-unbound completion differs from the original no-namespace retirement")
	}
	binding, err := pinRetirementBinding(f)
	if err != nil || binding.Schema != 2 {
		return errors.Join(err, errors.New("known-unbound completion has a mixed retirement marker"))
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	p.completionAttempted = true // An uncertain publication can never be retried.
	if err := writePinFileAt(int(f.directory.Fd()), "retired.json", raw, false, f.lock.uid, f.lock.gid); err != nil {
		return &NativeEffectsUncertain{Cause: err}
	}
	return nil
}
