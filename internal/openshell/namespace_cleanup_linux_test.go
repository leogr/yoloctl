package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Real ordinary owned files and the actual original-call ACK protocol, with
// declared synthetic kernel/engine/remaining-consumer predicates only. This
// fixture cannot call the admitted native constructor/VM or claim root proof.
func namespaceCleanupFixture(t *testing.T) (string, *namespaceGenerationInventory, *nativeNamespaceCleanup, nativeContainerRemovalScope) {
	t.Helper()
	root, lock, records, cid := namespaceInventoryFixture(t)
	for _, record := range records[:2] {
		f, err := lock.read(record, cid)
		if err != nil {
			t.Fatal(err)
		}
		var state namespacePinRecord
		if err := json.Unmarshal(f.current, &state); err != nil {
			t.Fatal(err)
		}
		state.Phase, state.DirectoryMountID, state.PinMountID = pinDirectoryUnmounted, 122, 123
		if err := f.writeObservation(state); err != nil {
			t.Fatal(err)
		}
		if err := f.publishRetired(syntheticRetirementACKs()); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err := snapshotNamespaceGenerations(t.Context(), lock, records, cid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	observed, err := inventoryNamespaceCleanup(t.Context(), lock, records)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareCleanupToComplete(observed, s); err != nil {
		t.Fatal("complete cleanup fixture omitted original members", err)
	}
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	// Metadata-only descriptor/records for private protocol scope tests, never
	// a NativeReservation or independently qualified full native inventory.
	full := &NativeContainerInventory{root: file, current: cloneRuntimeRecord(records[len(records)-1]), records: records}
	r := &nativeContainerRetirement{full: full, retired: true}
	p := &nativeNamespaceCleanup{retirement: r, lock: lock, records: full.Records(), cid: cid, expected: observed, flexibleSize: map[string]bool{}}
	byGeneration := map[string]*namespacePinFiles{}
	for _, f := range s.pins {
		byGeneration[f.record.Generation] = f
	}
	var entries []*nativeContainerRetirementEntry
	for _, record := range records {
		entries = append(entries, &nativeContainerRetirementEntry{record: cloneRuntimeRecord(record), pin: byGeneration[record.Generation]})
	}
	if err := populateNamespaceCleanupSteps(p, entries, s.noInitialization); err != nil {
		t.Fatal(err)
	}
	return root, s, p, nativeContainerRemovalScope{cid: cid, currentGeneration: records[len(records)-1].Generation, records: records}
}

func TestNamespaceCleanupOriginalRemoveACKDeletesOnlyOrderedOwnedMetadata(t *testing.T) {
	root, s, p, scope := namespaceCleanupFixture(t)
	inc := filepath.Join(root, "namespace-pins", s.lock.incarnation)
	before, err := namespaceInventoryMemberAt(int(s.lock.directories[2].file.Fd()), ".lock")
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "namespace-pins", strings.Repeat("f", 32))
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "retained"), []byte("other incarnation"), 0600); err != nil {
		t.Fatal(err)
	}
	current := func(context.Context) error {
		anyPin := false
		for _, record := range scope.records {
			if _, err := os.Lstat(filepath.Join(inc, "pins", record.Generation)); err == nil {
				anyPin = true
			}
		}
		if anyPin {
			for _, f := range s.pins {
				if _, err := os.Lstat(filepath.Join(inc, "origins", f.record.Generation+".json")); err != nil {
					t.Fatal("external origin removed before all generation data", err)
				}
			}
			for _, record := range scope.records {
				for _, f := range s.noInitialization {
					if recordIdentity(record) == f.marker.RecordSHA256 {
						if _, err := os.Lstat(filepath.Join(inc, noInitializationDirectory, record.Generation+".json")); err != nil {
							t.Fatal("no-init anchor removed before all generation data", err)
						}
					}
				}
			}
		}
		return nil
	}
	a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
		return p.removeWithCurrentVerifier(ctx, ack, current)
	}
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal("original remove ACK did not complete owned namespace cleanup", err)
	}
	if !p.completed || *removes != 1 {
		t.Fatal("namespace cleanup lost original removal/completion", p.completed, *removes)
	}
	if err := p.checkExpected(t.Context()); err != nil {
		t.Fatal("completed namespace cleanup left unexpected metadata", err)
	}
	for _, parent := range []string{"origins", "pins", noInitializationDirectory} {
		names, err := os.ReadDir(filepath.Join(inc, parent))
		if err != nil || len(names) != 0 {
			t.Fatal("owned generation metadata remains", parent, err, len(names))
		}
	}
	after, err := namespaceInventoryMemberAt(int(s.lock.directories[2].file.Fd()), ".lock")
	if err != nil || before != after {
		t.Fatal("namespace cleanup replaced stable lock", err)
	}
	if raw, err := os.ReadFile(filepath.Join(sibling, "retained")); err != nil || string(raw) != "other incarnation" {
		t.Fatal("namespace cleanup touched another incarnation", err)
	}
}

