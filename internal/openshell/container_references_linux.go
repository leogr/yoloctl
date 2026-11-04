package openshell

import (
	"context"
	"errors"
)

type containerReferencedFile struct{ device, inode uint64 }

// NativeContainerReferences is immutable original filesystem observation, not
// native admission, process/kernel ownership, a reservation or a Stop/rm ACK.
// It detects possible references through bind aliases even when a source uses
// another mount ID. A negative lookup alone NEVER authorizes removal. The
// admitted caller still owns complete current source/ancestor/consumer checks
// and the distinct original effect acknowledgements.
type NativeContainerReferences struct {
	inventory *NativeContainerInventory // borrowed original full view; lifetime only
	rootPath  string
	rootID    pinFileIdentity
	recordSHA string
	files     map[containerReferencedFile]struct{}
}

func (*NativeContainerReferences) String() string {
	return "native private file references [observation only]"
}
func (r *NativeContainerReferences) GoString() string { return r.String() }

// ObserveFilesystemReferences reads the COMPLETE original allowed tree twice
// around coherent per-member identities. Actual removal callers capture after
// their original Stop/inactivity and revalidate this view before rm; this
// observer supplies neither that history nor a fresh native launch grant.
// Secret/auth/key bodies are not opened for this inode inventory.
func (i *NativeContainerInventory) ObserveFilesystemReferences(ctx context.Context) (*NativeContainerReferences, error) {
	if err := i.Check(ctx); err != nil {
		return nil, err
	}
	members := map[string]namespaceInventoryMember{}
	if err := runtimeCleanupMembers(i.tree, "", i.identity.mount, members, nil); err != nil {
		return nil, err
	}
	if len(members) == 0 || len(members) > 1025 {
		return nil, errors.New("private reference observation lacks bounded complete tree")
	}
	r := &NativeContainerReferences{inventory: i, rootPath: i.root.Name(), rootID: i.identity, recordSHA: recordIdentity(i.current), files: map[containerReferencedFile]struct{}{}}
	for _, member := range members {
		key := containerReferencedFile{member.identity.device, member.identity.inode}
		if key.device == 0 || key.inode == 0 {
			return nil, errors.New("private reference observation lacks complete identity")
		}
		if _, exists := r.files[key]; exists {
			return nil, errors.New("private reference observation repeated original inode")
		}
		r.files[key] = struct{}{}
	}
	if err := i.Check(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *NativeContainerReferences) usable() error {
	if r == nil || r.inventory == nil || r.inventory.root == nil || r.inventory.tree == nil || len(r.files) == 0 || len(r.files) > 1025 || r.rootID != r.inventory.identity || r.rootPath != r.inventory.root.Name() {
		return errors.New("private reference observation is closed/incomplete")
	}
	return nil
}

// MatchesSelection compares only this original observation's selected metadata.
// It is deliberately not current disk/actor proof after metadata deletion.
func (r *NativeContainerReferences) MatchesSelection(selected RuntimeRecord, root string) error {
	if err := r.usable(); err != nil {
		return err
	}
	if root != r.rootPath || recordIdentity(selected) != r.recordSHA || recordIdentity(r.inventory.current) != r.recordSHA {
		return errors.New("private reference observation belongs to another original selection")
	}
	return nil
}

// CheckCurrent repeats the original complete private view BEFORE rm. Never use
// it while the acknowledged cleanup intentionally deletes private members.
func (r *NativeContainerReferences) CheckCurrent(ctx context.Context) error {
	if err := r.usable(); err != nil {
		return err
	}
	return r.inventory.Check(ctx)
}

// ReferencesFile is only immutable clash detection. Mount ID is intentionally
// not a key: a bind alias of the same filesystem inode remains a consumer.
// An invalid/closed observation or zero identity is an error, not false.
// A valid nonmember returns false as observation only, never cleanup authority.
func (r *NativeContainerReferences) ReferencesFile(device, inode uint64) (bool, error) {
	if err := r.usable(); err != nil {
		return false, err
	}
	if device == 0 || inode == 0 {
		return false, errors.New("private reference lookup lacks actual file identity")
	}
	_, exists := r.files[containerReferencedFile{device, inode}]
	return exists, nil
}
