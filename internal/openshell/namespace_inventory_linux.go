package openshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

// namespaceGenerationInventory is structural observation under the caller's
// continuously held incarnation lock. Its input records must come from the
// complete qualified runtime inventory, not container labels or a best-effort
// listing. It supplies no kernel handle, retirement, Stop/rm ACK or deletion
// authority; namespace phases and old completion markers remain metadata.
// No public/effect caller is wired by this slice.
type namespaceGenerationInventory struct {
	lock             *namespacePinLock          // borrowed; Close never releases the caller's lock
	readers          *namespaceInventoryReaders // nil for strict metadata-only reads
	cid              string
	records          []RuntimeRecord
	members          map[string]namespaceInventoryMember
	pins             []*namespacePinFiles
	noInitialization []*noInitializationFiles
}
type namespaceInventoryMember struct {
	identity pinFileIdentity
	mode     uint32
	uid, gid uint32
	links    uint64
	size     int64
}

func (s *namespaceGenerationInventory) Close() {
	if s == nil {
		return
	}
	for _, f := range s.pins {
		_ = f.Close()
	}
	for _, f := range s.noInitialization {
		_ = f.Close()
	}
	s.pins = nil
	s.noInitialization = nil
	s.lock = nil
	s.readers = nil
}
func snapshotNamespaceGenerations(ctx context.Context, lock *namespacePinLock, records []RuntimeRecord, cid string) (*namespaceGenerationInventory, error) {
	s, err := readNamespaceGenerationInventory(ctx, lock, records, cid)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *namespaceGenerationInventory) check(ctx context.Context) error {
	if s == nil || s.lock == nil {
		return errors.New("namespace generation inventory is closed")
	}
	next, err := s.readCurrent(ctx)
	if err != nil {
		return err
	}
	defer next.Close()
	if !reflect.DeepEqual(s.members, next.members) {
		return errors.New("namespace generation inventory members changed")
	}
	if len(s.pins) != len(next.pins) || len(s.noInitialization) != len(next.noInitialization) {
		return errors.New("namespace generation inventory coverage changed")
	}
	for n, f := range s.pins {
		current := next.pins[n]
		if f.record.Generation != current.record.Generation || !bytes.Equal(f.origin, current.origin) || !bytes.Equal(f.current, current.current) {
			return errors.New("namespace generation immutable origin/state changed")
		}
	}
	for n, f := range s.noInitialization {
		current := next.noInitialization[n]
		if f.marker != current.marker || !bytes.Equal(f.raw, current.raw) {
			return errors.New("namespace generation no-initialization marker changed")
		}
	}
	return ctx.Err()
}
func (s *namespaceGenerationInventory) readCurrent(ctx context.Context) (*namespaceGenerationInventory, error) {
	if s == nil || s.lock == nil {
		return nil, errors.New("namespace generation inventory is closed")
	}
	return readNamespaceGenerationInventoryWith(ctx, s.lock, s.records, s.cid, s.readers)
}
func readNamespaceGenerationInventory(ctx context.Context, lock *namespacePinLock, records []RuntimeRecord, cid string) (*namespaceGenerationInventory, error) {
	return readNamespaceGenerationInventoryWith(ctx, lock, records, cid, nil)
}
func readNamespaceGenerationInventoryWith(ctx context.Context, lock *namespacePinLock, records []RuntimeRecord, cid string, readers *namespaceInventoryReaders) (_ *namespaceGenerationInventory, err error) {
	if readers != nil && (readers.pin == nil || readers.retired == nil) {
		return nil, errors.New("incomplete admitted namespace inventory readers")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lock == nil || len(records) == 0 || len(records) > 1024 || !lowerHex(cid, 32) {
		return nil, errors.New("namespace generation inventory lacks bounded records/CID/lock")
	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	s := &namespaceGenerationInventory{lock: lock, cid: cid, members: map[string]namespaceInventoryMember{}, readers: readers}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	known := map[string]RuntimeRecord{}
	origin := generationOrigin(records[0])
	for _, record := range records {
		if err := record.Launch.Validate(); err != nil {
			return nil, err
		}
		first := records[0].Launch
		if record.Launch.HelperLaunch() != first.HelperLaunch() || record.Launch.UID != first.UID || record.Launch.GID != first.GID || record.Launch.WorkspaceReadOnly != first.WorkspaceReadOnly {
			return nil, errors.New("namespace generation inventory changed immutable launch intent")
		}
		if !reservationGeneration(record.Generation) || !record.Launch.UnconfinedNetwork || record.Launch.Binding.Incarnation != lock.incarnation || generationOrigin(record) != origin {
			return nil, errors.New("namespace generation inventory contains incompatible runtime scope")
		}
		if _, exists := known[record.Generation]; exists {
			return nil, errors.New("namespace generation inventory repeats an immutable generation")
		}
		known[record.Generation] = cloneRuntimeRecord(record)
		s.records = append(s.records, cloneRuntimeRecord(record))
	}
	// All per-incarnation names are authoritative, including optional no-init.
	// Enumerating just the runtime records would miss orphan origin/pin names.
	inc, err := namespaceInventoryNames(ctx, lock.directories[2].file, "", s.members)
	if err != nil {
		return nil, err
	}
	for _, name := range inc {
		if name != ".lock" && name != "origins" && name != "pins" && name != noInitializationDirectory {
			return nil, errors.New("namespace generation inventory contains unknown incarnation member")
		}
	}
	origins, err := namespaceInventoryNames(ctx, lock.directories[3].file, "origins/", s.members)
	if err != nil {
		return nil, err
	}
	pins, err := namespaceInventoryNames(ctx, lock.directories[4].file, "pins/", s.members)
	if err != nil {
		return nil, err
	}
	groups := map[string]uint8{}
	for _, name := range origins {
		generation := strings.TrimSuffix(name, ".json")
		if generation+".json" != name || !reservationGeneration(generation) {
			return nil, errors.New("invalid namespace origin inventory name")
		}
		groups[generation] |= 1
	}
	for _, generation := range pins {
		if !reservationGeneration(generation) {
			return nil, errors.New("invalid namespace pin inventory name")
		}
		groups[generation] |= 2
	}
	noinit, _, err := openNoInitializationDirectory(lock, false)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if noinit != nil {
		defer func() { _ = noinit.Close() }()
		names, err := namespaceInventoryNames(ctx, noinit, noInitializationDirectory+"/", s.members)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			generation := strings.TrimSuffix(name, ".json")
			if generation+".json" != name || !reservationGeneration(generation) {
				return nil, errors.New("invalid no-initialization inventory name")
			}
			groups[generation] |= 4
		}
	}
	for generation, kind := range groups {
		if _, found := known[generation]; !found {
			return nil, errors.New("namespace inventory has an orphan generation outside runtime records")
		}
		if kind != 3 && kind != 4 {
			return nil, errors.New("namespace inventory has conflicting or incomplete origin/pin/no-init evidence")
		}
	}
	if len(groups) != len(known) {
		return nil, errors.New("namespace inventory omits a retained runtime generation")
	}
	for _, record := range s.records {
		if groups[record.Generation] == 4 {
			f, found, err := readNoInitializationAt(lock, record, cid)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, errors.New("namespace no-initialization inventory disappeared")
			}
			s.noInitialization = append(s.noInitialization, f)
			if err := verifyNoInitializationEntries(lock, record); err != nil {
				return nil, err
			}
			continue
		}
		var f *namespacePinFiles
		var err error
		if readers == nil {
			f, err = lock.read(record, cid)
		} else {
			f, err = readers.pin(ctx, lock, record, cid)
		}
		if err != nil {
			return nil, err
		}
		s.pins = append(s.pins, f)
		names, err := namespaceInventoryNames(ctx, f.directory, "pins/"+record.Generation+"/", s.members)
		if err != nil {
			return nil, err
		}
		present := map[string]bool{}
		for _, name := range names {
			if name != "namespace" && name != "state.json" && name != "retired.json" {
				return nil, errors.New("namespace generation contains unknown bookkeeping member")
			}
			present[name] = true
		}
		if !present["namespace"] || !present["state.json"] {
			return nil, errors.New("namespace generation lacks placeholder/state")
		}
		if readers == nil {
			_, err = f.readRetired()
		} else {
			_, err = readers.retired(ctx, f)
		}
		if err != nil {
			return nil, err
		} // structural binding only; admitted stale mapping is independent

	}
	if err := lock.check(); err != nil {
		return nil, err
	}
	return s, nil
}

