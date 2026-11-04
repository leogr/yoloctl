package openshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// nativeRemovalReadScope composes actual native identity, persistent kernel
// infrastructure, immutable runtime ownership and complete namespace records.
// It still supplies NO Stop/rm/table/deletion ACK or automatic recovery. The
// admitted adapter must provide independent current storage/fixed-CID/process
// proof and separately acknowledged holder/forward prefix before effects.
// All use stays inside withNativeKernelInfrastructure's locked-thread callback.
type nativeRemovalReadScope struct {
	kernel          *kernelInfrastructure // borrowed; outer kernel callback owns close
	control         *os.File
	controlIdentity pinFileIdentity
	containers      *os.File
	// Borrowed from the lexical read callback's ORIGINAL open. That callback
	// alone closes/clears it; close() must never close this alias a second time.
	originalReadRoot   *os.File
	containersIdentity pinFileIdentity
	ownership          *nativeContainerOwnership
	lock               *namespacePinLock
	namespaces         *namespaceGenerationInventory
	selected           RuntimeRecord
	release            QualifiedRelease
	cid                string
	verifyOwned        func(context.Context) error
	closed             bool
}

func (s *nativeRemovalReadScope) close() error {
	if s == nil {
		return nil
	}
	s.closed = true
	if s.namespaces != nil {
		s.namespaces.Close()
		s.namespaces = nil
	}
	var err error
	if s.lock != nil {
		err = errors.Join(err, s.lock.Close())
		s.lock = nil
	}
	if s.ownership != nil {
		s.ownership.Close()
		s.ownership = nil
	}
	if s.containers != nil {
		err = errors.Join(err, s.containers.Close())
		s.containers = nil
	}
	if s.control != nil {
		err = errors.Join(err, s.control.Close())
		s.control = nil
	}
	s.kernel = nil
	s.verifyOwned = nil
	return err
}
func withNativeRemovalReadScope(ctx context.Context, selected RuntimeRecord, release QualifiedRelease, cid string, verifyOwned func(context.Context) error, use func(*nativeRemovalReadScope) error) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 || verifyOwned == nil || use == nil || !lowerHex(cid, 32) {
		return errors.New("native removal scope requires admitted VM-root ownership callbacks and full CID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := nativeGenerationIdentity(selected, release); err != nil {
		return err
	}
	if err := verifyOwned(ctx); err != nil {
		return err
	}
	return withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) (err error) {
		s := &nativeRemovalReadScope{kernel: kernel, selected: cloneRuntimeRecord(selected), release: release, cid: cid, verifyOwned: verifyOwned}
		defer func() { err = errors.Join(err, s.close()) }()
		path, err := layout.OpenShellControlDir()
		if err != nil {
			return err
		}
		s.control, err = openNativeGenerationDirectory(path)
		if err != nil {
			return err
		}
		s.controlIdentity, err = pinIdentity(int(s.control.Fd()), 0, 0, true)
		if err != nil {
			return err
		}
		s.containers, s.containersIdentity, err = openPinDirectory(int(s.control.Fd()), "containers", false, 0, 0)
		if err != nil {
			return err
		}
		if s.containersIdentity.device != s.controlIdentity.device || s.containersIdentity.mount != s.controlIdentity.mount {
			return errors.New("native containers parent is outside original control filesystem")
		}
		root, err := openNativeGenerationDirectory(filepath.Join(path, "containers", selected.Launch.Binding.Incarnation))
		if err != nil {
			return err
		}
		s.originalReadRoot = root
		defer func() { _ = root.Close(); s.originalReadRoot = nil }()
		owners := runtimeOwners{0, 0, int(selected.Launch.UID), int(selected.Launch.GID), int(SupervisorUID), int(SupervisorGID)}
		s.ownership, err = captureNativeContainerOwnershipAt(ctx, root, selected, release, owners)
		if err != nil {
			return err
		}
		identity := s.ownership.view.identity
		if identity.device != s.controlIdentity.device || identity.mount != s.controlIdentity.mount {
			return errors.New("native runtime ownership is outside its original control filesystem")
		}
		if selected.Launch.UnconfinedNetwork {
			s.lock, err = lockExistingNamespacePinsAt(ctx, s.control, selected.Launch.Binding.Incarnation, 0, 0)
			if err != nil {
				return err
			}
			s.namespaces, err = snapshotNativeNamespaceGenerations(ctx, s.kernel, s.lock, s.ownership.records(), cid)
			if err != nil {
				return err
			}
		} else if err := verifyConfinedNamespaceAbsence(s.control, s.controlIdentity, selected.Launch.Binding.Incarnation); err != nil {
			return err
		}
		if err := s.check(ctx); err != nil {
			return err
		}
		return use(s)
	})
}

