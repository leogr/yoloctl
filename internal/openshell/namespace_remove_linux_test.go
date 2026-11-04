package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNamespaceContainerRemovalNeverSubstitutesInspectionForLostCIDReply(t *testing.T) {
	want := []string{"inventory", "rm-cid", "remaining-owner", "metadata"}
	for index, where := range want {
		var events []string
		cause := errors.New("no such container: lost reply at " + where)
		step := func(name string) error {
			events = append(events, name)
			if name == where {
				return cause
			}
			return nil
		}
		h := namespaceRemovalHooks{beforeRemove: func(context.Context) error { return step("inventory") }, removeCID: func(context.Context) error { return step("rm-cid") }, remaining: func(context.Context) error { return step("remaining-owner") }, removeMetadata: func(context.Context) error { return step("metadata") }}
		p, err := runNamespaceContainerRemoval(context.Background(), h)
		var uncertain *NativeEffectsUncertain
		if !errors.Is(err, cause) || !errors.As(err, &uncertain) || p.completed || !reflect.DeepEqual(events, want[:index+1]) || p.removeAttempted != (index >= 1) || p.removeAcknowledged != (index > 1) || p.metadataAttempted != (index >= 3) || p.metadataAcknowledged {
			t.Fatal("lost reply continued/completed/deleted metadata", p, err, events)
		}
	}
}

