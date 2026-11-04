package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"golang.org/x/sys/unix"
)

type namespaceMetadataFile struct {
	name     string
	identity pinFileIdentity
	raw      []byte
	limit    int64
}

// Snapshot only fixed regular bookkeeping, never a recursive deletion scope.
// The admitted effect wrapper must independently prove bare native mapping
// and obtain an actual CID-removal ACK before remove is called.
type namespaceMetadataSnapshot struct {
	files    *namespacePinFiles
	children []namespaceMetadataFile
	origin   namespaceMetadataFile
}

func (s *namespaceMetadataSnapshot) checkDirectory() error {
	if s == nil || s.files == nil || s.files.directory == nil {
		return errors.New("closed namespace metadata inventory")
	}
	f := s.files
	if err := f.lock.check(); err != nil {
		return err
	}
	actual, err := pinIdentity(int(f.directory.Fd()), f.lock.uid, f.lock.gid, true)
	if err != nil || actual != f.identity {
		return errors.New("retained namespace metadata directory changed")
	}
	current, id, err := openPinDirectory(int(f.lock.directories[4].file.Fd()), f.record.Generation, false, f.lock.uid, f.lock.gid)
	if err != nil {
		return err
	}
	_ = current.Close()
	if id != f.identity || id.device != f.lock.directories[4].identity.device || id.mount != f.lock.directories[4].identity.mount {
		return errors.New("namespace metadata directory is not the selected bare native mapping")
	}
	return nil
}

func verifyNamespaceMetadataFile(parent int, want namespaceMetadataFile, uid, gid int) error {
	raw, id, err := readPinFileAt(parent, want.name, uid, gid, want.limit)
	if err != nil {
		return err
	}
	if id != want.identity || !bytes.Equal(raw, want.raw) {
		return errors.New("namespace bookkeeping file changed after inventory")
	}
	return nil
}

