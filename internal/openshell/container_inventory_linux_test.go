package openshell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func containerInventoryFixture(t *testing.T) (materialFixture, *NativeGeneration, []RuntimeRecord) {
	t.Helper()
	f, g := generationFixture(t)
	records := []RuntimeRecord{cloneRuntimeRecord(g.current)}
	effect := generationEffectFixture(t, g)
	for _, controller := range []string{"prior-controller", "current-controller"} {
		next, err := g.prepareNext(t.Context(), controller, g.OriginalPolicy(), generationTarget(g), effect)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, cloneRuntimeRecord(next.Record))
	}
	return f, g, records
}
func TestNativeContainerInventoryReadsAllHistoricalGenerationsWithoutPrivateCredentials(t *testing.T) {
	f, g, records := containerInventoryFixture(t)
	// Retirement needs metadata, not readable old/current activation credentials.
	for _, record := range records {
		if err := os.Remove(filepath.Join(f.root.Name(), "generations", record.Generation, "supervisor", "auth.json")); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := inventoryNativeContainerAt(t.Context(), f.root, g.current, f.release, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inventory.Close() }()
	got := inventory.Records()
	if len(got) != len(records) {
		t.Fatal("inventory dropped historical generation", len(got), len(records))
	}
	names := map[string]bool{}
	for _, record := range got {
		names[record.Generation] = true
	}
	for _, record := range records {
		if !names[record.Generation] {
			t.Fatal("inventory omitted non-current immutable record", record.Generation)
		}
	}
	got[0].Files["boundary/bootstrap.json"] = "caller-mutated"
	if reflect.DeepEqual(got, inventory.Records()) {
		t.Fatal("inventory returned mutable authority metadata")
	}
	if err := inventory.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", inventory, inventory), "test-secret") {
		t.Fatal("inventory leaked credential data")
	}
	_ = inventory.Close()
	if inventory.Check(t.Context()) == nil || inventory.Records() != nil {
		t.Fatal("closed inventory remained usable")
	}
}
func TestNativeContainerInventoryRefusesUntrustedOlderGenerationAndChangedMembers(t *testing.T) {
	for _, change := range []string{"extra-root", "extra-old-leaf", "old-record-invalid", "old-record-trailing", "old-record-name", "old-record-link", "old-record-hardlink", "old-record-mode", "missing-current", "origin", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			f, g, records := containerInventoryFixture(t)
			old := filepath.Join(f.root.Name(), "generations", records[0].Generation, "record.json")
			current := filepath.Join(f.root.Name(), "generations", g.current.Generation, "record.json")
			ctx := t.Context()
			var err error
			switch change {
			case "extra-root":
				err = os.WriteFile(filepath.Join(f.root.Name(), "unexpected"), []byte("retain"), 0600)
			case "extra-old-leaf":
				err = os.WriteFile(filepath.Join(filepath.Dir(old), "status", "unexpected"), []byte("retain"), 0600)
			case "old-record-invalid":
				err = os.WriteFile(old, []byte("{}"), 0600)
			case "old-record-trailing":
				raw, e := os.ReadFile(old)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(old, append(raw, []byte("{}")...), 0600)
			case "old-record-name":
				raw, e := os.ReadFile(current)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(old, raw, 0600)
			case "old-record-link":
				if err = os.Remove(old); err == nil {
					err = os.Symlink(current, old)
				}
			case "old-record-hardlink":
				if err = os.Remove(old); err == nil {
					err = os.Link(current, old)
				}
			case "old-record-mode":
				err = os.Chmod(old, 0644)
			case "missing-current":
				err = os.Remove(current)
			case "origin":
				err = os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), []byte("{}"), 0600)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			marker, err := os.ReadFile(filepath.Join(f.root.Name(), "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := inventoryNativeContainerAt(ctx, f.root, g.current, f.release, f.owners)
			if snapshot != nil {
				_ = snapshot.Close()
			}
			if err == nil {
				t.Fatal("unsafe runtime inventory was accepted", change)
			}
			after, e := os.ReadFile(filepath.Join(f.root.Name(), "current.json"))
			if e != nil || !bytes.Equal(marker, after) {
				t.Fatal("read-only refusal modified runtime", e)
			}
		})
	}
}
func TestNativeContainerInventoryRevalidationRefusesNewLeafRecordEditsAndReplacedMapping(t *testing.T) {
	for _, change := range []string{"new-allowed-leaf", "same-inode-record-edit", "root-replaced", "simulated-root-mount-change", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			f, g, records := containerInventoryFixture(t)
			inventory, err := inventoryNativeContainerAt(t.Context(), f.root, g.current, f.release, f.owners)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inventory.Close() }()
			ctx := t.Context()
			old := filepath.Join(f.root.Name(), "generations", records[0].Generation, "record.json")
			switch change {
			case "new-allowed-leaf":
				err = os.WriteFile(filepath.Join(filepath.Dir(old), "status", "runtime.json"), []byte("{}"), 0600)
			case "same-inode-record-edit":
				raw, e := os.ReadFile(old)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(old, append([]byte(" "), raw...), 0600)
			case "simulated-root-mount-change":
				// Metadata seam only: hold device/inode constant while the expected mount
				// differs. This never mounts anything or claims privileged mount coverage.
				inventory.identity.mount++
			case "root-replaced":
				if err = os.Rename(f.root.Name(), f.root.Name()+"-retained"); err == nil {
					err = os.Mkdir(f.root.Name(), 0700)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := inventory.Check(ctx); err == nil {
				t.Fatal("changed runtime inventory was accepted", change)
			}
		})
	}
}
func TestNativeContainerInventoryPublicBoundaryRefusesOrdinaryExecutor(t *testing.T) {
	f, _ := generationFixture(t)
	inventory, err := InventoryNativeContainer(t.Context(), f.record, f.release)
	if inventory != nil {
		_ = inventory.Close()
	}
	if err == nil {
		t.Fatal("ordinary executor obtained native inventory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InventoryNativeContainer(ctx, f.record, f.release); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled inventory reached native boundary", err)
	}
}
