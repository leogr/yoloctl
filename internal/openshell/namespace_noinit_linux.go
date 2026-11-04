package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"

	"golang.org/x/sys/unix"
)

const noInitializationDirectory = "no-init"

// A separate root-only completion outside container projections. It says only
// this exact generation was positively stopped with original no-initialization
// evidence. It is not a launch grant or an interrupted-operation receipt.
type namespaceNoInitialization struct {
	Schema              int               `json:"schema"`
	RecordSHA256        string            `json:"record_sha256"`
	ContainerID         string            `json:"container_id"`
	KernelBootID        string            `json:"kernel_boot_id"`
	InfrastructureMount namespaceIdentity `json:"infrastructure_mount_namespace"`
}

type noInitializationFiles struct {
	lock         *namespacePinLock
	directory    *os.File
	identity     pinFileIdentity
	raw          []byte
	fileIdentity pinFileIdentity
	marker       namespaceNoInitialization
}

func (f *noInitializationFiles) Close() error {
	if f == nil || f.directory == nil {
		return nil
	}
	err := f.directory.Close()
	f.directory = nil
	return err
}

func openNoInitializationDirectory(lock *namespacePinLock, create bool) (*os.File, pinFileIdentity, error) {
	if err := lock.check(); err != nil {
		return nil, pinFileIdentity{}, err
	}
	file, id, err := openPinDirectory(int(lock.directories[2].file.Fd()), noInitializationDirectory, create, lock.uid, lock.gid)
	if err != nil {
		return nil, pinFileIdentity{}, err
	}
	if id.device != lock.directories[0].identity.device || id.mount != lock.directories[0].identity.mount {
		_ = file.Close()
		return nil, pinFileIdentity{}, errors.New("no-initialization directory is outside its native filesystem")
	}
	return file, id, nil
}
func decodeNoInitialization(raw []byte, record RuntimeRecord, cid string) (namespaceNoInitialization, error) {
	var got namespaceNoInitialization
	if len(raw) == 0 || len(raw) > 4096 || !lowerHex(cid, 32) || !namespacePinGenerationName(record.Generation) || !record.Launch.UnconfinedNetwork {
		return got, errors.New("invalid no-initialization completion scope/bound")
	}
	if err := checkJSONKeys(raw); err != nil {
		return got, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		return got, err
	}
	if got.Schema != 1 || got.RecordSHA256 != recordIdentity(record) || got.ContainerID != cid || !canonicalUUID(got.KernelBootID) || got.InfrastructureMount.Device == 0 || got.InfrastructureMount.Inode == 0 {
		return namespaceNoInitialization{}, errors.New("no-initialization completion differs from exact generation/CID/boot binding")
	}
	canonical, err := json.Marshal(got)
	if err != nil || !bytes.Equal(raw, canonical) {
		return namespaceNoInitialization{}, errors.Join(err, errors.New("noncanonical no-initialization completion"))
	}
	return got, nil
}

func readNoInitializationAt(lock *namespacePinLock, record RuntimeRecord, cid string) (*noInitializationFiles, bool, error) {
	if lock == nil || record.Launch.Binding.Incarnation != lock.incarnation || !namespacePinGenerationName(record.Generation) || !lowerHex(cid, 32) || !record.Launch.UnconfinedNetwork {
		return nil, false, errors.New("no-initialization reader has an invalid incarnation/generation/CID")
	}
	directory, id, err := openNoInitializationDirectory(lock, false)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f := &noInitializationFiles{lock: lock, directory: directory, identity: id}
	raw, fileID, err := readPinFileAt(int(directory.Fd()), record.Generation+".json", lock.uid, lock.gid, 4096)
	if errors.Is(err, unix.ENOENT) {
		_ = f.Close()
		return nil, false, nil
	}
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	marker, err := decodeNoInitialization(raw, record, cid)
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	f.raw, f.fileIdentity, f.marker = raw, fileID, marker
	return f, true, nil
}
func (f *noInitializationFiles) check(record RuntimeRecord, cid string) error {
	if f == nil || f.directory == nil || f.lock == nil {
		return errors.New("closed no-initialization completion")
	}
	current, found, err := readNoInitializationAt(f.lock, record, cid)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no-initialization completion disappeared")
	}
	defer func() { _ = current.Close() }()
	if current.identity != f.identity || current.fileIdentity != f.fileIdentity || !bytes.Equal(current.raw, f.raw) {
		return errors.New("no-initialization completion changed")
	}
	return nil
}

