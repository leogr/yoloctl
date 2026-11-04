package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Actual owned-file origin/state/marker enumeration and repeated stage code,
// with declared synthetic current-kernel mount/admission observations. No VM,
// native reboot, real mount or namespace entry is performed or claimed.
func changedDeviceInventoryFixture(t *testing.T, change string) (*namespacePinLock, RuntimeRecord, string, *namespaceInventoryReaders) {
	t.Helper()
	root, _, lock, files, _, record := namespacePinFilesFixture(t)
	cid := files.cid
	var origin namespacePinOrigin
	if err := json.Unmarshal(files.origin, &origin); err != nil {
		t.Fatal(err)
	}
	currentDevice := lock.directories[4].identity.device
	oldDevice := currentDevice + 1
	if oldDevice == 0 {
		oldDevice = 1
	}
	if change == "same-boot-same-device" {
		oldDevice = currentDevice
	}
	origin.Directory.Device = oldDevice
	origin.Record.Placeholder.Device = oldDevice
	if change == "wrong-directory-inode" {
		origin.Directory.Inode++
	}
	if change == "wrong-placeholder-inode" {
		origin.Record.Placeholder.Inode++
	}
	identity, err := origin.Record.originIdentity()
	if err != nil {
		t.Fatal(err)
	}
	origin.Record.OriginSHA256 = identity
	rawOrigin, err := json.Marshal(origin)
	if err != nil {
		t.Fatal(err)
	}
	state := origin.Record // Legitimate historical initial state; no effect ACK.
	rawState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(root, "namespace-pins", lock.incarnation)
	directory := filepath.Join(inc, "pins", record.Generation)
	if err := os.WriteFile(filepath.Join(inc, "origins", record.Generation+".json"), rawOrigin, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), rawState, 0600); err != nil {
		t.Fatal(err)
	}
	files.origin = rawOrigin
	files.current = rawState
	binding, err := pinRetirementBinding(files)
	if err != nil || binding.Schema != 2 {
		t.Fatal("historical bare completion fixture invalid", binding, err)
	}
	marker, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if change == "wrong-marker" {
		binding.RecordSHA256 = strings.Repeat("e", 64)
		marker, _ = json.Marshal(binding)
	}
	if err := writePinFileAt(int(files.directory.Fd()), "retired.json", marker, false, lock.uid, lock.gid); err != nil {
		t.Fatal(err)
	}
	boot := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if change == "same-boot" || change == "same-boot-same-device" {
		boot = origin.Record.Scope.KernelBootID
	}
	base := lock.directories[4].identity
	reader := stalePinReadContext{bootID: boot, revalidate: func(context.Context) error {
		if change == "wrong-current-grant" {
			return errors.New("fixture current native disk grant mismatch")
		}
		return lock.check()
	}, target: func(*namespacePinFiles) (string, error) { return directory, nil }, mounts: func(context.Context) ([]nativePinMount, error) {
		m := nativePinMount{id: base.mount, device: base.device, root: "/", point: root, filesystem: "ext4", source: "fixture-native-disk"}
		if change == "wrong-current-disk" {
			m.device++
		}
		if change == "wrong-current-mount" {
			m.id++
		}
		mounts := []nativePinMount{m}
		if change == "current-descendant-mount" {
			mounts = append(mounts, nativePinMount{id: base.mount + 1000, point: directory + "/namespace"})
		}
		return mounts, nil
	}}
	readers := namespaceReadersForCurrentBoot(func(c context.Context) (string, error) {
		if err := reader.revalidate(c); err != nil {
			return "", err
		}
		return boot, nil
	}, func(c context.Context, l *namespacePinLock, r RuntimeRecord, id string) (*staleNamespacePinObservation, error) {
		return observeStaleNamespacePinAt(c, l, r, id, reader)
	})
	if change == "wrong-record" {
		record = cloneRuntimeRecord(record)
		record.Files["supervisor/auth.json"] = strings.Repeat("e", 64)
	}
	if change == "wrong-CID" {
		cid = strings.Repeat("b", 64)
	}
	return lock, record, cid, readers
}