func inventoryNamespaceBookkeeping(f *namespacePinFiles) (*namespaceMetadataSnapshot, error) {
	s := &namespaceMetadataSnapshot{files: f}
	if err := s.checkDirectory(); err != nil {
		return nil, err
	}
	if !namespacePinGenerationName(f.record.Generation) || f.record.Launch.Binding.Incarnation != f.lock.incarnation {
		return nil, errors.New("invalid namespace metadata deletion selection")
	}
	originRaw, originID, err := readPinFileAt(int(f.lock.directories[3].file.Fd()), f.record.Generation+".json", f.lock.uid, f.lock.gid, 4<<20)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(originRaw, f.origin) {
		return nil, errors.New("namespace deletion origin differs from its selected snapshot")
	}
	origin, err := decodeNamespacePinOrigin(originRaw, f.record, f.cid)
	if err != nil {
		return nil, err
	}
	if origin.Directory.Inode != f.identity.inode {
		return nil, errors.New("namespace deletion directory differs from original inode")
	}
	s.origin = namespaceMetadataFile{f.record.Generation + ".json", originID, bytes.Clone(originRaw), 4 << 20}
	dir, id, err := openPinDirectory(int(f.directory.Fd()), ".", false, f.lock.uid, f.lock.gid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	if id != f.identity {
		return nil, errors.New("namespace inventory reopened a different directory")
	}
	names, err := dir.Readdirnames(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) < 2 || len(names) > 3 {
		return nil, errors.New("namespace inventory has unexpected/missing members")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || (name != "namespace" && name != "state.json" && name != "retired.json") {
			return nil, errors.New("namespace inventory contains an unknown/duplicate member")
		}
		seen[name] = true
	}
	if !seen["namespace"] || !seen["state.json"] {
		return nil, errors.New("namespace inventory lacks original placeholder/state")
	}
	// Keep the record until the other generation leaves have been removed.
	for _, name := range []string{"namespace", "retired.json", "state.json"} {
		if !seen[name] {
			continue
		}
		limit := int64(4 << 20)
		if name == "namespace" {
			limit = 0
		}
		if name == "retired.json" {
			limit = 4096
		}
		raw, id, err := readPinFileAt(int(f.directory.Fd()), name, f.lock.uid, f.lock.gid, limit)
		if err != nil {
			return nil, err
		}
		switch name {
		case "namespace":
			if len(raw) != 0 || id.inode != origin.Record.Placeholder.Inode || id.device != f.identity.device {
				return nil, errors.New("namespace deletion placeholder differs from original regular file")
			}
		case "state.json":
			if !bytes.Equal(raw, f.current) {
				return nil, errors.New("namespace state changed before deletion inventory")
			}
			if _, err := decodeNamespacePinRecord(raw, f.record, f.cid, origin.Record.OriginSHA256); err != nil {
				return nil, err
			}
		case "retired.json":
			if err := validateRetiredPinMarker(raw, f); err != nil {
				return nil, err
			}
		}
		s.children = append(s.children, namespaceMetadataFile{name, id, bytes.Clone(raw), limit})
	}
	if err := s.checkComplete(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *namespaceMetadataSnapshot) checkComplete() error {
	if err := s.checkDirectory(); err != nil {
		return err
	}
	f := s.files
	if err := verifyNamespaceMetadataFile(int(f.lock.directories[3].file.Fd()), s.origin, f.lock.uid, f.lock.gid); err != nil {
		return err
	}
	dir, _, err := openPinDirectory(int(f.directory.Fd()), ".", false, f.lock.uid, f.lock.gid)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(len(s.children) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != len(s.children) {
		return errors.New("namespace inventory member set changed")
	}
	for _, child := range s.children {
		found := false
		for _, name := range names {
			found = found || name == child.name
		}
		if !found {
			return errors.New("namespace inventory member was replaced")
		}
		if err := verifyNamespaceMetadataFile(int(f.directory.Fd()), child, f.lock.uid, f.lock.gid); err != nil {
			return err
		}
	}
	return nil
}

func (s *namespaceMetadataSnapshot) remove(ctx context.Context, verifyRemaining func(context.Context) error) error {
	if verifyRemaining == nil {
		return errors.New("namespace removal lacks current remaining runtime/storage verifier")
	}
	if err := s.checkComplete(); err != nil {
		return err
	}
	f := s.files
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return verifyRemaining(ctx)
	}
	for _, child := range s.children {
		if err := check(); err != nil {
			return err
		}
		if err := s.checkDirectory(); err != nil {
			return err
		}
		if err := verifyNamespaceMetadataFile(int(f.lock.directories[3].file.Fd()), s.origin, f.lock.uid, f.lock.gid); err != nil {
			return err
		}
		if err := verifyNamespaceMetadataFile(int(f.directory.Fd()), child, f.lock.uid, f.lock.gid); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(f.directory.Fd()), child.name, 0); err != nil {
			return err
		}
		if err := f.directory.Sync(); err != nil {
			return err
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := s.checkDirectory(); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(f.lock.directories[4].file.Fd()), f.record.Generation, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := f.lock.directories[4].file.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := f.lock.check(); err != nil {
		return err
	}
	if err := verifyNamespaceMetadataFile(int(f.lock.directories[3].file.Fd()), s.origin, f.lock.uid, f.lock.gid); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(f.lock.directories[3].file.Fd()), s.origin.name, 0); err != nil {
		return err
	}
	return f.lock.directories[3].file.Sync()
}

type namespaceRemovalProgress struct {
	removeAttempted, removeAcknowledged     bool
	metadataAttempted, metadataAcknowledged bool
	completed                               bool
}

type namespaceRemovalHooks struct {
	beforeRemove   func(context.Context) error
	removeCID      func(context.Context) error
	remaining      func(context.Context) error
	removeMetadata func(context.Context) error
}

// Called only after this call's positive Stop/namespace-retirement protocol.
// The fixed CID rm reply is preserved: no later inspection can replace it.
func runNamespaceContainerRemoval(ctx context.Context, h namespaceRemovalHooks) (p namespaceRemovalProgress, err error) {
	if h.beforeRemove == nil || h.removeCID == nil || h.remaining == nil || h.removeMetadata == nil {
		return p, errors.New("incomplete retired namespace/container removal protocol")
	}
	// The effect wrapper supplies its independent cleanup context. Preserve
	// that earlier deadline rather than renewing a consumed cleanup budget.
	cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fail := func(cause error) (namespaceRemovalProgress, error) { return p, &NativeEffectsUncertain{Cause: cause} }
	if err := cleanup.Err(); err != nil {
		return fail(err)
	}
	if err := h.beforeRemove(cleanup); err != nil {
		return fail(err)
	}
	if err := cleanup.Err(); err != nil {
		return fail(err)
	}
	p.removeAttempted = true
	if err := h.removeCID(cleanup); err != nil {
		return fail(err)
	}
	p.removeAcknowledged = true
	if err := h.remaining(cleanup); err != nil {
		return fail(err)
	}
	if err := cleanup.Err(); err != nil {
		return fail(err)
	}
	p.metadataAttempted = true
	if err := h.removeMetadata(cleanup); err != nil {
		return fail(err)
	}
	p.metadataAcknowledged, p.completed = true, true
	return p, nil
}

type namespaceContainerRemoveOwner struct {
	stop      nativePinStopHooks
	removeCID func(context.Context, string) error
	// Current admitted storage and ORIGINAL record/keys only; the container
	// is gone after the actual rm ACK. No "not found" observation is an ACK.
	remaining func(context.Context) error
}

func selectNamespaceRemovalFiles(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string) (*namespacePinFiles, error) {
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if lock == nil || lock.uid != 0 || lock.gid != 0 {
		return nil, errors.New("namespace removal requires native root ownership")
	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	if !namespacePinGenerationName(record.Generation) || record.Launch.Binding.Incarnation != lock.incarnation || !lowerHex(cid, 32) {
		return nil, errors.New("invalid namespace removal record/CID")
	}
	raw, _, err := readPinFileAt(int(lock.directories[3].file.Fd()), record.Generation+".json", 0, 0, 4<<20)
	if err != nil {
		return nil, err
	}
	origin, err := decodeNamespacePinOrigin(raw, record, cid)
	if err != nil {
		return nil, err
	}
	relation, err := origin.Record.Scope.classifyBoot(kernel.bootID)
	if err != nil {
		return nil, err
	}
	if relation == pinDifferentBoot {
		stale, err := observeStaleNamespacePin(ctx, kernel, lock, record, cid)
		if err != nil {
			return nil, err
		}
		return stale.files, nil // transfer the sole metadata FD; no namespace FD
	}
	if relation != pinSameBoot {
		return nil, errors.New("unknown namespace removal boot")
	}
	files, err := lock.read(record, cid)
	if err != nil {
		return nil, err
	}
	completed, err := files.readRetired()
	if err != nil || !completed {
		_ = files.Close()
		return nil, errors.Join(err, errors.New("same-boot removal lacks independent kernel-retirement completion"))
	}
	return files, nil
}

func verifyNamespaceRemovalMapping(ctx context.Context, kernel *kernelInfrastructure, f *namespacePinFiles, partial *nativeUnboundPinRetirement) error {
	if err := kernel.revalidate(ctx); err != nil {
		return err
	}
	target, err := nativePinDirectoryPath(f)
	if err != nil {
		return err
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(f.origin, &origin); err != nil {
		return err
	}
	relation, err := origin.Record.Scope.classifyBoot(kernel.bootID)
	if err != nil {
		return err
	}
	switch relation {
	case pinDifferentBoot:
		if partial != nil {
			return errors.New("in-call partial removal cannot cross a kernel boot")
		}
		current, err := observeStaleNamespacePin(ctx, kernel, f.lock, f.record, f.cid)
		if err != nil {
			return err
		}
		defer func() { _ = current.Close() }()
		if current.files.identity != f.identity || !bytes.Equal(current.files.origin, f.origin) || !bytes.Equal(current.files.current, f.current) {
			return errors.New("stale namespace removal mapping changed")
		}
	case pinSameBoot:
		if origin.Record.Scope.InfrastructureMount != kernel.mountID || origin.Record.Scope.Namespace == kernel.networkID {
			return errors.New("namespace removal differs from the selected VM binding")
		}
		if partial == nil {
			ok, err := f.readRetired()
			if err != nil || !ok {
				return errors.Join(err, errors.New("namespace removal lacks its independent retired marker"))
			}
		} else {
			if partial.source.reservation == nil || partial.source.reservation.files != f || partial.failure != nil || !partial.progress.ready || !partial.progress.stopAcknowledged || !partial.source.reservation.cleanupAttempted {
				return errors.New("partial metadata removal lacks its actual successful Stop/retirement token")
			}
			var state namespacePinRecord
			if err := json.Unmarshal(f.current, &state); err != nil {
				return err
			}
			if state.PinMountID != 0 || state.ReadbackSHA256 != "" || state.DirectoryMountID != partial.source.directoryMount {
				return errors.New("partial metadata removal changed the known no-namespace scope")
			}
			if partial.source.directoryMount != 0 && (!partial.progress.unmountAcknowledged || state.Phase != pinPartialUnmounted) {
				return errors.New("partial metadata removal lacks its actual directory-unmount completion")
			}
			if err := f.checkUnchanged(); err != nil {
				return err
			}
		}
	default:
		return errors.New("namespace removal has unknown boot relation")
	}
	// This complete bare inventory precedes reading/deleting the regular leaf.
	mounts, err := readNativePinMountInventory(ctx, kernel)
	if err != nil {
		return err
	}
	return validateBareStalePinDirectory(mounts, target, f.lock.directories[4].identity.mount, f.identity)
}

// removeNativeNamespaceContainer owns the actual fixed-CID rm acknowledgement.
// It is private/unwired: current admission/storage/record/process proof remains
// the native callback owner's duty. Full routes perform their own fresh Stop;
// a partial route requires the original successful in-call retirement token.
// Every post-retirement error is typed, with no inspection-as-rm-ACK fallback.
// Runtime keys/record are deliberately left for the following lifecycle step.
func removeNativeNamespaceContainer(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string, owner namespaceContainerRemoveOwner, partial *nativeUnboundPinRetirement) (namespaceRemovalProgress, error) {
	if owner.stop.owner == nil || owner.stop.stop == nil || owner.stop.inactive == nil || owner.removeCID == nil || owner.remaining == nil {
		return namespaceRemovalProgress{}, errors.New("namespace/container removal lacks admitted actual owner callbacks")
	}
	var files *namespacePinFiles
	if partial == nil {
		stopped, err := stopOwnedNativeNamespace(ctx, kernel, lock, record, cid, owner.stop)
		if err != nil {
			return namespaceRemovalProgress{}, err
		}
		if !stopped.completed || !stopped.stopAcknowledged {
			return namespaceRemovalProgress{}, &NativeEffectsUncertain{Cause: errors.New("namespace/container removal lacks this call's Stop completion")}
		}
	} else {
		if partial.removalAttempted {
			return namespaceRemovalProgress{}, &NativeEffectsUncertain{Cause: errors.New("partial container removal was already attempted; no retry")}
		}
		if partial.failure != nil {
			return namespaceRemovalProgress{}, partial.failure
		}
		if !partial.progress.ready || !partial.progress.stopAcknowledged || partial.source.reservation == nil || partial.source.reservation.files == nil {
			return namespaceRemovalProgress{}, errors.New("partial container removal lacks successful original retirement")
		}
		files = partial.source.reservation.files
		if files.lock != lock || files.cid != cid || recordIdentity(files.record) != recordIdentity(record) {
			return namespaceRemovalProgress{}, errors.New("partial removal token belongs to another record/CID/lock")
		}
		partial.removalAttempted = true
	}
	// Continue only this bounded suffix after its known Stop, even if the
	// request transport disappeared. No mutation is replayed after lost replies.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if files == nil {
		if err := owner.stop.inactive(cleanup); err != nil {
			return namespaceRemovalProgress{}, &NativeEffectsUncertain{Cause: err}
		}
		var err error
		files, err = selectNamespaceRemovalFiles(cleanup, kernel, lock, record, cid)
		if err != nil {
			return namespaceRemovalProgress{}, &NativeEffectsUncertain{Cause: err}
		}
	}
	defer func() { _ = files.Close() }()
	var snapshot *namespaceMetadataSnapshot
	h := namespaceRemovalHooks{
		beforeRemove: func(c context.Context) error {
			if err := owner.stop.inactive(c); err != nil {
				return err
			}
			if err := verifyNamespaceRemovalMapping(c, kernel, files, partial); err != nil {
				return err
			}
			var err error
			snapshot, err = inventoryNamespaceBookkeeping(files)
			if err != nil {
				return err
			}
			return owner.stop.inactive(c)
		},
		removeCID: func(c context.Context) error { return owner.removeCID(c, cid) },
		remaining: func(c context.Context) error {
			if err := owner.remaining(c); err != nil {
				return err
			}
			if err := verifyNamespaceRemovalMapping(c, kernel, files, partial); err != nil {
				return err
			}
			return snapshot.checkComplete()
		},
		removeMetadata: func(c context.Context) error {
			return snapshot.remove(c, func(check context.Context) error {
				if err := kernel.revalidate(check); err != nil {
					return err
				}
				return owner.remaining(check)
			})
		},
	}
	return runNamespaceContainerRemoval(cleanup, h)
}
