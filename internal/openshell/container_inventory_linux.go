package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// NativeContainerInventory is a bounded read-only inventory of every immutable
// generation and allowed runtime leaf in one already selected incarnation.
// It supplies no admission, process, Stop/rm ACK or namespace-retirement proof.
// In particular, it cannot be converted into a fresh NativeReservation.
type NativeContainerInventory struct {
	root       *os.File
	identity   pinFileIdentity
	tree       *reservationNode
	current    RuntimeRecord
	owners     runtimeOwners
	records    []RuntimeRecord
	rawRecords map[string][]byte
}

func (*NativeContainerInventory) String() string {
	return "OpenShell container inventory [metadata only]"
}
func (i *NativeContainerInventory) GoString() string { return i.String() }
func (i *NativeContainerInventory) Close() error {
	if i == nil {
		return nil
	}
	if i.tree != nil {
		i.tree.close()
		i.tree = nil
	}
	if i.root != nil {
		err := i.root.Close()
		i.root = nil
		return err
	}
	return nil
}
func (i *NativeContainerInventory) Records() []RuntimeRecord {
	if i == nil || i.root == nil {
		return nil
	}
	result := make([]RuntimeRecord, len(i.records))
	for n, record := range i.records {
		result[n] = cloneRuntimeRecord(record)
	}
	return result
}

// InventoryNativeContainer uses the actual selected historical release/helper
// identity, without requiring fresh launch source qualification or opening
// private bootstrap/auth/key files. The admitted caller independently verifies
// native storage and fixed CID/launch/binds before and after the observation.
func InventoryNativeContainer(ctx context.Context, selected RuntimeRecord, release QualifiedRelease) (*NativeContainerInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("container inventory requires admitted native VM-root infrastructure")
	}
	if err := nativeGenerationIdentity(selected, release); err != nil {
		return nil, err
	}
	control, err := layout.OpenShellControlDir()
	if err != nil {
		return nil, err
	}
	root, err := openNativeGenerationDirectory(filepath.Join(control, "containers", selected.Launch.Binding.Incarnation))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	owners := runtimeOwners{0, 0, int(selected.Launch.UID), int(selected.Launch.GID), int(SupervisorUID), int(SupervisorGID)}
	return inventoryNativeContainerAt(ctx, root, selected, release, owners)
}

func inventoryNativeContainerAt(ctx context.Context, root *os.File, selected RuntimeRecord, release QualifiedRelease, owners runtimeOwners) (*NativeContainerInventory, error) {
	i, err := readNativeContainerInventoryAt(ctx, root, selected, release, owners, false)
	if err != nil {
		return nil, err
	}
	if err := i.Check(ctx); err != nil {
		_ = i.Close()
		return nil, err
	}
	return i, nil
}

// The ownership view is private and never returned by InventoryNativeContainer.
// It still inventories every immutable generation/record and role directory;
// changing observation files are deliberately not a pre-Stop ownership grant.
func readNativeContainerInventoryAt(ctx context.Context, root *os.File, selected RuntimeRecord, release QualifiedRelease, owners runtimeOwners, ownership bool) (_ *NativeContainerInventory, err error) {
	if root == nil {
		return nil, errors.New("container inventory lacks its selected native directory")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyGenerationSelection(root, selected, owners); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	i := &NativeContainerInventory{root: os.NewFile(uintptr(fd), root.Name()), current: cloneRuntimeRecord(selected), owners: owners, rawRecords: map[string][]byte{}}
	defer func() {
		if err != nil {
			_ = i.Close()
		}
	}()
	identity, err := pinIdentity(int(i.root.Fd()), owners.rootUID, owners.rootGID, true)
	if err != nil {
		return nil, err
	}
	i.identity = identity
	remaining := 1024 // same bounded on-disk runtime schema as owned cleanup
	if ownership {
		i.tree, err = inventoryReservationOwnership(ctx, i.root, "", identity.device, owners, &remaining)
	} else {
		i.tree, err = inventoryReservation(ctx, i.root, "", identity.device, owners, &remaining)
	}
	if err != nil {
		return nil, err
	}
	if err := verifyContainerInventoryMounts(i.tree); err != nil {
		return nil, err
	}
	var generations *reservationNode
	for _, node := range i.tree.children {
		if node.name == "generations" {
			generations = node
		}
	}
	if generations == nil || generations.file == nil || len(generations.children) == 0 {
		return nil, errors.New("container inventory lacks immutable generations")
	}
	children := append([]*reservationNode(nil), generations.children...)
	sort.Slice(children, func(a, b int) bool { return children[a].name < children[b].name })
	foundCurrent := false
	for _, node := range children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if node.file == nil || !reservationGeneration(node.name) {
			return nil, errors.New("container inventory contains an invalid generation directory")
		}
		raw, err := runtimeReadAt(node.file, "record.json", owners.rootUID, owners.rootGID, 1<<20)
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, errors.New("container inventory generation record has trailing/invalid JSON")
		}
		record, err := decodeRuntimeRecord(raw, release, selected.Launch.HelperLaunch())
		if err != nil {
			return nil, err
		}
		if record.Generation != node.name || generationOrigin(record) != generationOrigin(selected) || record.Launch.UID != selected.Launch.UID || record.Launch.GID != selected.Launch.GID || record.Launch.WorkspaceReadOnly != selected.Launch.WorkspaceReadOnly || record.Launch.UnconfinedNetwork != selected.Launch.UnconfinedNetwork {
			return nil, errors.New("container inventory generation belongs to another origin/launch intent")
		}
		if record.Generation == selected.Generation {
			expected, _ := json.Marshal(selected)
			if !bytes.Equal(raw, expected) {
				return nil, errors.New("container inventory immutable current record differs from selection")
			}
			foundCurrent = true
		}
		i.records = append(i.records, cloneRuntimeRecord(record))
		i.rawRecords[node.name] = bytes.Clone(raw)
	}
	if !foundCurrent {
		return nil, errors.New("container inventory omits the selected generation")
	}
	return i, nil
}

