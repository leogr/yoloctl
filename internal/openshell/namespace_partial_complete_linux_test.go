package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only protected-file metadata and protocol guards are modelled here. The
// positive flags are synthetic test inputs, not actual root/engine/mount ACKs.
// The actual adapter must obtain them from retireKnownUnboundNativePin first.
func knownUnboundCompletionFixture(t *testing.T, bound bool) (string, *namespacePinFiles, *nativeUnboundPinRetirement) {
	t.Helper()
	root, _, _, reservation, state, _ := namespacePinReservationFixture(t)
	mount := uint64(0)
	progress := partialPinProgress{stopAttempted: true, stopAcknowledged: true, ready: true}
	if bound {
		mount = reservation.files.lock.directories[4].identity.mount + 1000
		state.Phase = pinPartialUnmounted
		state.DirectoryMountID = mount
		if err := reservation.files.writeObservation(state); err != nil {
			t.Fatal(err)
		}
		progress.unmountAttempted = true
		progress.unmountAcknowledged = true
	}
	reservation.cleanupAttempted = true
	return root, reservation.files, &nativeUnboundPinRetirement{source: knownUnboundPin{reservation: reservation, directoryMount: mount}, progress: progress}
}

func TestNamespaceKnownUnboundCompletionPublishesBoundIndependentMarkerAndPreservesFiles(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "bare", true: "directory-only"}[bound], func(t *testing.T) {
			root, f, token := knownUnboundCompletionFixture(t, bound)
			if ok, err := f.readRetired(); ok || err != nil {
				t.Fatal("phase alone granted retirement completion", ok, err)
			}
			if err := f.publishRetired(syntheticRetirementACKs()); err == nil {
				t.Fatal("full-pin publisher certified an unbound attempt")
			}
			if err := token.publishKnownUnboundRetired(); err != nil {
				t.Fatal("original known-unbound completion was refused", err)
			}
			cold, err := f.lock.read(f.record, f.cid)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cold.Close() }()
			if ok, err := cold.readRetired(); !ok || err != nil {
				t.Fatal("independent known-unbound marker refused", ok, err)
			}
			binding, err := pinRetirementBinding(cold)
			if err != nil || binding.Schema != 2 {
				t.Fatal("unbound marker mixed with full-policy retirement", binding, err)
			}
			marker := filepath.Join(root, "namespace-pins", f.lock.incarnation, "pins", f.record.Generation, "retired.json")
			first, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			var uncertain *NativeEffectsUncertain
			if err := token.publishKnownUnboundRetired(); !errors.As(err, &uncertain) {
				t.Fatal("completion publication was retried", err)
			}
			after, err := os.ReadFile(marker)
			if err != nil || string(first) != string(after) {
				t.Fatal("independent marker replaced", err)
			}
			// Existing bounded inventory/removal accepts this exact marker kind;
			// actual caller still requires native bare mapping and CID-rm ACK first.
			snapshot, err := inventoryNamespaceBookkeeping(cold)
			if err != nil {
				t.Fatal("new marker rejected by bounded metadata inventory", err)
			}
			if err := snapshot.checkComplete(); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.remove(context.Background(), func(context.Context) error { return nil }); err != nil {
				t.Fatal("bounded metadata removal rejected complete fixture", err)
			}
			if err := f.lock.check(); err != nil {
				t.Fatal("stable lock removed with generation bookkeeping", err)
			}
		})
	}
}

