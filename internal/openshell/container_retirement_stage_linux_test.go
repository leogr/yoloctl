package openshell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real protected ordinary files/publications, with synthetic namespace phases
// and effect progress. These comparisons do not prove any mount/nft/Stop ACK.
func containerRetirementStageFixture(t *testing.T) (*namespaceGenerationInventory, []*nativeContainerRetirementEntry, string) {
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
		state.Phase, state.DirectoryMountID, state.PinMountID = pinPresent, 122, 123
		if err := f.writeObservation(state); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	original, err := snapshotNamespaceGenerations(t.Context(), lock, records, cid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(original.Close)
	byGeneration := map[string]*namespacePinFiles{}
	for _, f := range original.pins {
		byGeneration[f.record.Generation] = f
	}
	var entries []*nativeContainerRetirementEntry
	for _, record := range original.records {
		e := &nativeContainerRetirementEntry{record: cloneRuntimeRecord(record), sameBoot: true, pin: byGeneration[record.Generation]}
		if e.pin != nil {
			e.origin = bytes.Clone(e.pin.origin)
			e.initialState = bytes.Clone(e.pin.current)
		}
		entries = append(entries, e)
	}
	return original, entries, root
}

func compareStageFixture(t *testing.T, original *namespaceGenerationInventory, entries []*nativeContainerRetirementEntry, active *nativeContainerRetirementEntry, p pinRetirementProgress) error {
	t.Helper()
	return compareCurrentRetirementNamespaceStage(t.Context(), original, entries, active, p, original.lock.directories[4].identity.mount)
}

func publishStageState(t *testing.T, e *nativeContainerRetirementEntry, phase namespacePinPhase) {
	t.Helper()
	var state namespacePinRecord
	if err := json.Unmarshal(e.pin.current, &state); err != nil {
		t.Fatal(err)
	}
	state.Phase = phase
	if err := e.pin.writeObservation(state); err != nil {
		t.Fatal(err)
	}
}

func completedStageProgress() pinRetirementProgress {
	return pinRetirementProgress{stopAttempted: true, stopAcknowledged: true, policyAttempted: true, policyAcknowledged: true, pinUnmountAttempted: true, pinUnmountAcknowledged: true, directoryUnmountAttempted: true, directoryUnmountAcknowledged: true, completed: true}
}

func TestContainerRetirementStageAllowsOnlyOwnPublishedStateAndMarker(t *testing.T) {
	original, entries, _ := containerRetirementStageFixture(t)
	if err := compareStageFixture(t, original, entries, nil, pinRetirementProgress{}); err != nil {
		t.Fatal("original complete stage failed", err)
	}
	e := entries[0]
	publishStageState(t, e, pinRetiring)
	if err := compareStageFixture(t, original, entries, e, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}); err != nil {
		t.Fatal("own durable state transition rejected", err)
	}
	publishStageState(t, e, pinDirectoryUnmounted)
	if err := e.pin.publishRetired(syntheticRetirementACKs()); err != nil {
		t.Fatal(err)
	}
	e.progress = completedStageProgress()
	if err := compareStageFixture(t, original, entries, nil, pinRetirementProgress{}); err != nil {
		t.Fatal("own complete producer-bound retirement rejected", err)
	}
	if err := original.check(t.Context()); err == nil {
		t.Fatal("ordinary strict namespace snapshot was weakened")
	}
}

func TestContainerRetirementStageRefusesAdoptedOrUnscopedPublications(t *testing.T) {
	for _, change := range []string{"missing-ACK", "same-bytes-new-inode", "other-generation", "lost-producer-identity", "wrong-producer-bytes", "foreign-producer-mount", "stale-boot-write", "missing-marker-producer", "wrong-marker-bytes"} {
		t.Run(change, func(t *testing.T) {
			original, entries, _ := containerRetirementStageFixture(t)
			e := entries[0]
			p := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
			publishStageState(t, e, pinRetiring)
			switch change {
			case "missing-ACK":
				p = pinRetirementProgress{}
			case "same-bytes-new-inode":
				if err := writePinFileAt(int(e.pin.directory.Fd()), "state.json", e.pin.current, true, original.lock.uid, original.lock.gid); err != nil {
					t.Fatal(err)
				}
			case "other-generation":
				other, err := original.lock.read(entries[1].record, original.cid)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = other.Close() }()
				var state namespacePinRecord
				if err := json.Unmarshal(other.current, &state); err != nil {
					t.Fatal(err)
				}
				state.Phase = pinRetiring
				if err := other.writeObservation(state); err != nil {
					t.Fatal(err)
				}
			case "lost-producer-identity":
				e.pin.currentPublication = nil
			case "wrong-producer-bytes":
				e.pin.currentPublication.raw = bytes.Clone(e.pin.currentPublication.raw)
				e.pin.currentPublication.raw[0] = '['
			case "foreign-producer-mount":
				e.pin.currentPublication.identity.mount++
			case "stale-boot-write":
				e.sameBoot = false
			case "missing-marker-producer", "wrong-marker-bytes":
				publishStageState(t, e, pinDirectoryUnmounted)
				if err := e.pin.publishRetired(syntheticRetirementACKs()); err != nil {
					t.Fatal(err)
				}
				p = completedStageProgress()
				if change == "missing-marker-producer" {
					e.pin.retiredPublication = nil
				} else {
					e.pin.retiredPublication.raw = bytes.Clone(e.pin.retiredPublication.raw)
					e.pin.retiredPublication.raw[0] = '['
				}
			}
			if err := compareStageFixture(t, original, entries, e, p); err == nil {
				t.Fatal("stage adopted unacknowledged or foreign publication", change)
			}
		})
	}
}

