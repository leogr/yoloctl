package openshell

import (
	"bytes"
	"context"
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// staleNamespacePinObservation is read-only metadata from a proven different
// current kernel boot. It carries no namespace FD, effect ACK, creation token,
// removal permission or receipt conclusion. Close only releases the directory.
type staleNamespacePinObservation struct {
	files       *namespacePinFiles
	currentBoot string
	placeholder pinFileIdentity
}

func (s *staleNamespacePinObservation) Close() error {
	if s == nil {
		return nil
	}
	return s.files.Close()
}

// Check the complete inventory BEFORE opening a stale namespace leaf. No old
// namespace inode/PID is used to locate anything. The current native filesystem
// and mount come from the admitted reader's independently verified root.
func validateBareStalePinDirectory(mounts []nativePinMount, target string, nativeMount uint64, directory pinFileIdentity) error {
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" || nativeMount == 0 || directory.mount != nativeMount || directory.device == 0 || directory.inode == 0 {
		return errors.New("stale namespace directory lacks its current bare native selection")
	}
	var base *nativePinMount
	for i := range mounts {
		m := &mounts[i]
		if m.point == target || strings.HasPrefix(m.point, target+"/") {
			return errors.New("stale namespace path contains an unexpected current mount")
		}
		if m.id == nativeMount {
			if base != nil {
				return errors.New("duplicate stale namespace native base")
			}
			base = m
		}
	}
	if base == nil || base.filesystem != "ext4" || base.readOnly || base.device != directory.device || !path.IsAbs(base.root) || path.Clean(base.root) != base.root || !path.IsAbs(base.point) || path.Clean(base.point) != base.point {
		return errors.New("stale namespace metadata is outside its current writable native ext4 base")
	}
	relative, err := filepath.Rel(base.point, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return errors.New("stale namespace path is not beneath the current native filesystem")
	}
	return nil
}

func validateStalePinFiles(origin namespacePinOrigin, currentBoot string, native pinFileIdentity, directory, placeholder pinFileIdentity) error {
	relation, err := origin.Record.Scope.classifyBoot(currentBoot)
	if err != nil || relation != pinDifferentBoot {
		return errors.Join(err, errors.New("stale namespace metadata requires a verified different kernel boot"))
	}
	// dev_t and mount IDs are current-kernel observations, not persistent
	// filesystem identities. The current disk UUID/control-root proof remains
	// mandatory in the actual reader; old inode anchors select regular files
	// only, never a current namespace or a mount to unmount.
	if native.device == 0 || native.mount == 0 || origin.Directory.Inode == 0 || origin.Record.Placeholder.Inode == 0 || directory.device != native.device || placeholder.device != native.device || directory.mount != native.mount || placeholder.mount != native.mount || directory.inode != origin.Directory.Inode || placeholder.inode != origin.Record.Placeholder.Inode {
		return errors.New("stale namespace regular files differ from the original inodes/current native filesystem")
	}
	return nil
}

// observeStaleNamespacePin requires current native disk/admission authority
// from its caller, the stable existing ownership lock and a real locked-thread
// kernel context. It does no initializer writes, namespace entry or cleanup.
// In particular a different boot does not settle an interrupted operation.
func observeStaleNamespacePin(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string) (*staleNamespacePinObservation, error) {
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if lock == nil || lock.uid != 0 || lock.gid != 0 {
		return nil, errors.New("stale namespace reader requires protected native root ownership")
	}
	return observeStaleNamespacePinAt(ctx, lock, record, cid, stalePinReadContext{
		bootID: kernel.bootID, revalidate: kernel.revalidate, target: nativePinDirectoryPath,
		mounts: func(c context.Context) ([]nativePinMount, error) { return readNativePinMountInventory(c, kernel) },
	})
}

// The actual wrapper supplies independent kernel/disk/procfs readers. Current-
// user owned-file project fixtures supply synthetic kernel observations only;
// this private seam never creates native authority or enters a namespace.
type stalePinReadContext struct {
	bootID     string
	revalidate func(context.Context) error
	target     func(*namespacePinFiles) (string, error)
	mounts     func(context.Context) ([]nativePinMount, error)
}