// This retains observed identities even for a mounted namespace leaf. It does
// not open/adopt that leaf as a kernel namespace: the effect wrapper must use
// the independent typed NSFS/persistent-infrastructure/actual-plan checks.
func namespaceInventoryNames(ctx context.Context, directory *os.File, prefix string, members map[string]namespaceInventoryMember) ([]string, error) {
	if directory == nil || members == nil {
		return nil, errors.New("namespace inventory lacks its protected parent")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	read := os.NewFile(uintptr(fd), "namespace-inventory")
	defer func() { _ = read.Close() }()
	remaining := 1024 - len(members)
	if remaining < 0 {
		return nil, errors.New("namespace inventory exceeds member bound")
	}
	names, err := read.Readdirnames(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > remaining {
		return nil, errors.New("namespace inventory exceeds member bound")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return nil, errors.New("invalid namespace inventory member")
		}
		key := prefix + name
		if _, found := members[key]; found {
			return nil, errors.New("duplicate namespace inventory member")
		}
		// One kernel observation binds mount/inode/type/owner/mode/size;
		// do not combine an lstat of one mapping with a statx of a replacement.
		member, err := namespaceInventoryMemberAt(fd, name)
		if err != nil {
			return nil, err
		}
		members[key] = member
	}
	return names, nil
}

// One kernel observation binds every member field; callers never combine a
// name read from one inode with a different object's owner/mount metadata.
func namespaceInventoryMemberAt(parent int, name string) (namespaceInventoryMember, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return namespaceInventoryMember{}, errors.New("invalid namespace member component")
	}
	var sx unix.Statx_t
	const needed = unix.STATX_MNT_ID | unix.STATX_INO | unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_NLINK | unix.STATX_SIZE
	if err := unix.Statx(parent, name, unix.AT_SYMLINK_NOFOLLOW, needed, &sx); err != nil {
		return namespaceInventoryMember{}, err
	}
	device := unix.Mkdev(sx.Dev_major, sx.Dev_minor)
	if sx.Mask&needed != needed || sx.Mnt_id == 0 || device == 0 || sx.Ino == 0 {
		return namespaceInventoryMember{}, errors.New("namespace inventory lacks complete member identity")
	}
	if sx.Mode&unix.S_IFMT == unix.S_IFLNK {
		return namespaceInventoryMember{}, errors.New("namespace inventory refuses symlink member")
	}
	return namespaceInventoryMember{pinFileIdentity{device, sx.Ino, sx.Mnt_id}, uint32(sx.Mode), sx.Uid, sx.Gid, uint64(sx.Nlink), int64(sx.Size)}, nil
}