func TestContainerRetirementStageRetainsEveryOtherOriginalMember(t *testing.T) {
	for _, change := range []string{"lock-inode", "origin-inode", "extra-member", "directory-inode", "directory-mode", "directory-links", "no-init-inode"} {
		t.Run(change, func(t *testing.T) {
			original, entries, _ := containerRetirementStageFixture(t)
			e := entries[0]
			publishStageState(t, e, pinRetiring)
			next, err := readNamespaceGenerationInventory(t.Context(), original.lock, original.records, original.cid)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			name := "pins/" + e.record.Generation
			switch change {
			case "lock-inode":
				name = ".lock"
			case "origin-inode":
				name = "origins/" + entries[1].record.Generation + ".json"
			case "extra-member":
				next.members["unexpected"] = next.members[".lock"]
			case "no-init-inode":
				name = "no-init/" + entries[2].record.Generation + ".json"
			}
			if change != "extra-member" {
				m := next.members[name]
				switch change {
				case "directory-mode":
					m.mode ^= 1
				case "directory-links":
					m.links++
				default:
					m.identity.inode++
				}
				next.members[name] = m
			}
			if err := compareRetirementNamespaceStage(original, next, entries, e, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}, original.lock.directories[4].identity.mount); err == nil {
				t.Fatal("stage lost immutable all-generation member guard", change)
			}
		})
	}
}

func TestContainerRetirementStageDirectorySizeNeedsOwnDurablePublication(t *testing.T) {
	original, entries, _ := containerRetirementStageFixture(t)
	e := entries[0]
	check := func() error {
		next, err := readNamespaceGenerationInventory(t.Context(), original.lock, original.records, original.cid)
		if err != nil {
			return err
		}
		defer next.Close()
		name := "pins/" + e.record.Generation
		m := next.members[name]
		m.size++
		next.members[name] = m
		return compareRetirementNamespaceStage(original, next, entries, e, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}, original.lock.directories[4].identity.mount)
	}
	if err := check(); err == nil {
		t.Fatal("directory size adopted without original publication")
	}
	publishStageState(t, e, pinRetiring)
	if err := check(); err != nil {
		t.Fatal("own atomic publication directory size rejected despite exact membership/anchors", err)
	}
}

func TestContainerRetirementStageDoesNotAdoptReplacedOriginWithSameBytes(t *testing.T) {
	original, entries, root := containerRetirementStageFixture(t)
	e := entries[0]
	path := filepath.Join(root, "namespace-pins", original.lock.incarnation, "origins", e.record.Generation+".json")
	if err := os.Rename(path, filepath.Join(t.TempDir(), "retained-origin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, e.origin, 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareStageFixture(t, original, entries, nil, pinRetirementProgress{}); err == nil {
		t.Fatal("stage adopted identical bytes in replaced external origin")
	}
}

func TestContainerRetirementStageMountChangesRequireActualProgress(t *testing.T) {
	original, entries, _ := containerRetirementStageFixture(t)
	e := entries[0]
	publishStageState(t, e, pinDirectoryUnmounted)
	if err := e.pin.publishRetired(syntheticRetirementACKs()); err != nil {
		t.Fatal(err)
	}
	// Pure metadata model: script a prior distinct directory/NSFS mount in
	// the original snapshot. The ordinary files below represent the bare
	// mapping after scripted positive unmounts. No actual mount occurs here.
	base := original.lock.directories[4].identity.mount
	oldMount := base + 1000
	prefix := "pins/" + e.record.Generation
	for _, name := range []string{prefix, prefix + "/state.json"} {
		m := original.members[name]
		m.identity.mount = oldMount
		original.members[name] = m
	}
	m := original.members[prefix+"/namespace"]
	m.identity.mount = oldMount + 1
	m.identity.inode++
	original.members[prefix+"/namespace"] = m
	p := completedStageProgress()
	if err := compareStageFixture(t, original, entries, e, p); err != nil {
		t.Fatal("acknowledged owned mounts did not return to exact native base", err)
	}
	p.directoryUnmountAttempted = false
	p.directoryUnmountAcknowledged = false
	p.completed = false
	if err := compareStageFixture(t, original, entries, e, p); err == nil {
		t.Fatal("bare mapping adopted without actual directory-unmount acknowledgement")
	}
}

func TestContainerRetirementStageKeepsCompleteRecordAndActionCoverage(t *testing.T) {
	for _, change := range []string{"subset", "duplicate", "changed-record", "foreign-active", "unpaired-progress"} {
		t.Run(change, func(t *testing.T) {
			original, entries, _ := containerRetirementStageFixture(t)
			var active *nativeContainerRetirementEntry
			progress := pinRetirementProgress{}
			switch change {
			case "subset":
				entries = entries[:len(entries)-1]
			case "duplicate":
				entries[1] = entries[0]
			case "changed-record":
				entries[0].record.Files["supervisor/auth.json"] = strings.Repeat("e", 64)
			case "foreign-active":
				copy := *entries[0]
				active = &copy
			case "unpaired-progress":
				active = entries[0]
				progress = pinRetirementProgress{stopAcknowledged: true}
			}
			if err := compareStageFixture(t, original, entries, active, progress); err == nil {
				t.Fatal("stage lost complete immutable record/action coverage", change)
			}
		})
	}
}