func TestNamespaceNativeInventoryComposesChangedDeviceThroughRepeatedMarkerAndStageReads(t *testing.T) {
	lock, record, cid, readers := changedDeviceInventoryFixture(t, "")
	if strict, err := readNamespaceGenerationInventory(t.Context(), lock, []RuntimeRecord{record}, cid); err == nil {
		strict.Close()
		t.Fatal("ordinary strict reader accepted changed saved device")
	}
	s, err := readNamespaceGenerationInventoryWith(t.Context(), lock, []RuntimeRecord{record}, cid, readers)
	if err != nil {
		t.Fatal("admitted different-boot complete inventory rejected changed device before stale proof", err)
	}
	defer s.Close()
	if err := s.check(t.Context()); err != nil {
		t.Fatal("repeated admitted inventory lost boot-aware selection", err)
	}
	if len(s.pins) != 1 {
		t.Fatal("admitted inventory omitted original pin", len(s.pins))
	}
	f := s.pins[0]
	if strict, err := f.readRetired(); err == nil || strict {
		t.Fatal("same-boot marker reader was weakened for saved-device mismatch", strict, err)
	}
	if found, err := readers.retired(t.Context(), f); err != nil || !found {
		t.Fatal("admitted stale marker did not retain exact current bare proof", found, err)
	}
	e := &nativeContainerRetirementEntry{record: cloneRuntimeRecord(record), pin: f, origin: bytes.Clone(f.origin), initialState: bytes.Clone(f.current), sameBoot: false}
	entries := []*nativeContainerRetirementEntry{e}
	if err := compareCurrentRetirementNamespaceStage(t.Context(), s, entries, e, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true, completed: true}, lock.directories[4].identity.mount); err != nil {
		t.Fatal("actual shared stage read lost admitted different-boot selection", err)
	}
	if f.currentPublication != nil || f.retiredPublication != nil {
		t.Fatal("stale canonical metadata manufactured writer evidence")
	}
}

func TestNamespaceNativeInventoryChangedDeviceRefusalsStayComposed(t *testing.T) {
	for _, change := range []string{"same-boot", "wrong-current-grant", "wrong-current-disk", "wrong-current-mount", "current-descendant-mount", "wrong-directory-inode", "wrong-placeholder-inode", "wrong-record", "wrong-CID", "wrong-marker"} {
		t.Run(change, func(t *testing.T) {
			lock, record, cid, readers := changedDeviceInventoryFixture(t, change)
			if s, err := readNamespaceGenerationInventoryWith(t.Context(), lock, []RuntimeRecord{record}, cid, readers); err == nil {
				s.Close()
				t.Fatal("composed boot-aware inventory adopted invalid current selection", change)
			}
		})
	}
}

func TestNamespaceNativeInventoryRequiresActualNativeContextForProductionConstructor(t *testing.T) {
	if s, err := snapshotNativeNamespaceGenerations(t.Context(), nil, nil, nil, ""); err == nil || s != nil {
		t.Fatal("missing native root/kernel selection created admitted inventory", s, err)
	}
	readers := namespaceReadersForCurrentBoot(nil, nil)
	if _, err := readers.pin(t.Context(), nil, RuntimeRecord{}, ""); err == nil {
		t.Fatal("missing independent boot reader selected namespace metadata")
	}
}

func TestNamespaceNativeInventoryKeepsValidSameBootStrictPath(t *testing.T) {
	lock, record, cid, readers := changedDeviceInventoryFixture(t, "same-boot-same-device")
	s, err := readNamespaceGenerationInventoryWith(t.Context(), lock, []RuntimeRecord{record}, cid, readers)
	if err != nil {
		t.Fatal("valid same-boot native selection refused", err)
	}
	defer s.Close()
	if err := s.check(t.Context()); err != nil {
		t.Fatal("same-boot repeated inventory changed", err)
	}
	if found, err := s.pins[0].readRetired(); !found || err != nil {
		t.Fatal("same-boot marker lost original strict path", found, err)
	}
}
