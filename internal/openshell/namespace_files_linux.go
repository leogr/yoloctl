package openshell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"golang.org/x/sys/unix"
)

type pinFileIdentity struct{ device, inode, mount uint64 }
type pinDirectory struct {
	file     *os.File
	name     string
	identity pinFileIdentity
}

// namespacePinLock serializes all generations of one incarnation. Its stable
// inode is never removed/replaced here, including when an individual pin is
// retired. The eventual admitted native caller supplies the verified control
// root; root/uid/gid are private project-test seams, not workload inputs.
// All methods require this lock and one caller; Close releases only FDs.
type namespacePinLock struct {
	directories []pinDirectory // control, namespace-pins, incarnation, origins, pins
	file        *os.File
	identity    pinFileIdentity
	incarnation string
	uid, gid    int
}

func (l *namespacePinLock) Close() error {
	if l == nil {
		return nil
	}
	var result error
	if l.file != nil {
		result = l.file.Close()
		l.file = nil
	}
	for i := len(l.directories) - 1; i >= 0; i-- {
		result = errors.Join(result, l.directories[i].file.Close())
	}
	l.directories = nil
	return result
}

func pinIdentity(fd, uid, gid int, directory bool) (pinFileIdentity, error) {
	var st unix.Stat_t
	var sx unix.Statx_t
	mode, kind := uint32(0o600), uint32(unix.S_IFREG)
	if directory {
		mode, kind = 0o700, unix.S_IFDIR
	}
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != kind || st.Mode&0o7777 != mode || st.Uid != uint32(uid) || st.Gid != uint32(gid) || !directory && st.Nlink != 1 {
		return pinFileIdentity{}, errors.New("namespace bookkeeping object has an invalid type/owner/mode/link count")
	}
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx); err != nil {
		return pinFileIdentity{}, err
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id == 0 || st.Dev == 0 || st.Ino == 0 {
		return pinFileIdentity{}, errors.New("namespace bookkeeping lacks kernel file/mount identity")
	}
	return pinFileIdentity{uint64(st.Dev), st.Ino, sx.Mnt_id}, nil
}

func openPinDirectory(parent int, name string, create bool, uid, gid int) (*os.File, pinFileIdentity, error) {
	created := false
	if create {
		if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, pinFileIdentity{}, err
		} else {
			created = err == nil
		}
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, pinFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if created {
		err = errors.Join(unix.Fchown(fd, uid, gid), unix.Fchmod(fd, 0o700))
	}
	identity, identityErr := pinIdentity(fd, uid, gid, true)
	if err = errors.Join(err, identityErr); err != nil {
		_ = file.Close()
		return nil, pinFileIdentity{}, err
	}
	if create {
		parentIdentity, err := pinIdentity(parent, uid, gid, true)
		if err != nil || identity.mount != parentIdentity.mount || identity.device != parentIdentity.device {
			_ = file.Close()
			return nil, pinFileIdentity{}, errors.New("namespace bookkeeping parent contains an unexpected mount")
		}
		// Existing directories may belong to a concurrent initializer which
		// has not acknowledged their sync yet. Establish durability ourselves;
		// observing their current existence is not that earlier ACK.
		if err := errors.Join(unix.Fsync(fd), unix.Fsync(parent)); err != nil {
			_ = file.Close()
			return nil, pinFileIdentity{}, &atomicfile.CommitError{Err: err}
		}
	}
	return file, identity, nil
}

func lockNamespacePinsAt(ctx context.Context, control *os.File, incarnation string, uid, gid int) (*namespacePinLock, error) {
	return lockNamespacePinsAtMode(ctx, control, incarnation, uid, gid, true)
}

// Existing cleanup never initializes or syncs missing bookkeeping as a
// substitute for a previous publication. The held lock still permits the
// separately authorized retirement writes; it is not a read-only API.
func lockExistingNamespacePinsAt(ctx context.Context, control *os.File, incarnation string, uid, gid int) (*namespacePinLock, error) {
	return lockNamespacePinsAtMode(ctx, control, incarnation, uid, gid, false)
}