func TestNamespaceKnownUnboundCompletionRequiresOriginalACKsAndNoUncertainSuffix(t *testing.T) {
	for _, change := range []string{"no-token", "no-original", "stop-attempt", "stop-ack", "not-ready", "no-consumed-cleanup", "uncertain", "removal-attempted", "private-unacked", "unmount-unacked", "different-mount", "full-pin", "changed-observation"} {
		t.Run(change, func(t *testing.T) {
			root, f, token := knownUnboundCompletionFixture(t, true)
			switch change {
			case "no-token":
				token = nil
			case "no-original":
				token.source.reservation = nil
			case "stop-attempt":
				token.progress.stopAttempted = false
			case "stop-ack":
				token.progress.stopAcknowledged = false
			case "not-ready":
				token.progress.ready = false
			case "no-consumed-cleanup":
				token.source.reservation.cleanupAttempted = false
			case "uncertain":
				token.failure = &NativeEffectsUncertain{Cause: errors.New("lost reply")}
			case "removal-attempted":
				token.removalAttempted = true
			case "private-unacked":
				token.progress.privateAttempted = true
			case "unmount-unacked":
				token.progress.unmountAcknowledged = false
			case "different-mount":
				token.source.directoryMount++
			case "full-pin":
				var state namespacePinRecord
				if err := json.Unmarshal(f.current, &state); err != nil {
					t.Fatal(err)
				}
				state.Phase = pinDirectoryUnmounted
				state.PinMountID = state.DirectoryMountID + 1
				if err := f.writeObservation(state); err != nil {
					t.Fatal(err)
				}
			case "changed-observation":
				if err := os.WriteFile(filepath.Join(root, "namespace-pins", f.lock.incarnation, "pins", f.record.Generation, "state.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := token.publishKnownUnboundRetired()
			if err == nil {
				t.Fatal("partial completion published without original ACKs", change)
			}
			if change == "uncertain" {
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &uncertain) {
					t.Fatal("partial completion lost original typed uncertainty", err)
				}
			}
			if _, err := os.Lstat(filepath.Join(root, "namespace-pins", f.lock.incarnation, "pins", f.record.Generation, "retired.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused completion published marker", err)
			}
		})
	}
}

func TestNamespaceKnownUnboundCompletedBareMappingDoesNotAdoptColdPartialState(t *testing.T) {
	for _, bound := range []bool{false, true} {
		_, f, _ := knownUnboundCompletionFixture(t, bound)
		var state namespacePinRecord
		if err := json.Unmarshal(f.current, &state); err != nil {
			t.Fatal(err)
		}
		native := f.lock.directories[4].identity.mount
		view := nativePinMountView{directoryMount: native}
		if err := validatePinRetirementStage(view, state, native, false, pinRetirementProgress{}); err == nil {
			t.Fatal("cold uncompleted partial state gained retirement authority")
		}
		if err := validatePinRetirementStage(view, state, native, true, pinRetirementProgress{}); err != nil {
			t.Fatal("validated completed bare mapping refused", err)
		}
		view.directoryBound = true
		if err := validatePinRetirementStage(view, state, native, true, pinRetirementProgress{}); err == nil {
			t.Fatal("completed marker hid a current bound mount")
		}
	}
}

func TestNamespaceKnownUnboundCompletionFailedPublicationIsConsumedWithoutAdoption(t *testing.T) {
	root, f, token := knownUnboundCompletionFixture(t, false)
	marker := filepath.Join(root, "namespace-pins", f.lock.incarnation, "pins", f.record.Generation, "retired.json")
	if err := os.WriteFile(marker, []byte("foreign retained marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	var uncertain *NativeEffectsUncertain
	if err := token.publishKnownUnboundRetired(); !errors.As(err, &uncertain) || !token.completionAttempted {
		t.Fatal("failed completion publication lost consumed uncertainty", err)
	}
	if err := token.publishKnownUnboundRetired(); !errors.As(err, &uncertain) {
		t.Fatal("failed completion publication retried", err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "foreign retained marker" {
		t.Fatal("failed publication adopted/replaced foreign marker", err)
	}
	if ok, err := f.readRetired(); ok || err == nil {
		t.Fatal("foreign marker became cleanup completion", ok, err)
	}
}

func TestNamespaceKnownUnboundCompletionMarkerRefusesCaseAliasesAndNoncanonicalBytes(t *testing.T) {
	_, f, token := knownUnboundCompletionFixture(t, false)
	if err := token.publishKnownUnboundRetired(); err != nil {
		t.Fatal(err)
	}
	binding, err := pinRetirementBinding(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range [][]byte{
		append([]byte(`{"Schema":0,`), raw[1:]...),
		append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"Schema":2}`)...),
		append(append([]byte(nil), raw...), byte(' ')),
	} {
		if err := validateRetiredPinMarker(changed, f); err == nil {
			t.Fatal("ambiguous/noncanonical retirement marker accepted", string(changed))
		}
	}
}

func TestNamespaceKnownUnboundCompletionComposesPostPersistPreBindFailureWithCleanup(t *testing.T) {
	for _, cancelAtPersist := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer-disappears", true: "client-cancels"}[cancelAtPersist], func(t *testing.T) {
			_, _, _, reservation, state, _ := namespacePinReservationFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("peer disappeared before bind")
			checks, mountEffects := 0, 0
			hook := pinDirectoryHooks{
				check: func() error {
					checks++
					if checks == 2 {
						return cause
					}
					return reservation.files.checkUnchanged()
				},
				persist: func(phase namespacePinPhase, mount uint64) error {
					state.Phase, state.DirectoryMountID = phase, mount
					if err := reservation.files.writeObservation(state); err != nil {
						return err
					}
					if cancelAtPersist {
						cancel()
					}
					return nil
				},
				bind: func() error { mountEffects++; return nil }, reopenBound: func() (uint64, error) { mountEffects++; return 77, nil },
				makePrivate: func() error { mountEffects++; return nil }, verifyPrivate: func() error { mountEffects++; return nil },
			}
			// The actual creator consumes its exclusive reservation immediately before
			// invoking this same protocol. It retains the returned attempt on failure.
			reservation.consumed = true
			progress, failed := runPinDirectoryCreate(ctx, hook)
			var uncertain *NativeEffectsUncertain
			if failed == nil || errors.As(failed, &uncertain) || progress != (pinDirectoryProgress{}) || mountEffects != 0 || state.Phase != pinDirectoryBindAttempt || state.DirectoryMountID != 0 {
				t.Fatal("post-persist pre-bind failure did not retain a known zero-effect attempt", progress, failed, state.Phase)
			}
			if cancelAtPersist && !errors.Is(failed, context.Canceled) || !cancelAtPersist && !errors.Is(failed, cause) {
				t.Fatal("wrong original check failure", failed)
			}
			attempt := &nativePinDirectoryAttempt{reservation: reservation, progress: progress, failure: failed}
			original, err := selectKnownUnboundPin(reservation, attempt, nil)
			if err != nil {
				t.Fatal("actual original no-bind attempt was refused", err)
			}
			if _, err := selectKnownUnboundPin(reservation, nil, nil); err == nil {
				t.Fatal("consumed phase minted no-attempt authority without its original attempt")
			}
			f := reservation.files
			bare := nativePinMountView{directoryMount: f.lock.directories[4].identity.mount}
			if err := validatePinRetirementStage(bare, state, bare.directoryMount, false, pinRetirementProgress{}); err == nil {
				t.Fatal("cold pre-bind phase granted retirement authority")
			}
			cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancelCleanup()
			stops, inactive := 0, 0
			unexpected := func() error { mountEffects++; return errors.New("unexpected partial mount effect") }
			partial := partialPinHooks{
				beforeStop: func(context.Context) error { return f.checkUnchanged() },
				stop:       func(context.Context) error { stops++; return nil },
				check: func(c context.Context, _ partialPinProgress) error {
					inactive++
					if err := c.Err(); err != nil {
						return err
					}
					return f.checkUnchanged()
				},
				persist: func(namespacePinPhase) error { return unexpected() }, makePrivate: unexpected, verifyPrivate: func(context.Context) error { return unexpected() }, unmount: unexpected, verifyBare: func(context.Context) error { return unexpected() },
			}
			reservation.cleanupAttempted = true
			retired, err := runPartialPinRetirement(cleanup, false, false, partial)
			if err != nil || !retired.ready || !retired.stopAcknowledged || stops != 1 || inactive != 1 || mountEffects != 0 {
				t.Fatal("known no-bind cleanup did not obtain fresh Stop/inactive without mount replay", retired, err, stops, inactive, mountEffects)
			}
			token := &nativeUnboundPinRetirement{source: original, progress: retired}
			if ok, err := f.readRetired(); ok || err != nil {
				t.Fatal("cleanup phase alone became independent completion", ok, err)
			}
			if err := token.publishKnownUnboundRetired(); err != nil {
				t.Fatal("known post-persist pre-bind cleanup could not complete marker", err)
			}
			if ok, err := f.readRetired(); !ok || err != nil {
				t.Fatal("composed cleanup marker did not verify", ok, err)
			}
			if err := validatePinRetirementStage(bare, state, bare.directoryMount, true, pinRetirementProgress{}); err != nil {
				t.Fatal("completed pre-bind bare mapping refused", err)
			}
			var after namespacePinRecord
			if err := json.Unmarshal(f.current, &after); err != nil || after.Phase != pinDirectoryBindAttempt || after.DirectoryMountID != 0 || after.PinMountID != 0 || mountEffects != 0 {
				t.Fatal("completion invented a bind/terminal mutation", after, err)
			}
		})
	}
}
