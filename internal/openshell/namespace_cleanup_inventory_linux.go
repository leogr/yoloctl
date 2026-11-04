package openshell

import (
	"context"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// A deletion-stage observation can contain intentionally incomplete groups.
// It is NEVER initial ownership/retirement/ACK authority. The admitted remover
// compares it with the complete original inventory minus only this call's
// positively acknowledged unlink+fsync steps, while keeping the stable lock.
type namespaceCleanupInventory struct {
	members map[string]namespaceInventoryMember
	raw     map[string][]byte
}

func inventoryNamespaceCleanup(ctx context.Context, lock *namespacePinLock, records []RuntimeRecord) (*namespaceCleanupInventory, error) {
	if lock == nil || len(records) == 0 || len(records) > 1024 {
		return nil, errors.New("namespace cleanup lacks original bounded generation scope")
	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, record := range records {
		if !reservationGeneration(record.Generation) || record.Launch.Binding.Incarnation != lock.incarnation || known[record.Generation] {
			return nil, errors.New("namespace cleanup has invalid/repeated original generation")
		}
		known[record.Generation] = true
	}
	s := &namespaceCleanupInventory{members: map[string]namespaceInventoryMember{}, raw: map[string][]byte{}}
	inc, err := namespaceInventoryNames(ctx, lock.directories[2].file, "", s.members)
	if err != nil {
		return nil, err
	}
	for _, name := range inc {
		if name != ".lock" && name != "origins" && name != "pins" && name != noInitializationDirectory {
			return nil, errors.New("namespace cleanup has unknown incarnation member")
		}
	}
	base := lock.directories[0].identity
	locked, exists := s.members[".lock"]
	if !exists || locked.identity != lock.identity || locked.mode != unix.S_IFREG|0600 || locked.uid != uint32(lock.uid) || locked.gid != uint32(lock.gid) || locked.links != 1 {
		return nil, errors.New("namespace cleanup lost original stable lock")
	}

	checkDirectory := func(key string, file *os.File) error {
		id, err := pinIdentity(int(file.Fd()), lock.uid, lock.gid, true)
		if err != nil {
			return err
		}
		member, exists := s.members[key]
		if !exists || member.identity != id || id.device != base.device || id.mount != base.mount || member.mode != unix.S_IFDIR|0700 || member.uid != uint32(lock.uid) || member.gid != uint32(lock.gid) {
			return errors.New("namespace cleanup directory changed original native mapping")
		}
		return nil
	}
	if err := checkDirectory("origins", lock.directories[3].file); err != nil {
		return nil, err
	}
	if err := checkDirectory("pins", lock.directories[4].file); err != nil {
		return nil, err
	}
	readLeaf := func(parent int, name, key string, limit int64) error {
		raw, id, err := readPinFileAt(parent, name, lock.uid, lock.gid, limit)
		if err != nil {
			return err
		}
		member, exists := s.members[key]
		if !exists || member.identity != id || id.device != base.device || id.mount != base.mount || member.mode != unix.S_IFREG|0600 || member.uid != uint32(lock.uid) || member.gid != uint32(lock.gid) || member.links != 1 || member.size != int64(len(raw)) {
			return errors.New("namespace cleanup regular leaf changed original native mapping")
		}
		s.raw[key] = raw
		return nil
	}
	origins, err := namespaceInventoryNames(ctx, lock.directories[3].file, "origins/", s.members)
	if err != nil {
		return nil, err
	}
	for _, name := range origins {
		generation := strings.TrimSuffix(name, ".json")
		if name != generation+".json" || !known[generation] {
			return nil, errors.New("namespace cleanup found orphan/unknown origin")
		}
		if err := readLeaf(int(lock.directories[3].file.Fd()), name, "origins/"+name, 4<<20); err != nil {
			return nil, err
		}
	}
	pins, err := namespaceInventoryNames(ctx, lock.directories[4].file, "pins/", s.members)
	if err != nil {
		return nil, err
	}
	for _, generation := range pins {
		if !known[generation] {
			return nil, errors.New("namespace cleanup found orphan/unknown pin directory")
		}
		if err := func() error {
			dir, _, err := openPinDirectory(int(lock.directories[4].file.Fd()), generation, false, lock.uid, lock.gid)
			if err != nil {
				return err
			}
			defer func() { _ = dir.Close() }()
			prefix := "pins/" + generation
			if err := checkDirectory(prefix, dir); err != nil {
				return err
			}
			names, err := namespaceInventoryNames(ctx, dir, prefix+"/", s.members)
			if err != nil {
				return err
			}
			for _, name := range names {
				limit := int64(4 << 20)
				switch name {
				case "namespace":
					limit = 0
				case "state.json":
				case "retired.json":
					limit = 4096
				default:
					return errors.New("namespace cleanup found unknown pin member")
				}
				if err := readLeaf(int(dir.Fd()), name, prefix+"/"+name, limit); err != nil {
					return err
				}
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}
	noinit, _, err := openNoInitializationDirectory(lock, false)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if noinit != nil {
		defer func() { _ = noinit.Close() }()
		if err := checkDirectory(noInitializationDirectory, noinit); err != nil {
			return nil, err
		}
		names, err := namespaceInventoryNames(ctx, noinit, noInitializationDirectory+"/", s.members)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			generation := strings.TrimSuffix(name, ".json")
			if name != generation+".json" || !known[generation] {
				return nil, errors.New("namespace cleanup found orphan/unknown no-init marker")
			}
			if err := readLeaf(int(noinit.Fd()), name, noInitializationDirectory+"/"+name, 4096); err != nil {
				return nil, err
			}
		}
	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	return s, nil
}