func TestNamespaceCleanupRefusesMissingForeignOrIncompleteACKBeforeFiles(t *testing.T) {
	for _, change := range []string{"nil-ACK", "foreign-CID", "subset-records", "duplicate-records", "not-retired"} {
		t.Run(change, func(t *testing.T) {
			_, s, p, scope := namespaceCleanupFixture(t)
			verified := 0
			a, _ := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				switch change {
				case "nil-ACK":
					ack = nil
				case "foreign-CID":
					p.cid = strings.Repeat("b", 64)
				case "subset-records":
					p.records = p.records[:1]
				case "duplicate-records":
					p.records[1] = p.records[0]
				case "not-retired":
					p.retirement.retired = false
				}
				return p.removeWithCurrentVerifier(ctx, ack, func(context.Context) error { verified++; return nil })
			}
			// After the rm ACK a refused namespace cleanup is a named warning
			// (decision #3); it still deletes nothing.
			notices := captureNativeRemovalNotices(t)
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			if err != nil || len(*notices) == 0 || verified != 0 || p.attempted || p.completed {
				t.Fatal("unscoped remove token reached namespace deletion", change, err, verified)
			}
			if err := s.check(t.Context()); err != nil {
				t.Fatal("unscoped remove token changed original files", err)
			}
		})
	}
}

func TestNamespaceCleanupLostSyncStopsWithoutAdvancingOrRetrying(t *testing.T) {
	root, s, p, scope := namespaceCleanupFixture(t)
	first := p.steps[0]
	fd, err := unix.Openat(int(first.parent.Fd()), ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	lookup := os.NewFile(uintptr(fd), "cleanup-sync-failure-fixture")
	defer func() { _ = lookup.Close() }()
	p.steps[0].parent = lookup
	verified := 0
	a, _ := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
		current := func(context.Context) error { verified++; return nil }
		err := p.removeWithCurrentVerifier(ctx, ack, current)
		before := verified
		if retry := p.removeWithCurrentVerifier(ctx, ack, current); retry == nil || before != verified {
			t.Fatal("uncertain namespace unlink/sync was retried", retry, verified)
		}
		return err
	}
	err = completeNativeContainerRemoval(t.Context(), scope, a)
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || !errors.Is(err, unix.EBADF) || !p.attempted || p.completed {
		t.Fatal("failed namespace sync became successful cleanup", err)
	}
	if _, exists := p.expected.members[first.key]; !exists {
		t.Fatal("failed unlink durability advanced expected member set")
	}
	inc := filepath.Join(root, "namespace-pins", s.lock.incarnation)
	if _, err := os.Lstat(filepath.Join(inc, first.key)); !os.IsNotExist(err) {
		t.Fatal("fixture did not reach post-unlink sync failure", err)
	}
	for _, f := range s.pins {
		if _, err := os.Lstat(filepath.Join(inc, "origins", f.record.Generation+".json")); err != nil {
			t.Fatal("sync failure removed immutable recovery origin", err)
		}
	}
}

