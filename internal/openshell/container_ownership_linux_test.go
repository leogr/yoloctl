package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeContainerOwnershipKeepsImmutableScopeWhileObservationsChange(t *testing.T) {
	f, g, _, scope := containerRefreshFixture(t)
	ownership, err := captureNativeContainerOwnershipAt(t.Context(), f.root, g.current, f.release, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()
	status := filepath.Join(f.root.Name(), "generations", g.current.Generation, "status")
	// Deliberately unsafe changing observations are never opened/adopted by the
	// initial ownership view. The FULL post-Stop reader below must reject them.
	if err := os.Remove(filepath.Join(status, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root.Name(), "origin.json"), filepath.Join(status, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if err := ownership.check(t.Context(), f.release); err != nil {
		t.Fatal("mutable observation became pre-Stop ownership authority", err)
	}
	if len(ownership.records()) != len(scope.records) {
		t.Fatal("ownership capture omitted historical immutable records")
	}
	if err := ownership.view.Check(t.Context()); err == nil {
		t.Fatal("filtered ownership view became complete inventory")
	}
	removed := 0
	a, _ := refreshProtocolActions(t, scope, func(ctx context.Context, ack *nativeContainerStopACK) error {
		full, err := ownership.afterStop(ctx, f.release, g.cid, ack)
		if full != nil {
			_ = full.Close()
		}
		return err
	})
	a.remove = func(context.Context) error { removed++; return nil }
	err = completeNativeContainerRemoval(t.Context(), scope, a)
	var uncertain *NativeEffectsUncertain
	if err == nil || errors.As(err, &uncertain) || removed != 0 { // plain step error before rm (decision #3)
		t.Fatal("post-Stop schema checks adopted a skipped unsafe observation", err, removed)
	}
}
func TestNativeContainerOwnershipPromotesOnlyThroughOriginalACKAndFullSchema(t *testing.T) {
	f, g, _, scope := containerRefreshFixture(t)
	ownership, err := captureNativeContainerOwnershipAt(t.Context(), f.root, g.current, f.release, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()
	var full *NativeContainerInventory
	a, removed := refreshProtocolActions(t, scope, func(ctx context.Context, ack *nativeContainerStopACK) error {
		var err error
		full, err = ownership.afterStop(ctx, f.release, g.cid, ack)
		return err
	})
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal("qualified immutable capture did not reach full post-Stop inventory", err)
	}
	if full == nil || *removed != 1 {
		t.Fatal("ownership capture bypassed original protocol", full, *removed)
	}
	defer func() { _ = full.Close() }()
	if err := full.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := ownership.afterStop(t.Context(), f.release, g.cid, &nativeContainerStopACK{}); err == nil {
		t.Fatal("cold metadata promoted ownership without original Stop ACK")
	}
	ownership.Close()
	if ownership.check(t.Context(), f.release) == nil || ownership.records() != nil {
		t.Fatal("closed ownership view remained usable")
	}
}
func TestNativeContainerOwnershipRetainsLocksRecordsAndNamedRoot(t *testing.T) {
	for _, change := range []string{"lock-replaced", "lock-missing", "parent-missing", "record-replaced", "origin-replaced", "root-replaced"} {
		t.Run(change, func(t *testing.T) {
			f, g, _, _ := containerRefreshFixture(t)
			ownership, err := captureNativeContainerOwnershipAt(t.Context(), f.root, g.current, f.release, f.owners)
			if err != nil {
				t.Fatal(err)
			}
			defer ownership.Close()
			dir := filepath.Join(f.root.Name(), "generations", g.current.Generation)
			switch change {
			case "lock-replaced":
				path := filepath.Join(dir, "status/forwards", strings.Repeat("e", 32)+".lock")
				if err = os.Rename(path, filepath.Join(t.TempDir(), "retained-lock")); err == nil {
					err = os.WriteFile(path, nil, 0600)
				}
			case "lock-missing":
				err = os.Remove(filepath.Join(dir, "status/forwards", strings.Repeat("e", 32)+".lock"))
			case "parent-missing":
				err = os.Rename(filepath.Join(dir, "status/forwards"), filepath.Join(t.TempDir(), "retained-forwards"))
			case "record-replaced":
				path := filepath.Join(dir, "record.json")
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if err = os.Rename(path, filepath.Join(t.TempDir(), "retained-record")); err == nil {
					err = os.WriteFile(path, raw, 0600)
				}
			case "origin-replaced":
				path := filepath.Join(f.root.Name(), "origin.json")
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if err = os.Rename(path, filepath.Join(t.TempDir(), "retained-origin")); err == nil {
					err = os.WriteFile(path, raw, 0600)
				}
			case "root-replaced":
				if err = os.Rename(f.root.Name(), f.root.Name()+"-retained"); err == nil {
					err = os.Mkdir(f.root.Name(), 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ownership.check(t.Context(), f.release); err == nil {
				t.Fatal("ownership capture accepted changed immutable scope", change)
			}
		})
	}
}

func TestNativeContainerAtomicStatusIntermediatesHaveExactBoundedSchema(t *testing.T) {
	generation := "generations/g" + strings.Repeat("d", 48)
	name := ".yoloctl-" + strings.Repeat("e", 32) + ".tmp"
	for _, path := range []string{generation + "/status/" + name, generation + "/status/forwards/" + name} {
		directory, socket, allowed := reservationMember(path)
		if !allowed || directory || socket || !mutableRuntimeObservation(path) {
			t.Fatal("known atomic status intermediate missing from exact schema", path)
		}
	}
	for _, path := range []string{name, generation + "/supervisor/" + name, generation + "/status/.yoloctl-nothex.tmp", generation + "/status/.other-" + strings.Repeat("e", 32) + ".tmp", generation + "/status/forwards/" + name + ".extra"} {
		if _, _, allowed := reservationMember(path); allowed {
			t.Fatal("atomic status allowance widened outside exact writer scope", path)
		}
	}
	r := ownedReservationFixture(t)
	populateReservationFixture(t, r)
	for _, parent := range []string{generation + "/status", generation + "/status/forwards"} {
		if err := os.WriteFile(filepath.Join(r.Directory(), parent, name), []byte("interrupted own status write"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err != nil {
		t.Fatal("known creator cleanup rejected orphaned exact status intermediates", err)
	}
}

func TestNativeContainerAtomicIntermediatesKeepRoleLinkModeAndParentRefusals(t *testing.T) {
	for _, change := range []string{"mode", "link", "symlink", "root-parent", "supervisor-parent"} {
		t.Run(change, func(t *testing.T) {
			r := ownedReservationFixture(t)
			populateReservationFixture(t, r)
			generation := "generations/g" + strings.Repeat("d", 48)
			name := ".yoloctl-" + strings.Repeat("e", 32) + ".tmp"
			parent := generation + "/status"
			if change == "root-parent" {
				parent = ""
			}
			if change == "supervisor-parent" {
				parent = generation + "/supervisor"
			}
			path := filepath.Join(r.Directory(), parent, name)
			var err error
			switch change {
			case "symlink":
				err = os.Symlink(filepath.Join(r.Directory(), "current.json"), path)
			case "link":
				err = os.Link(filepath.Join(r.Directory(), "current.json"), path)
			default:
				err = os.WriteFile(path, []byte("own staging"), 0600)
				if change == "mode" && err == nil {
					err = os.Chmod(path, 0644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := r.RemoveAfterContainerRemoval(t.Context()); err == nil {
				t.Fatal("atomic intermediate allowance bypassed cleanup refusal", change)
			}
			if _, err := os.Lstat(filepath.Join(r.Directory(), "current.json")); err != nil {
				t.Fatal("unsafe staging inventory unlinked metadata before refusal", err)
			}
		})
	}
}