func TestNamespaceContainerRemovalPreservesExistingCleanupDeadline(t *testing.T) {
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	checks := 0
	check := func(c context.Context) error {
		checks++
		got, ok := c.Deadline()
		if !ok || got.After(deadline) || c.Err() != nil {
			return errors.New("renewed/ignored original cleanup budget")
		}
		return nil
	}
	h := namespaceRemovalHooks{check, check, check, check}
	if p, err := runNamespaceContainerRemoval(ctx, h); err != nil || !p.completed || !p.removeAcknowledged || !p.metadataAcknowledged || checks != 4 {
		t.Fatal(p, err, checks)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	checks = 0
	if p, err := runNamespaceContainerRemoval(ctx, h); !errors.Is(err, context.Canceled) || p.removeAttempted || checks != 0 {
		t.Fatal("expired owned suffix mutated", p, err, checks)
	}
	if _, err := runNamespaceContainerRemoval(context.Background(), namespaceRemovalHooks{}); err == nil {
		t.Fatal("incomplete removal callbacks accepted")
	}
}

func TestNamespaceBookkeepingInventoryAndDeletionKeepStableLockAndSiblings(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		root, _, lock, files, pin, record := namespacePinFilesFixture(t)
		if terminal {
			pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinDirectoryUnmounted, 122, 123
			if err := files.writeObservation(pin); err != nil {
				t.Fatal(err)
			}
			if err := files.publishRetired(syntheticRetirementACKs()); err != nil {
				t.Fatal(err)
			}
		}
		inc := filepath.Join(root, "namespace-pins", lock.incarnation)
		generation := filepath.Join(inc, "pins", record.Generation)
		origin := filepath.Join(inc, "origins", record.Generation+".json")
		keep := filepath.Join(inc, "origins", "unrelated.json")
		key := filepath.Join(root, "retained-runtime.key")
		for _, name := range []string{keep, key} {
			if err := os.WriteFile(name, []byte("ordinary fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		snapshot, err := inventoryNamespaceBookkeeping(files)
		if err != nil {
			t.Fatal(err)
		}
		lockIdentity := lock.identity
		removedCID := false
		// Ordinary filesystem/project protocol only: no actual container,
		// root mount mapping or engine acknowledgement is claimed by fixtures.
		h := namespaceRemovalHooks{
			beforeRemove: func(context.Context) error { return snapshot.checkComplete() },
			removeCID:    func(context.Context) error { removedCID = true; return nil },
			remaining: func(context.Context) error {
				if !removedCID {
					return errors.New("rm reply not received")
				}
				return nil
			},
			removeMetadata: func(c context.Context) error {
				return snapshot.remove(c, func(context.Context) error {
					if !removedCID {
						return errors.New("metadata before rm reply")
					}
					_, err := os.Stat(key)
					return err
				})
			},
		}
		if p, err := runNamespaceContainerRemoval(context.Background(), h); err != nil || !p.completed {
			t.Fatal("bounded ordinary-file deletion", p, err)
		}
		for _, name := range []string{generation, origin} {
			if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned metadata retained", name, err)
			}
		}
		for _, name := range []string{keep, key} {
			if _, err := os.Stat(name); err != nil {
				t.Fatal("unrelated/runtime key deleted", name, err)
			}
		}
		if err := lock.check(); err != nil || lock.identity != lockIdentity {
			t.Fatal("stable incarnation lock removed/replaced", err)
		}
	}
}

func TestNamespaceBookkeepingInventoryRefusesForeignLeavesBeforeCIDRemoval(t *testing.T) {
	for _, change := range []string{"unknown", "missing", "state-link", "state-hardlink", "state-mode", "state-data", "namespace-data", "namespace-link", "namespace-hardlink", "origin-data", "marker-invalid", "directory-mode"} {
		t.Run(change, func(t *testing.T) {
			root, _, lock, files, _, record := namespacePinFilesFixture(t)
			inc := filepath.Join(root, "namespace-pins", lock.incarnation)
			dir := filepath.Join(inc, "pins", record.Generation)
			state, leaf := filepath.Join(dir, "state.json"), filepath.Join(dir, "namespace")
			switch change {
			case "unknown":
				if err := os.WriteFile(filepath.Join(dir, "foreign"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
			case "state-link":
				if err := os.Remove(state); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("namespace", state); err != nil {
					t.Fatal(err)
				}
			case "state-hardlink":
				if err := os.Link(state, filepath.Join(root, "state-alias")); err != nil {
					t.Fatal(err)
				}
			case "state-mode":
				if err := os.Chmod(state, 0o644); err != nil {
					t.Fatal(err)
				}
			case "state-data":
				if err := os.WriteFile(state, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "namespace-data":
				if err := os.WriteFile(leaf, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "namespace-link":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("state.json", leaf); err != nil {
					t.Fatal(err)
				}
			case "namespace-hardlink":
				if err := os.Link(leaf, filepath.Join(root, "namespace-alias")); err != nil {
					t.Fatal(err)
				}
			case "origin-data":
				if err := os.WriteFile(filepath.Join(inc, "origins", record.Generation+".json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "marker-invalid":
				if err := os.WriteFile(filepath.Join(dir, "retired.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			removes := 0
			h := namespaceRemovalHooks{beforeRemove: func(context.Context) error { _, err := inventoryNamespaceBookkeeping(files); return err }, removeCID: func(context.Context) error { removes++; return nil }, remaining: func(context.Context) error { return nil }, removeMetadata: func(context.Context) error { t.Fatal("foreign metadata reached deletion"); return nil }}
			if p, err := runNamespaceContainerRemoval(context.Background(), h); err == nil || p.removeAttempted || removes != 0 {
				t.Fatal("foreign inventory removed CID", p, err, removes)
			}
			if _, err := os.Lstat(state); err != nil {
				t.Fatal("refusal removed state", err)
			}
		})
	}
}

func TestNamespaceBookkeepingRechecksIdentityAndStopsAfterPartialFailure(t *testing.T) {
	for _, changed := range []bool{false, true} {
		root, _, lock, files, _, record := namespacePinFilesFixture(t)
		snapshot, err := inventoryNamespaceBookkeeping(files)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "namespace-pins", lock.incarnation, "pins", record.Generation)
		state, leaf := filepath.Join(dir, "state.json"), filepath.Join(dir, "namespace")
		cause := errors.New("remaining runtime storage became unavailable")
		checks := 0
		h := namespaceRemovalHooks{
			beforeRemove: func(context.Context) error { return snapshot.checkComplete() },
			removeCID: func(context.Context) error {
				if changed {
					return os.WriteFile(state, []byte(`{}`), 0o600)
				}
				return nil
			},
			remaining: func(context.Context) error { return snapshot.checkComplete() },
			removeMetadata: func(c context.Context) error {
				return snapshot.remove(c, func(context.Context) error {
					checks++
					if checks == 2 {
						return cause
					}
					return nil
				})
			},
		}
		p, err := runNamespaceContainerRemoval(context.Background(), h)
		var uncertain *NativeEffectsUncertain
		if !errors.As(err, &uncertain) || !p.removeAcknowledged || p.completed || p.metadataAcknowledged {
			t.Fatal("changed/partially removed metadata reported success", p, err)
		}
		if changed {
			if p.metadataAttempted || checks != 0 {
				t.Fatal("changed inventory entered deletion", p, checks)
			}
			if _, err := os.Stat(leaf); err != nil {
				t.Fatal("changed inventory removed placeholder", err)
			}
		} else {
			if !p.metadataAttempted || !errors.Is(err, cause) || checks != 2 {
				t.Fatal("failed remaining-owner check continued", p, err, checks)
			}
			if _, err := os.Stat(leaf); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("expected only first acknowledged unlink", err)
			}
		}
		if _, err := os.Stat(state); err != nil {
			t.Fatal("failure deleted later state", err)
		}
		if _, err := os.Stat(filepath.Join(root, "namespace-pins", lock.incarnation, "origins", record.Generation+".json")); err != nil {
			t.Fatal("failure removed external origin", err)
		}
	}
}

func TestNamespaceContainerRemovalActualEntryPreservesUncertaintyAndRequiresKernel(t *testing.T) {
	removes := 0
	owner := namespaceContainerRemoveOwner{stop: nativePinStopHooks{owner: func(context.Context) error { return nil }, stop: func(context.Context) error { t.Fatal("missing infrastructure reached Stop"); return nil }, inactive: func(context.Context) error { return nil }}, removeCID: func(context.Context, string) error { removes++; return nil }, remaining: func(context.Context) error { return nil }}
	if p, err := removeNativeNamespaceContainer(context.Background(), nil, nil, RuntimeRecord{}, "", owner, nil); err == nil || p.removeAttempted || removes != 0 {
		t.Fatal("missing kernel reached removal", p, err)
	}
	cause := errors.New("lost partial cleanup reply")
	partial := &nativeUnboundPinRetirement{failure: &NativeEffectsUncertain{Cause: cause}}
	if _, err := removeNativeNamespaceContainer(context.Background(), nil, nil, RuntimeRecord{}, "", owner, partial); !errors.Is(err, cause) {
		t.Fatal("partial uncertainty was replaced", err)
	}
	partial = &nativeUnboundPinRetirement{removalAttempted: true}
	var uncertain *NativeEffectsUncertain
	if _, err := removeNativeNamespaceContainer(context.Background(), nil, nil, RuntimeRecord{}, "", owner, partial); !errors.As(err, &uncertain) || removes != 0 {
		t.Fatal("consumed partial removal retried/lost marker", err, removes)
	}
}