func TestNamespaceCleanupChangedInventoryOrParentStopsBeforeUnlink(t *testing.T) {
	for _, change := range []string{"remaining-proof", "foreign-parent", "same-bytes-origin-inode", "unknown-incarnation-member", "unknown-pin-member", "changed-state-bytes"} {
		t.Run(change, func(t *testing.T) {
			root, s, p, scope := namespaceCleanupFixture(t)
			inc := filepath.Join(root, "namespace-pins", s.lock.incarnation)
			first := p.steps[0]
			cause := errors.New("remaining consumer/current storage unavailable")
			current := func(context.Context) error {
				if change == "remaining-proof" {
					return cause
				}
				return nil
			}
			a, _ := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			foreign := ""
			a.remaining = func(context.Context) error {
				switch change {
				case "foreign-parent":
					foreign = t.TempDir()
					// TempDir does not promise0700. Match every role/shape field so
					// the intended identity guard, not a permission mismatch, refuses.
					if err := os.Chown(foreign, p.lock.uid, p.lock.gid); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(foreign, 0700); err != nil {
						t.Fatal(err)
					}
					target := filepath.Join(foreign, first.name)
					if err := os.WriteFile(target, nil, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chown(target, p.lock.uid, p.lock.gid); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(target, 0600); err != nil {
						t.Fatal(err)
					}
					f, err := os.Open(foreign)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = f.Close() })
					foreignIdentity, err := pinIdentity(int(f.Fd()), p.lock.uid, p.lock.gid, true)
					if err != nil {
						t.Fatal("foreign-parent fixture did not reach valid directory role", err)
					}
					originalIdentity, err := pinIdentity(int(first.parent.Fd()), p.lock.uid, p.lock.gid, true)
					if err != nil {
						t.Fatal(err)
					}
					if foreignIdentity == originalIdentity {
						t.Fatal("foreign-parent fixture aliases original directory")
					}
					if _, _, err := readPinFileAt(int(f.Fd()), first.name, p.lock.uid, p.lock.gid, 0); err != nil {
						t.Fatal("foreign-parent fixture did not reach valid empty regular leaf role", err)
					}
					p.steps[0].parent = f
				case "same-bytes-origin-inode":
					name := filepath.Join(inc, "origins", s.pins[0].record.Generation+".json")
					raw, err := os.ReadFile(name)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(name, filepath.Join(t.TempDir(), "retained-origin")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, raw, 0600); err != nil {
						t.Fatal(err)
					}
				case "unknown-incarnation-member":
					if err := os.WriteFile(filepath.Join(inc, "unexpected"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "unknown-pin-member":
					if err := os.WriteFile(filepath.Join(inc, "pins", s.pins[0].record.Generation, "unexpected"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "changed-state-bytes":
					name := filepath.Join(inc, "pins", s.pins[0].record.Generation, "state.json")
					raw, err := os.ReadFile(name)
					if err != nil {
						t.Fatal(err)
					}
					raw[0] = '['
					if err := os.WriteFile(name, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				return p.removeWithCurrentVerifier(ctx, ack, current)
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			var uncertain *NativeEffectsUncertain
			if !errors.As(err, &uncertain) || p.completed {
				t.Fatal("unproved metadata/current predicate allowed namespace cleanup", change, err)
			}
			if _, err := os.Lstat(filepath.Join(inc, first.key)); err != nil {
				t.Fatal("refused metadata cleanup unlinked original first member", change, err)
			}
			if foreign != "" {
				if _, err := os.Lstat(filepath.Join(foreign, first.name)); err != nil {
					t.Fatal("foreign parent descriptor was used for unlink", err)
				}
			}
		})
	}
}

func TestNamespaceCleanupExpiredACKCannotReplayObservedFiles(t *testing.T) {
	_, s, p, scope := namespaceCleanupFixture(t)
	var saved *nativeContainerRemoveACK
	a, _ := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.namespaceMetadata = func(_ context.Context, ack *nativeContainerRemoveACK) error { saved = ack; return nil }
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal(err)
	}
	verified := 0
	if err := p.removeWithCurrentVerifier(t.Context(), saved, func(context.Context) error { verified++; return nil }); err == nil || verified != 0 || p.attempted {
		t.Fatal("expired remove token reached namespace files", err, verified)
	}
	if err := s.check(t.Context()); err != nil {
		t.Fatal("expired token changed original namespace metadata", err)
	}
}

func TestNamespaceCleanupActualBoundaryRequiresOriginalNativeScope(t *testing.T) {
	if p, err := prepareNativeNamespaceCleanup(t.Context(), nil, nil); err == nil || p != nil {
		t.Fatal("missing native retirement prepared deletion", p, err)
	}
	if verifyNativeNamespaceCleanupBare(t.Context(), nil) == nil || verifyNativeNamespaceCleanupBare(t.Context(), &nativeRemovalReadScope{}) == nil {
		t.Fatal("missing actual kernel/root produced bare namespace proof")
	}
	if _, err := inventoryNamespaceCleanup(t.Context(), nil, nil); err == nil {
		t.Fatal("missing original lock/records became deletion inventory")
	}
	var p *nativeNamespaceCleanup
	if p.removeAfterACK(t.Context(), nil, nil) == nil || p.removeWithCurrentVerifier(t.Context(), nil, nil) == nil {
		t.Fatal("nil deletion handle accepted")
	}
	_, s, p, _ := namespaceCleanupFixture(t)
	verified := 0
	if err := p.removeAfterACK(t.Context(), nil, func(context.Context) error { verified++; return nil }); err == nil || verified != 0 || p.attempted {
		t.Fatal("ordinary owned-file fixture bypassed actual native scope", err, verified)
	}
	if err := s.check(t.Context()); err != nil {
		t.Fatal("refused native boundary changed owned fixture", err)
	}
}