func observeStaleNamespacePinAt(ctx context.Context, lock *namespacePinLock, record RuntimeRecord, cid string, reader stalePinReadContext) (_ *staleNamespacePinObservation, err error) {
	if lock == nil || reader.revalidate == nil || reader.target == nil || reader.mounts == nil || !canonicalUUID(reader.bootID) {
		return nil, errors.New("stale namespace observation lacks independent current context readers")
	}
	if err := reader.revalidate(ctx); err != nil {
		return nil, err
	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	if record.Launch.Binding.Incarnation != lock.incarnation || !namespacePinGenerationName(record.Generation) || !lowerHex(cid, 32) {
		return nil, errors.New("invalid stale namespace record/CID selection")
	}
	readOrigin := func() ([]byte, error) {
		raw, _, err := readPinFileAt(int(lock.directories[3].file.Fd()), record.Generation+".json", lock.uid, lock.gid, 4<<20)
		return raw, err
	}
	originRaw, err := readOrigin()
	if err != nil {
		return nil, err
	}
	origin, err := decodeNamespacePinOrigin(originRaw, record, cid)
	if err != nil {
		return nil, err
	}
	if relation, err := origin.Record.Scope.classifyBoot(reader.bootID); err != nil || relation != pinDifferentBoot {
		return nil, errors.Join(err, errors.New("stale namespace reader refuses same/unknown kernel boot"))
	}
	dir, identity, err := openPinDirectory(int(lock.directories[4].file.Fd()), record.Generation, false, lock.uid, lock.gid)
	if err != nil {
		return nil, err
	}
	files := &namespacePinFiles{lock: lock, directory: dir, identity: identity, record: record, cid: cid, origin: bytes.Clone(originRaw)}
	defer func() {
		if err != nil {
			_ = files.Close()
		}
	}()
	target, err := reader.target(files)
	if err != nil {
		return nil, err
	}
	native := lock.directories[4].identity
	checkDirectory := func() error {
		if err := lock.check(); err != nil {
			return err
		}
		current, id, err := openPinDirectory(int(lock.directories[4].file.Fd()), record.Generation, false, lock.uid, lock.gid)
		if err != nil {
			return err
		}
		_ = current.Close()
		if id != identity || id.inode != origin.Directory.Inode {
			return errors.New("stale namespace directory mapping changed")
		}
		mounts, err := reader.mounts(ctx)
		if err != nil {
			return err
		}
		return validateBareStalePinDirectory(mounts, target, native.mount, id)
	}
	if err := checkDirectory(); err != nil {
		return nil, err
	}
	// No namespace operation precedes this complete bare-mount check. A
	// concurrent unexpected NSFS replacement is refused as a non-regular file;
	// kernelNamespaceIdentity/setns are deliberately never used by this reader.
	readPlaceholder := func() (pinFileIdentity, error) {
		raw, id, err := readPinFileAt(int(dir.Fd()), "namespace", lock.uid, lock.gid, 0)
		if err != nil {
			return pinFileIdentity{}, err
		}
		if len(raw) != 0 {
			return pinFileIdentity{}, errors.New("stale namespace placeholder is not empty")
		}
		return id, nil
	}
	placeholder, err := readPlaceholder()
	if err != nil {
		return nil, err
	}
	if err := validateStalePinFiles(origin, reader.bootID, native, identity, placeholder); err != nil {
		return nil, err
	}
	state, _, err := readPinFileAt(int(dir.Fd()), "state.json", lock.uid, lock.gid, 4<<20)
	if err != nil {
		return nil, err
	}
	if _, err := decodeNamespacePinRecord(state, record, cid, origin.Record.OriginSHA256); err != nil {
		return nil, err
	}
	files.current = bytes.Clone(state)
	if err := checkDirectory(); err != nil {
		return nil, err
	}
	originAgain, err := readOrigin()
	if err != nil {
		return nil, err
	}
	stateAgain, _, err := readPinFileAt(int(dir.Fd()), "state.json", lock.uid, lock.gid, 4<<20)
	if err != nil {
		return nil, err
	}
	leafAgain, err := readPlaceholder()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(originRaw, originAgain) || !bytes.Equal(state, stateAgain) || leafAgain != placeholder {
		return nil, errors.New("stale namespace files changed during read-only observation")
	}
	if err := reader.revalidate(ctx); err != nil {
		return nil, err
	}
	return &staleNamespacePinObservation{files, reader.bootID, placeholder}, nil
}