// Repeated independently of namespace state transitions. This does not adopt
// new namespace inventory or grant any ACK/deletion authority.
func (s *nativeRemovalReadScope) checkRuntime(ctx context.Context) error {
	if s == nil {
		return errors.New("native removal read scope is closed/incomplete")
	}
	return s.checkInfrastructure(ctx, s.verifyOwned)
}

// The admitted caller supplies current storage plus the appropriate actor
// predicate: selected existing CID before removal, authoritative remaining
// consumers afterward. This read never creates/replaces a Stop/rm ACK. A
// post-removal caller must separately hold the distinct original Remove token.
func (s *nativeRemovalReadScope) checkInfrastructure(ctx context.Context, verifyCurrent func(context.Context) error) error {
	if err := s.checkKernelStorage(ctx, verifyCurrent); err != nil {
		return err
	}
	return s.ownership.check(ctx, s.release)
}

// Partial private cleanup cannot re-read already removed immutable records.
// Keep current admission/storage/actor, kernel, canonical control/parent and
// complete mount proofs independent of the expected remaining private tree.
func (s *nativeRemovalReadScope) checkKernelStorage(ctx context.Context, verifyCurrent func(context.Context) error) error {
	if s == nil || s.closed || s.kernel == nil || s.control == nil || s.containers == nil || s.ownership == nil || s.ownership.view == nil || verifyCurrent == nil {
		return errors.New("native removal read scope lacks original infrastructure/current verifier")
	}
	if err := s.kernel.revalidate(ctx); err != nil {
		return err
	}
	if err := verifyCurrent(ctx); err != nil {
		return err
	}
	current, err := openNativeGenerationDirectory(s.control.Name())
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	id, err := pinIdentity(int(current.Fd()), 0, 0, true)
	if err != nil {
		return err
	}
	if id != s.controlIdentity {
		return errors.New("native removal control mapping changed")
	}
	parent, parentID, err := openPinDirectory(int(current.Fd()), "containers", false, 0, 0)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	retainedID, err := pinIdentity(int(s.containers.Fd()), 0, 0, true)
	if err != nil {
		return err
	}
	if parentID != s.containersIdentity || retainedID != parentID {
		return errors.New("native removal containers parent changed")
	}
	// The pre-Stop view skips volatile files, so use the COMPLETE current
	// infrastructure mount inventory independently. The same unprojected native
	// ext4 condition as no-init applies to this entire private runtime path;
	// this observation supplies neither namespace completion nor an ACK.
	mounts, err := readNativePinMountInventory(ctx, s.kernel)
	if err != nil {
		return err
	}
	if err := validateNoInitializationMounts(mounts, s.ownership.view.root.Name(), s.ownership.view.identity); err != nil {
		return fmt.Errorf("native private runtime mount inventory: %w", err)
	}
	return nil
}

func (s *nativeRemovalReadScope) check(ctx context.Context) error {
	if err := s.checkRuntime(ctx); err != nil {
		return err
	}

	if s.selected.Launch.UnconfinedNetwork {
		if s.lock == nil || s.namespaces == nil {
			return errors.New("unconfined removal scope lacks complete namespace inventory")
		}
		if err := s.lock.check(); err != nil {
			return err
		}
		if s.lock.directories[0].identity != s.controlIdentity {
			return errors.New("namespace inventory has another native control root")
		}
		return s.namespaces.check(ctx)
	}
	if s.lock != nil || s.namespaces != nil {
		return errors.New("confined runtime acquired unexpected namespace cleanup scope")
	}
	return verifyConfinedNamespaceAbsence(s.control, s.controlIdentity, s.selected.Launch.Binding.Incarnation)
}

// Absence is a current precondition only, never completion of any old command.
// Confined generations do not own this native unmediated-network ledger. An
// existing same-incarnation ledger is incompatible, even if it looks retired.
func verifyConfinedNamespaceAbsence(control *os.File, native pinFileIdentity, incarnation string) error {
	return verifyConfinedNamespaceAbsenceAt(control, native, incarnation, 0, 0)
}

// Current-user ownership parameters are project filesystem fixtures only.
func verifyConfinedNamespaceAbsenceAt(control *os.File, native pinFileIdentity, incarnation string, uid, gid int) error {
	if control == nil || !lowerHex(incarnation, 16) || native.device == 0 || native.inode == 0 || native.mount == 0 {
		return errors.New("confined namespace check lacks original control/identity")
	}
	observed, err := pinIdentity(int(control.Fd()), uid, gid, true)
	if err != nil {
		return err
	}
	if observed != native {
		return errors.New("confined namespace check changed original control identity")
	}
	parent, id, err := openPinDirectory(int(control.Fd()), "namespace-pins", false, uid, gid)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if id.device != native.device || id.mount != native.mount {
		return errors.New("namespace collection is outside native control filesystem")
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), incarnation, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("confined native incarnation has unexpected unmediated namespace bookkeeping")
}
