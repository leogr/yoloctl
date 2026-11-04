package openshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeActivationCleanupFallbackRequiresNoNamespaceInitialization(t *testing.T) {
	calls := 0
	callback := func(context.Context) error { calls++; return nil }
	owner := NativeActivationCleanup{callback, callback, callback, func(context.Context, string) error { calls++; return nil }, callback}
	for _, activation := range []*NativeActivation{nil, {}, {failure: errors.New("known read-only failure before reservation")}} {
		handled, err := activation.RemoveCreatedContainer(context.Background(), owner)
		if handled || err != nil || calls != 0 {
			t.Fatal("no-namespace path gained engine authority", handled, err, calls)
		}
	}
	cause := errors.New("lost namespace/authorization reply")
	for _, activation := range []*NativeActivation{
		{failure: &NativeEffectsUncertain{Cause: cause}},
		{closed: true},
		{cleanupAttempted: true},
		{lock: &namespacePinLock{}},
	} {
		handled, err := activation.RemoveCreatedContainer(context.Background(), owner)
		var uncertain *NativeEffectsUncertain
		if !handled || !errors.As(err, &uncertain) || calls != 0 {
			t.Fatal("unknown/consumed initialization fell through to ordinary rm", handled, err, calls)
		}
		if activation.failure != nil && !errors.Is(err, cause) {
			t.Fatal("original uncertainty cause lost", err)
		}
	}
	_, _, _, r, _, _ := namespacePinReservationFixture(t)
	activation := &NativeActivation{reservation: r}
	if handled, err := activation.RemoveCreatedContainer(context.Background(), NativeActivationCleanup{}); !handled || err == nil {
		t.Fatal("reserved attempt accepted missing callbacks", handled, err)
	}
}

func TestNativeActivationCloseDropsOnlyHeldReferencesAndPreservesBookkeeping(t *testing.T) {
	root, _, lock, r, _, record := namespacePinReservationFixture(t)
	name := filepath.Join(root, "descriptor-fixture")
	if err := os.WriteFile(name, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	extra, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary descriptor fixture only, not an actual NSFS/mount proof.
	directory := &nativePinDirectoryAttempt{reservation: r}
	namespace := &nativePinAttempt{directory: directory, namespace: extra}
	a := &NativeActivation{lock: lock, reservation: r, directory: directory, namespace: namespace, cid: "not-for-display"}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal("close was not idempotent", err)
	}
	if _, err := extra.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("held descriptor retained", err)
	}
	if r.files.directory != nil || lock.file != nil {
		t.Fatal("metadata/ownership lock descriptor retained")
	}
	if _, err := os.Stat(filepath.Join(root, "namespace-pins", record.Launch.Binding.Incarnation, "pins", record.Generation, "state.json")); err != nil {
		t.Fatal("Close destructively removed metadata", err)
	}
	if got := fmt.Sprintf("%v %#v", a, a); strings.Contains(got, "not-for-display") || !strings.Contains(got, "redacted") {
		t.Fatal("opaque activation formatting exposed state", got)
	}
}

func TestNativeActivationConsumedCreationAndMissingBackendRefuseBeforeEffects(t *testing.T) {
	for _, a := range []*NativeActivation{{closed: true}, {cleanupAttempted: true}, {lock: &namespacePinLock{}}} {
		proof, err := a.createOwnedEgress(context.Background(), nil, nil)
		var uncertain *NativeEffectsUncertain
		if proof.Schema != 0 || !errors.As(err, &uncertain) {
			t.Fatal("consumed attempt lost typed refusal", proof, err)
		}
	}
	if proof, err := (&NativeActivation{}).createOwnedEgress(context.Background(), nil, nil); err == nil || proof.Schema != 0 {
		t.Fatal("missing peer/namespace accepted", proof, err)
	}
	a, err := ActivateNativeOwned(context.Background(), PreparedRuntime{}, nil)
	if a == nil || err == nil {
		t.Fatal("invalid actual root context/preparation accepted", a, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyPreparedNativeRecord(ctx, PreparedRuntime{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled verifier acquired authority", err)
	}
}

func TestNativePreparedRecordVerifierUsesRealComponentReadersAndRejectsReplacement(t *testing.T) {
	f, _ := generationFixture(t)
	if err := verifyPreparedNativeRecordAt(f.root, f.record, f.owners); err != nil {
		t.Fatal("real component-wise record reader refused original", err)
	}
	for _, change := range []string{"generation-link", "record-link", "record-copy", "current-record", "origin"} {
		t.Run(change, func(t *testing.T) {
			f, _ := generationFixture(t)
			generation := filepath.Join(f.root.Name(), "generations", f.record.Generation)
			record := filepath.Join(generation, "record.json")
			switch change {
			case "generation-link":
				if err := os.Rename(generation, generation+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(generation+"-retained", generation); err != nil {
					t.Fatal(err)
				}
			case "record-link":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../../current.json", record); err != nil {
					t.Fatal(err)
				}
			case "record-copy":
				if err := os.WriteFile(record, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "current-record":
				if err := os.WriteFile(filepath.Join(f.root.Name(), "current.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "origin":
				if err := os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyPreparedNativeRecordAt(f.root, f.record, f.owners); err == nil {
				t.Fatal("replaced prepared record accepted", change)
			}
		})
	}
}
