package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// ReserveNativeContainer creates only fresh, private native bookkeeping for a
// new incarnation. The admitted lifecycle caller verifies storage and installs
// the qualified pair/helper before any destructive cleanup. This reservation
// must precede container create, so nerdctl never creates its control bind with
// generic permissions. It is neither create-history proof nor launch authority.
// Existing/stopped container generations use PrepareNative under their separate
// proved-stop lifecycle; this entry never adopts any existing incarnation.
func ReserveNativeContainer(ctx context.Context, binding Binding, release QualifiedRelease, helperSHA256 string) (string, error) {
	reservation, err := ReserveOwnedNativeContainer(ctx, binding, release, helperSHA256)
	if err != nil {
		return "", err
	}
	directory := reservation.Directory()
	return directory, reservation.Close()
}

// ReserveOwnedNativeContainer retains the actual exclusive directory inode for
// same-call startup cleanup. Close releases descriptors only; successful runtime
// state is never tied to this worker's lifetime. The caller still owns admission.
func ReserveOwnedNativeContainer(ctx context.Context, binding Binding, release QualifiedRelease, helperSHA256 string) (*NativeReservation, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("OpenShell reservation requires native VM-root infrastructure")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if err := release.ValidateHelperSource(); err != nil {
		return nil, err
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil || helperSHA256 != helper.BinarySHA256 || binding.Qualification != release.Identity() {
		return nil, errors.New("reservation requires the current qualified helper/release")
	}
	current := spaceenv.Current()
	if binding.Space != current.Space || binding.DiskID != current.DiskID || binding.DiskUUID != current.DiskUUID || binding.ControllerGeneration != current.Generation {
		return nil, errors.New("reservation belongs to another native disk/controller")
	}
	for _, relative := range []string{"runtime", "runtime/openshell"} {
		if err := safepath.PrivateDirectory(current.DiskMount, relative, 0, 0); err != nil {
			return nil, err
		}
	}
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return nil, err
	}
	origin := containerOrigin{binding.Space, binding.DiskID, binding.DiskUUID, binding.Box, binding.Incarnation, release.Identity(), helperSHA256}
	reservation, err := reserveOwnedContainerAt(root, origin, 0, 0)
	if err != nil {
		return nil, err
	}
	reservation.owners = runtimeOwners{0, 0, current.UID, current.GID, int(SupervisorUID), int(SupervisorGID)}
	return reservation, nil
}

// reserveContainerAt's owner parameters allow ordinary private-filesystem
// tests. Production supplies fixed root ownership and the verified native root.
// A failed publication retains its new private directory for explicit owned
// cleanup; it never repairs, replaces or removes a pre-existing incarnation.
func reserveContainerAt(root string, origin containerOrigin, uid, gid int) (string, error) {
	reservation, err := reserveOwnedContainerAt(root, origin, uid, gid)
	if err != nil {
		return "", err
	}
	directory := reservation.Directory()
	return directory, reservation.Close()
}

func reserveOwnedContainerAt(root string, origin containerOrigin, uid, gid int) (_ *NativeReservation, err error) {
	if !lowerHex(origin.Incarnation, 16) || !lowerHex(origin.Qualification, 32) || !lowerHex(origin.HelperSHA256, 32) {
		return nil, errors.New("invalid fresh container reservation identity")
	}
	if err := safepath.PrivateDirectory(root, "containers", uid, gid); err != nil {
		return nil, err
	}
	parent, err := unix.Open(filepath.Join(root, "containers"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = unix.Close(parent)
		}
	}()
	if err := unix.Mkdirat(parent, origin.Incarnation, 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(parent, origin.Incarnation, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()
	if uid != os.Geteuid() || gid != os.Getegid() {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return nil, err
		}
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return nil, err
	}
	if err := unix.Fsync(fd); err != nil {
		return nil, &atomicfile.CommitError{Err: err}
	}
	directory := filepath.Join(root, "containers", origin.Incarnation)
	raw, err := json.Marshal(origin)
	if err != nil {
		return nil, err
	}
	collectionID, err := pinIdentity(parent, uid, gid, true)
	if err != nil {
		return nil, err
	}
	directoryID, err := pinIdentity(fd, uid, gid, true)
	if err != nil || directoryID.device != collectionID.device || directoryID.mount != collectionID.mount {
		return nil, errors.New("fresh native reservation crossed its original collection mount")
	}
	var originID pinFileIdentity
	var originalOrigin *os.File
	defer func() {
		if err != nil && originalOrigin != nil {
			_ = originalOrigin.Close()
		}
	}()
	if err := publishPrivateOnceObserved(directory, "origin.json", raw, uid, gid, func(file *os.File) error {
		var err error
		originID, err = pinIdentity(int(file.Fd()), uid, gid, false)
		if err != nil {
			return err
		}
		if originID.device != directoryID.device || originID.mount != directoryID.mount {
			return errors.New("fresh native origin crossed its original reservation mount")
		}
		retained, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return err
		}
		originalOrigin = os.NewFile(uintptr(retained), "native-original-origin")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := unix.Fsync(parent); err != nil {
		return nil, &atomicfile.CommitError{Err: err}
	}
	var identity unix.Stat_t
	if err := unix.Fstat(fd, &identity); err != nil {
		return nil, err
	}
	return &NativeReservation{parent: os.NewFile(uintptr(parent), "containers"), directory: os.NewFile(uintptr(fd), directory), origin: raw, identity: identity, collectionID: collectionID, directoryID: directoryID, originID: originID, originalOrigin: originalOrigin, uid: uid, gid: gid, owners: runtimeOwners{uid, gid, uid, gid, uid, gid}, name: origin.Incarnation}, nil
}