// Check repeats the complete bounded namespace/identity inventory, immutable
// records and origin/current selection. It is observation of present storage,
// never proof that another command completed or that cleanup is authorized.
func (i *NativeContainerInventory) Check(ctx context.Context) error {
	if i == nil || i.root == nil || i.tree == nil {
		return errors.New("container inventory is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := i.namedRoot(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	identity := i.identity
	remaining := 1024
	next, err := inventoryReservation(ctx, current, "", identity.device, i.owners, &remaining)
	if err != nil {
		return err
	}
	defer next.close()
	if err := verifyContainerInventoryMounts(next); err != nil {
		return err
	}
	if !sameContainerInventory(i.tree, next) {
		return errors.New("container inventory members changed")
	}
	for _, generation := range next.children {
		if generation.name != "generations" {
			continue
		}
		for _, node := range generation.children {
			raw, err := runtimeReadAt(node.file, "record.json", i.owners.rootUID, i.owners.rootGID, 1<<20)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, i.rawRecords[node.name]) {
				return errors.New("container inventory immutable record changed")
			}
		}
	}
	return ctx.Err()
}
func (i *NativeContainerInventory) namedRoot(ctx context.Context) (_ *os.File, err error) {
	if i == nil || i.root == nil || i.tree == nil {
		return nil, errors.New("container inventory is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := openNativeGenerationDirectory(i.root.Name())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = current.Close()
		}
	}()
	identity, err := pinIdentity(int(current.Fd()), i.owners.rootUID, i.owners.rootGID, true)
	if err != nil {
		return nil, err
	}
	if identity != i.identity {
		return nil, errors.New("container inventory native directory mapping changed")
	}
	if err := verifyGenerationSelection(current, i.current, i.owners); err != nil {
		return nil, err
	}
	return current, nil
}

func sameContainerInventory(a, b *reservationNode) bool {
	if a == nil || b == nil || !sameReservationNode(a.identity, b.identity) || a.identity.Mode != b.identity.Mode || a.identity.Uid != b.identity.Uid || a.identity.Gid != b.identity.Gid || a.identity.Nlink != b.identity.Nlink || len(a.children) != len(b.children) {
		return false
	}
	children := make(map[string]*reservationNode, len(a.children))
	for _, node := range a.children {
		children[node.name] = node
	}
	for _, node := range b.children {
		if !sameContainerInventory(children[node.name], node) {
			return false
		}
	}
	return true
}

// A file bind on the same filesystem also has the same st_dev. Verify kernel
// mount identities for ALL leaves, not only directories opened with NO_XDEV.
func verifyContainerInventoryMounts(root *reservationNode) error {
	if root == nil || root.file == nil {
		return errors.New("container inventory lacks retained root")
	}
	var base unix.Statx_t
	if err := unix.Statx(int(root.file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &base); err != nil {
		return err
	}
	if base.Mask&unix.STATX_MNT_ID == 0 || base.Mnt_id == 0 {
		return errors.New("container inventory lacks kernel mount identity")
	}
	var visit func(*reservationNode) error
	visit = func(parent *reservationNode) error {
		for _, child := range parent.children {
			var st unix.Statx_t
			if err := unix.Statx(child.parent, child.name, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st); err != nil {
				return err
			}
			if st.Mask&unix.STATX_MNT_ID == 0 || st.Mnt_id != base.Mnt_id {
				return errors.New("container inventory contains a mounted runtime leaf")
			}
			if child.file != nil {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(root)
}