func lockNamespacePinsAtMode(ctx context.Context, control *os.File, incarnation string, uid, gid int, create bool) (_ *namespacePinLock, err error) {
	if control == nil || !lowerHex(incarnation, 16) || uid < 0 || gid < 0 {
		return nil, errors.New("invalid namespace bookkeeping root/identity")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(control.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "namespace-control-root")
	identity, err := pinIdentity(fd, uid, gid, true)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	l := &namespacePinLock{directories: []pinDirectory{{root, "", identity}}, incarnation: incarnation, uid: uid, gid: gid}
	defer func() {
		if err != nil {
			_ = l.Close()
		}
	}()
	for _, name := range []string{"namespace-pins", incarnation} {
		parent := l.directories[len(l.directories)-1]
		file, id, err := openPinDirectory(int(parent.file.Fd()), name, create, uid, gid)
		if err != nil {
			return nil, err
		}
		l.directories = append(l.directories, pinDirectory{file, name, id})
		if id.mount != identity.mount || id.device != identity.device {
			return nil, errors.New("namespace lock root contains an unexpected mount")
		}
	}
	inc := int(l.directories[2].file.Fd())
	var lock int
	if create {
		lock, err = unix.Openat(inc, ".lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	} else {
		lock, err = unix.Openat(inc, ".lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	}
	if errors.Is(err, unix.EEXIST) {
		lock, err = unix.Openat(inc, ".lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	} else if err == nil && create {
		err = errors.Join(unix.Fchown(lock, uid, gid), unix.Fchmod(lock, 0o600), unix.Fsync(lock), unix.Fsync(inc))
	}
	if lock >= 0 {
		l.file = os.NewFile(uintptr(lock), "namespace-incarnation-lock")
	}
	if err != nil {
		return nil, err
	}
	l.identity, err = pinIdentity(lock, uid, gid, false)
	if err != nil || l.identity.mount != identity.mount {
		return nil, errors.New("namespace lock is not a protected native inode")
	}
	for {
		err := unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check the retained lock's mapping after acquisition, before creating
	// transaction children. A concurrent initializer's unsynced lock is also
	// synced here without treating its existence as a historical result.
	if err := l.checkLock(); err != nil {
		return nil, err
	}
	if create {
		if err := errors.Join(l.file.Sync(), unix.Fsync(inc)); err != nil {
			return nil, &atomicfile.CommitError{Err: err}
		}
	}
	for _, name := range []string{"origins", "pins"} {
		file, id, err := openPinDirectory(inc, name, create, uid, gid)
		if err != nil {
			return nil, err
		}
		l.directories = append(l.directories, pinDirectory{file, name, id})
		if id.mount != identity.mount || id.device != identity.device {
			return nil, errors.New("namespace bookkeeping parent contains an unexpected mount")
		}
	}
	if err := l.check(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *namespacePinLock) check() error {
	if l == nil || l.file == nil || len(l.directories) != 5 {
		return errors.New("namespace bookkeeping lock is closed")
	}
	for i, dir := range l.directories {
		identity, err := pinIdentity(int(dir.file.Fd()), l.uid, l.gid, true)
		if err != nil || identity != dir.identity {
			return errors.New("namespace bookkeeping directory changed")
		}
		if i != 0 {
			parent := i - 1
			if i >= 3 {
				parent = 2
			}
			file, actual, err := openPinDirectory(int(l.directories[parent].file.Fd()), dir.name, false, l.uid, l.gid)
			if err != nil {
				return err
			}
			_ = file.Close()
			if actual != identity {
				return errors.New("namespace bookkeeping directory mapping changed")
			}
		}
	}
	return l.checkLock()
}

func (l *namespacePinLock) checkLock() error {
	if l == nil || l.file == nil || len(l.directories) < 3 {
		return errors.New("namespace bookkeeping lock is closed")
	}
	file, id, err := readPinFileAt(int(l.directories[2].file.Fd()), ".lock", l.uid, l.gid, 0)
	if err != nil || id != l.identity || len(file) != 0 {
		return errors.New("namespace bookkeeping lock mapping changed")
	}
	actual, err := pinIdentity(int(l.file.Fd()), l.uid, l.gid, false)
	if err != nil || actual != l.identity {
		return errors.New("namespace bookkeeping lock inode changed")
	}
	return nil
}

type namespacePinOrigin struct {
	Directory namespacePinPlaceholder `json:"directory"`
	Record    namespacePinRecord      `json:"record"`
}

// namespacePinFiles is metadata under the held lock, never a mount/Stop/nft
// ACK or launch/cleanup authority. The immutable origin lives OUTSIDE the
// future mounted generation directory and independently anchors its inode.
// No method mounts, enters, unmounts, repairs or deletes a generation.
type namespacePinFiles struct {
	lock      *namespacePinLock
	directory *os.File
	identity  pinFileIdentity
	record    RuntimeRecord
	cid       string
	origin    []byte
	current   []byte
	// Same-call producer observations only; decoded/cold metadata never creates
	// these. They supply no Stop/effect/removal authority.
	currentPublication, retiredPublication *namespacePinPublication
}

type namespacePinPublication struct {
	identity pinFileIdentity
	raw      []byte
}

func (f *namespacePinFiles) Close() error {
	if f == nil || f.directory == nil {
		return nil
	}
	err := f.directory.Close()
	f.directory = nil
	return err
}

// namespacePinReservation carries same-call exclusive file reservation ACKs.
// A cold read returns only namespacePinFiles and cannot mint this token.
// Its single creation attempt consumes it; Close unmounts/deletes nothing.
type namespacePinReservation struct {
	files            *namespacePinFiles
	original         pinFileIdentity
	consumed         bool
	cleanupAttempted bool
}

func (r *namespacePinReservation) Close() error {
	if r == nil {
		return nil
	}
	return r.files.Close()
}

func (l *namespacePinLock) reserve(scope namespacePinScope, record RuntimeRecord, cid string, plan nativeEgressPlan) (_ *namespacePinReservation, err error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	if err := scope.validate(record, cid); err != nil {
		return nil, err
	}
	if scope.Binding.Incarnation != l.incarnation || plan.validate(scope.Namespace, plan.Seed) != nil || plan.Inputs.OwnerSHA256 != egressOwner(record) {
		return nil, errors.New("namespace files differ from selected incarnation/plan")
	}
	pins := int(l.directories[4].file.Fd())
	if err := unix.Mkdirat(pins, record.Generation, 0o700); err != nil {
		return nil, err // Existing/partial generations are never adopted/repaired.
	}
	fd, err := unix.Openat(pins, record.Generation, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), record.Generation)
	defer func() { _ = dir.Close() }()
	if err := errors.Join(unix.Fchown(fd, l.uid, l.gid), unix.Fchmod(fd, 0o700)); err != nil {
		return nil, err
	}
	identity, err := pinIdentity(fd, l.uid, l.gid, true)
	if err != nil {
		return nil, err
	}
	if identity.mount != l.directories[4].identity.mount || identity.device != l.directories[4].identity.device {
		return nil, errors.New("new namespace generation has an unexpected mount")
	}
	if err := errors.Join(unix.Fsync(int(dir.Fd())), unix.Fsync(pins)); err != nil {
		return nil, &atomicfile.CommitError{Err: err}
	}
	if err := writePinFileAt(int(dir.Fd()), "namespace", nil, false, l.uid, l.gid); err != nil {
		return nil, err
	}
	_, placeholder, err := readPinFileAt(int(dir.Fd()), "namespace", l.uid, l.gid, 0)
	if err != nil {
		return nil, err
	}
	initial, err := newNamespacePinRecord(scope, record, cid, namespacePinPlaceholder{placeholder.device, placeholder.inode}, plan)
	if err != nil {
		return nil, err
	}
	origin, err := json.Marshal(namespacePinOrigin{namespacePinPlaceholder{identity.device, identity.inode}, initial})
	if err != nil {
		return nil, err
	}
	if err := writePinFileAt(int(l.directories[3].file.Fd()), record.Generation+".json", origin, false, l.uid, l.gid); err != nil {
		return nil, err
	}
	state, err := json.Marshal(initial)
	if err != nil {
		return nil, err
	}
	if err := writePinFileAt(int(dir.Fd()), "state.json", state, false, l.uid, l.gid); err != nil {
		return nil, err
	}
	files, err := l.read(record, cid)
	if err != nil {
		return nil, err
	}
	return &namespacePinReservation{files: files, original: identity}, nil
}

func (l *namespacePinLock) read(record RuntimeRecord, cid string) (_ *namespacePinFiles, err error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	if record.Launch.Binding.Incarnation != l.incarnation || !namespacePinGenerationName(record.Generation) || !lowerHex(cid, 32) {
		return nil, errors.New("invalid namespace file selection")
	}
	originRaw, _, err := readPinFileAt(int(l.directories[3].file.Fd()), record.Generation+".json", l.uid, l.gid, 4<<20)
	if err != nil {
		return nil, err
	}
	origin, err := decodeNamespacePinOrigin(originRaw, record, cid)
	if err != nil {
		return nil, err
	}
	dir, identity, err := openPinDirectory(int(l.directories[4].file.Fd()), record.Generation, false, l.uid, l.gid)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = dir.Close()
		}
	}()
	if identity.device != origin.Directory.Device || identity.inode != origin.Directory.Inode {
		return nil, errors.New("namespace generation directory differs from its external immutable origin")
	}
	state, _, err := readPinFileAt(int(dir.Fd()), "state.json", l.uid, l.gid, 4<<20)
	if err != nil {
		return nil, err
	}
	if _, err := decodeNamespacePinRecord(state, record, cid, origin.Record.OriginSHA256); err != nil {
		return nil, err
	}
	return &namespacePinFiles{lock: l, directory: dir, identity: identity, record: record, cid: cid, origin: originRaw, current: state}, nil
}

func decodeNamespacePinOrigin(raw []byte, record RuntimeRecord, cid string) (namespacePinOrigin, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return namespacePinOrigin{}, errors.New("invalid namespace origin bound")
	}
	if err := checkJSONKeys(raw); err != nil {
		return namespacePinOrigin{}, err
	}
	var origin namespacePinOrigin
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&origin); err != nil {
		return namespacePinOrigin{}, err
	}
	if origin.Directory.Device == 0 || origin.Directory.Inode == 0 || origin.Record.Phase != pinPlanned || origin.Record.validate(record, cid, origin.Record.OriginSHA256) != nil {
		return namespacePinOrigin{}, errors.New("namespace immutable origin differs from selected record")
	}
	return origin, nil
}

// writeObservation writes only bookkeeping after the caller's actual effect
// protocol; accepting a structurally valid phase does NOT acknowledge it. The
// immutable origin and last observed state must remain byte-identical first.
func (f *namespacePinFiles) writeObservation(next namespacePinRecord) error {
	if f == nil || f.directory == nil {
		return errors.New("namespace metadata handle is closed")
	}
	current, err := f.lock.read(f.record, f.cid)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	if current.identity != f.identity || !bytes.Equal(current.origin, f.origin) || !bytes.Equal(current.current, f.current) {
		return errors.New("namespace origin/directory/state changed before observation publication")
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(f.origin, &origin); err != nil {
		return err
	}
	if err := next.validate(f.record, f.cid, origin.Record.OriginSHA256); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	identity, err := writePinFileAtIdentity(int(f.directory.Fd()), "state.json", raw, true, f.lock.uid, f.lock.gid)
	if err != nil {
		return err
	}
	f.currentPublication = &namespacePinPublication{identity: identity, raw: bytes.Clone(raw)}
	f.current = raw
	return nil
}

func readPinFileAt(parent int, name string, uid, gid int, limit int64) ([]byte, pinFileIdentity, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, pinFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	id, err := pinIdentity(fd, uid, gid, false)
	var sx unix.Statx_t
	if err != nil || unix.Statx(parent, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx) != nil || sx.Mask&unix.STATX_MNT_ID == 0 || id.mount != sx.Mnt_id {
		return nil, pinFileIdentity{}, errors.New("namespace metadata file is not on its protected parent mount")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, pinFileIdentity{}, errors.New("namespace metadata exceeds its read bound")
	}
	return raw, id, nil
}

func namespacePinGenerationName(name string) bool {
	// Match RuntimeRecord/generationForSession, not a SHA256 filename: the
	// upstream token is "g" plus the first 24 digest bytes in lowercase hex.
	return len(name) == 49 && name[0] == 'g' && lowerHex(name[1:], 24)
}

func writePinFileAt(parent int, name string, raw []byte, replace bool, uid, gid int) error {
	_, err := writePinFileAtIdentity(parent, name, raw, replace, uid, gid)
	return err
}

// The identity is returned only after this call's positive rename AND parent
// fsync. Partial publication returns no producer evidence even if a current
// target exists; there is no readback-as-success fallback or retry.
func writePinFileAtIdentity(parent int, name string, raw []byte, replace bool, uid, gid int) (_ pinFileIdentity, err error) {
	originName := strings.HasSuffix(name, ".json") && namespacePinGenerationName(strings.TrimSuffix(name, ".json"))
	if name != "namespace" && name != "state.json" && name != "retired.json" && !originName || len(raw) > 4<<20 || replace && name != "state.json" {
		return pinFileIdentity{}, errors.New("invalid namespace metadata publication")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return pinFileIdentity{}, err
	}
	temporary := ".pin-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(parent, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return pinFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() { _ = file.Close() }()
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, unix.Unlinkat(parent, temporary, 0), unix.Fsync(parent))
		}
	}()
	if err := errors.Join(file.Chown(uid, gid), file.Chmod(0o600)); err != nil {
		return pinFileIdentity{}, err
	}
	if _, err := file.Write(raw); err != nil {
		return pinFileIdentity{}, err
	}
	if err := file.Sync(); err != nil {
		return pinFileIdentity{}, err
	}
	// Capture from OUR still-retained newly written FD, before rename. A
	// later path read cannot substitute another inode as producer evidence.
	created, err := pinIdentity(fd, uid, gid, false)
	if err != nil {
		return pinFileIdentity{}, err
	}
	if err := file.Close(); err != nil {
		return pinFileIdentity{}, err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	if err := unix.Renameat2(parent, temporary, parent, name, flags); err != nil {
		return pinFileIdentity{}, err
	}
	published = true
	if err := unix.Fsync(parent); err != nil {
		return pinFileIdentity{}, &atomicfile.CommitError{Err: err}
	}
	return created, nil
}