// Current no-initialization proof uses the complete mount inventory BEFORE
// testing the fixed directory entries. There is no namespace leaf to open and
// no old PID/inode is used to select a current kernel namespace.
func validateNoInitializationMounts(mounts []nativePinMount, target string, native pinFileIdentity) error {
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" || native.device == 0 || native.mount == 0 {
		return errors.New("invalid no-initialization native path")
	}
	var base *nativePinMount
	for i := range mounts {
		m := &mounts[i]
		if m.point == target || strings.HasPrefix(m.point, target+"/") {
			return errors.New("no-initialization path contains a current mount")
		}
		if m.id == native.mount {
			if base != nil {
				return errors.New("duplicate no-initialization native base")
			}
			base = m
		}
	}
	if base == nil || base.filesystem != "ext4" || base.readOnly || base.device != native.device || !path.IsAbs(base.root) || path.Clean(base.root) != base.root || !path.IsAbs(base.point) || path.Clean(base.point) != base.point {
		return errors.New("no-initialization path lacks current writable native ext4")
	}
	relative, err := filepath.Rel(base.point, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return errors.New("no-initialization path is outside native filesystem")
	}
	return nil
}
func verifyNoInitializationEntries(lock *namespacePinLock, record RuntimeRecord) error {
	if err := lock.check(); err != nil {
		return err
	}
	if record.Launch.Binding.Incarnation != lock.incarnation || !namespacePinGenerationName(record.Generation) {
		return errors.New("invalid no-initialization fixed selection")
	}
	var st unix.Stat_t
	for _, entry := range []struct {
		parent int
		name   string
	}{{int(lock.directories[3].file.Fd()), record.Generation + ".json"}, {int(lock.directories[4].file.Fd()), record.Generation}} {
		if err := unix.Fstatat(entry.parent, entry.name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return errors.Join(err, errors.New("no-initialization completion conflicts with namespace bookkeeping"))
		}
	}
	return nil
}
func verifyNoInitializationKernel(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord) error {
	if err := kernel.revalidate(ctx); err != nil {
		return err
	}
	target, err := nativeNamespaceGenerationPath(lock, record)
	if err != nil {
		return err
	}
	mounts, err := readNativePinMountInventory(ctx, kernel)
	if err != nil {
		return err
	}
	if err := validateNoInitializationMounts(mounts, target, lock.directories[4].identity); err != nil {
		return err
	}
	return verifyNoInitializationEntries(lock, record)
}

// Protocol seam models fresh Stop/verification/publication only. Lost replies
// stop the suffix. Any completion publication is one attempted mutation, never
// an observation-as-ACK fallback or a replacement of another marker.
type noInitializationStopHooks struct {
	before   func(context.Context) error
	stop     func(context.Context) error
	inactive func(context.Context) error
	complete func(context.Context, pinRetirementProgress) error
}

