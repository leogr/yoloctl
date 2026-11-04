package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"golang.org/x/sys/unix"
)

// This is comparison of a complete fresh read with ORIGINAL membership plus
// this call's successful writer identities and actual effect progress. Neither
// decoded phases nor the fresh read produce progress, publication ACKs or new
// deletion authority. The actual parent supplies the live Stop token, native
// context, full runtime inventory and per-action checks independently.
func compareRetirementNamespaceStage(original, next *namespaceGenerationInventory, entries []*nativeContainerRetirementEntry, active *nativeContainerRetirementEntry, progress pinRetirementProgress, nativeMount uint64) error {
	if original == nil || next == nil || original.lock == nil || next.lock != original.lock || original.cid != next.cid || nativeMount == 0 || len(entries) != len(original.records) || len(next.records) != len(original.records) {
		return errors.New("namespace stage lacks complete original scope")
	}
	if active == nil && progress != (pinRetirementProgress{}) {
		return errors.New("namespace progress has no selected original action")
	}
	expected := make(map[string]namespaceInventoryMember, len(original.members))
	for name, member := range original.members {
		expected[name] = member
	}
	known := map[string]string{}
	for n, record := range original.records {
		if recordIdentity(record) != recordIdentity(next.records[n]) || known[record.Generation] != "" {
			return errors.New("namespace stage changed original immutable generation set")
		}
		known[record.Generation] = recordIdentity(record)
	}
	freshPins := map[string]*namespacePinFiles{}
	for _, f := range next.pins {
		freshPins[f.record.Generation] = f
	}
	activeFound := active == nil
	for _, e := range entries {
		if e == nil || known[e.record.Generation] != recordIdentity(e.record) {
			return errors.New("namespace stage action changed original record coverage")
		}
		delete(known, e.record.Generation)
		p := e.progress
		if e == active {
			p = progress
			activeFound = true
		}
		if p.stopAttempted != p.stopAcknowledged || p.policyAttempted != p.policyAcknowledged || p.pinUnmountAttempted != p.pinUnmountAcknowledged || p.directoryUnmountAttempted != p.directoryUnmountAcknowledged || p.policyAcknowledged && !p.stopAcknowledged || p.pinUnmountAcknowledged && !p.policyAcknowledged || p.directoryUnmountAcknowledged && !p.pinUnmountAcknowledged || p.completed && !p.stopAcknowledged {
			return errors.New("namespace stage lacks ordered actual transition acknowledgements")
		}
		if e.pin == nil {
			if p.policyAttempted || p.pinUnmountAttempted || p.directoryUnmountAttempted {
				return errors.New("no-init observation acquired kernel effects")
			}
			continue
		}
		f := freshPins[e.record.Generation]
		if f == nil || !bytes.Equal(e.origin, f.origin) || !bytes.Equal(e.origin, e.pin.origin) {
			return errors.New("namespace stage changed original external origin")
		}
		if !e.sameBoot && (p.policyAttempted || p.pinUnmountAttempted || p.directoryUnmountAttempted || e.pin.currentPublication != nil || e.pin.retiredPublication != nil) {
			return errors.New("different-boot observation acquired current kernel/publication effects")
		}
		prefix := "pins/" + e.record.Generation
		directory, ok := expected[prefix]
		if !ok {
			return errors.New("namespace stage lost original directory")
		}
		oldMount := directory.identity.mount
		mount := oldMount
		if p.directoryUnmountAcknowledged {
			mount = nativeMount
			directory.identity.mount = mount
			expected[prefix] = directory
		}
		if e.pin.identity != directory.identity {
			return errors.New("namespace stage changed selected underlying directory mapping")
		}
		state := e.initialState
		if publication := e.pin.currentPublication; publication != nil {
			if !e.sameBoot || !p.stopAcknowledged || publication.identity.mount != oldMount && (!p.directoryUnmountAcknowledged || publication.identity.mount != nativeMount) {
				return errors.New("namespace stage lacks original state-publication/mount provenance")
			}
			member, err := publicationInventoryMember(publication, mount, original.lock.uid, original.lock.gid)
			if err != nil {
				return err
			}
			expected[prefix+"/state.json"] = member
			// Atomic replacement itself creates/removes a temporary directory entry;
			// native filesystems may retain increased directory size afterward.
			current, exists := next.members[prefix]
			if !exists {
				return errors.New("state publication lost parent directory")
			}
			directory.size = current.size
			expected[prefix] = directory
			state = publication.raw
		}
		if !bytes.Equal(state, f.current) || !bytes.Equal(state, e.pin.current) {
			return errors.New("namespace stage adopted state without exact successful producer bytes")
		}
		if p.policyAcknowledged && e.pin.currentPublication == nil {
			return errors.New("namespace kernel effects lack original observation publication")
		}
		if p.pinUnmountAcknowledged {
			origin, err := decodeNamespacePinOrigin(e.origin, e.record, original.cid)
			if err != nil {
				return err
			}
			placeholder := origin.Record.Placeholder
			expected[prefix+"/namespace"] = namespaceInventoryMember{identity: pinFileIdentity{placeholder.Device, placeholder.Inode, mount}, mode: unix.S_IFREG | 0600, uid: uint32(original.lock.uid), gid: uint32(original.lock.gid), links: 1, size: 0}
		}
		if publication := e.pin.retiredPublication; publication != nil {
			if !e.sameBoot || !p.completed || !p.directoryUnmountAcknowledged || publication.identity.mount != mount {
				return errors.New("namespace marker lacks this call's complete original effects/publication")
			}
			binding, err := pinRetirementBinding(f)
			if err != nil || binding.Schema != 1 {
				return errors.Join(err, errors.New("new retirement marker lacks exact full-retirement binding"))
			}
			canonical, err := json.Marshal(binding)
			if err != nil {
				return err
			}
			if !bytes.Equal(canonical, publication.raw) {
				return errors.New("new retirement marker differs from exact successful producer bytes")
			}
			name := prefix + "/retired.json"
			if _, exists := expected[name]; exists {
				return errors.New("namespace retirement replaced an original completion marker")
			}
			member, err := publicationInventoryMember(publication, mount, original.lock.uid, original.lock.gid)
			if err != nil {
				return err
			}
			expected[name] = member
			// The single appended regular member may change directory size. Its
			// complete names, underlying identity, owner/mode/links stay pinned.
			current, exists := next.members[prefix]
			if !exists {
				return errors.New("namespace marker lost parent directory")
			}
			directory.size = current.size
			expected[prefix] = directory
		} else if p.completed && p.directoryUnmountAcknowledged {
			return errors.New("namespace retirement omitted its successful completion publication")
		}
	}
	if !activeFound || len(known) != 0 {
		return errors.New("namespace stage omitted an original action")
	}
	if len(original.noInitialization) != len(next.noInitialization) {
		return errors.New("namespace stage changed no-init coverage")
	}
	for n, f := range original.noInitialization {
		if f.marker != next.noInitialization[n].marker || !bytes.Equal(f.raw, next.noInitialization[n].raw) {
			return errors.New("namespace stage changed original no-init completion")
		}
	}
	if !reflect.DeepEqual(expected, next.members) {
		return errors.New("namespace stage changed members beyond original acknowledged transitions")
	}
	return nil
}