func runNoInitializationStop(ctx context.Context, h noInitializationStopHooks) (p pinRetirementProgress, err error) {
	if h.before == nil || h.stop == nil || h.inactive == nil || h.complete == nil {
		return p, errors.New("no-initialization Stop lacks complete admitted wiring")
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if err := h.before(ctx); err != nil {
		return p, err
	}
	p.stopAttempted = true
	if err := h.stop(ctx); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.stopAcknowledged = true
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := finish.Err(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	if err := h.inactive(finish); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	if err := finish.Err(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	if err := h.complete(finish, p); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.completed = true
	return p, nil
}

func stopUninitializedNativeGeneration(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, record RuntimeRecord, cid string, owner nativePinStopHooks, selected *noInitializationFiles) (pinRetirementProgress, error) {
	if err := kernel.revalidate(ctx); err != nil {
		return pinRetirementProgress{}, err
	}
	return completeUninitializedNativeStop(ctx, owner, selected, record, cid, kernel.bootID, kernel.mountID,
		func(c context.Context) error { return verifyNoInitializationKernel(c, kernel, lock, record) })
}

func completeUninitializedNativeStop(ctx context.Context, owner nativePinStopHooks, selected *noInitializationFiles, record RuntimeRecord, cid, boot string, mount namespaceIdentity, verify func(context.Context) error) (pinRetirementProgress, error) {
	if !canonicalUUID(boot) || mount.Device == 0 || mount.Inode == 0 || verify == nil || owner.owner == nil || owner.stop == nil || owner.inactive == nil || selected == nil {
		return pinRetirementProgress{}, errors.New("cold uninitialized Stop lacks independent completion/owner callbacks")
	}
	check := func(c context.Context) error {
		if err := checkUninitializedStopSelection(selected, record, cid, boot, mount); err != nil {
			return err
		}
		return verify(c)
	}
	return runNoInitializationStop(ctx, noInitializationStopHooks{
		before: func(c context.Context) error {
			if err := owner.owner(c); err != nil {
				return err
			}
			return check(c)
		},
		stop: owner.stop,
		inactive: func(c context.Context) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			return check(c)
		},
		complete: func(context.Context, pinRetirementProgress) error { return nil }, // Existing completion never rewritten.
	})
}

// Shared current protected selection check; the independent kernel verifier
// and actual Stop/inactivity authority remain mandatory in both callers.
func checkUninitializedStopSelection(selected *noInitializationFiles, record RuntimeRecord, cid, boot string, mount namespaceIdentity) error {
	if selected == nil || !canonicalUUID(boot) || mount.Device == 0 || mount.Inode == 0 {
		return errors.New("invalid no-initialization current native selection")
	}
	if err := selected.check(record, cid); err != nil {
		return err
	}
	if selected.marker.KernelBootID == boot && selected.marker.InfrastructureMount != mount {
		return errors.New("same-boot no-initialization marker has a different infrastructure namespace")
	}
	return nil
}

// Only the original owned-preparation Stop protocol calls this publisher.
// Its caller has repeated kernel/inactive/absence proof while holding lock.
// Structural metadata or a cold stopped observation cannot supply p's ACKs.
func publishNoInitializationAt(lock *namespacePinLock, record RuntimeRecord, cid, boot string, mount namespaceIdentity, p pinRetirementProgress) error {
	if !p.stopAttempted || !p.stopAcknowledged || p.completed || p.policyAttempted || p.policyAcknowledged || p.pinUnmountAttempted || p.pinUnmountAcknowledged || p.directoryUnmountAttempted || p.directoryUnmountAcknowledged {
		return errors.New("no-initialization publication lacks this original Stop-only acknowledgement")
	}
	marker := namespaceNoInitialization{1, recordIdentity(record), cid, boot, mount}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if _, err := decodeNoInitialization(raw, record, cid); err != nil {
		return err
	}
	if err := verifyNoInitializationEntries(lock, record); err != nil {
		return err
	}
	directory, _, err := openNoInitializationDirectory(lock, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return writePinFileAt(int(directory.Fd()), record.Generation+".json", raw, false, lock.uid, lock.gid)
}

// This is a refusal guard only; absence never supplies activation authority.
// A completed generation is permanently retired, so even the plain SDK entry
// cannot activate it again from saved metadata. Fresh Start must PrepareNext.
func rejectNoInitializationCompletion(prepared PreparedRuntime) error {
	if !prepared.Record.Launch.UnconfinedNetwork {
		return nil
	}
	if !namespacePinGenerationName(prepared.Record.Generation) || !lowerHex(prepared.Record.Launch.Binding.Incarnation, 16) {
		return errors.New("invalid activation generation before no-initialization guard")
	}
	control, err := layout.OpenShellControlDir()
	if err != nil {
		return err
	}
	relative := filepath.Join("namespace-pins", prepared.Record.Launch.Binding.Incarnation, noInitializationDirectory, prepared.Record.Generation+".json")
	_, _, err = safepath.ReadFileInfo(control, relative, 4096)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(err, errors.New("native generation has retained no-initialization completion; prepare a fresh generation, never replay activation"))
}
func rejectNoInitializationAt(lock *namespacePinLock, record RuntimeRecord) error {
	directory, _, err := openNoInitializationDirectory(lock, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	var st unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), record.Generation+".json", &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else {
		return errors.Join(err, errors.New("completed no-initialization generation cannot reserve a namespace"))
	}
}
