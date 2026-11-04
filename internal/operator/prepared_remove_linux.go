package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// PreparedEndpointRemoval retains the original registration BEFORE lifecycle
// effects. It is neither container ownership nor a Stop/rm acknowledgement.
// The native removal transaction must supply those independent proofs before
// invoking Withdraw. Close invalidates the observation; no persisted metadata
// can reconstruct a failed or interrupted withdrawal.
type PreparedEndpointRemoval struct {
	mu        sync.Mutex
	root      string
	endpoint  Endpoint
	identity  RegistrationIdentity
	parent    *os.File
	directory *os.File
	parentID  endpointFileID
	dirID     endpointFileID
	members   map[string]endpointFileID
	raw       map[string][]byte // nonsecret identity/routing metadata only
	uid, gid  uint32
	attempted bool
	closed    bool
	// These are private syscall seams for filesystem fault regressions. Public
	// construction always binds the actual unlink/fsync operations.
	unlink func(int, string, int) error
	sync   func(int) error
}

func (*PreparedEndpointRemoval) String() string   { return "<prepared operator withdrawal>" }
func (*PreparedEndpointRemoval) GoString() string { return "<prepared operator withdrawal>" }

type endpointFileID struct {
	dev, ino, mount uint64
	mode, uid, gid  uint32
	links           uint64
	size            int64
	mtime, ctime    unix.Timespec
}

func endpointID(fd int, name string) (endpointFileID, error) {
	var s unix.Stat_t
	var x unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	if err := unix.Fstatat(fd, name, &s, flags); err != nil {
		return endpointFileID{}, err
	}
	if err := unix.Statx(fd, name, flags, unix.STATX_INO|unix.STATX_MNT_ID, &x); err != nil {
		return endpointFileID{}, err
	}
	if x.Mask&(unix.STATX_INO|unix.STATX_MNT_ID) != unix.STATX_INO|unix.STATX_MNT_ID || x.Mnt_id == 0 || x.Ino != s.Ino {
		return endpointFileID{}, errors.New("operator withdrawal lacks stable mount identity")
	}
	return endpointFileID{uint64(s.Dev), s.Ino, x.Mnt_id, s.Mode, s.Uid, s.Gid, uint64(s.Nlink), s.Size, s.Mtim, s.Ctim}, nil
}

// Only mutable directory bookkeeping is excluded. Other registrations may be
// created in the parent, and this invocation unlinks its own directory members.
func (i endpointFileID) sameDirectory(j endpointFileID) bool {
	return i.dev == j.dev && i.ino == j.ino && i.mount == j.mount && i.mode == j.mode && i.uid == j.uid && i.gid == j.gid
}

func openEndpointDirectory(fd int, name string, sameMount bool) (*os.File, error) {
	resolve := uint64(unix.RESOLVE_NO_SYMLINKS)
	if sameMount {
		resolve |= unix.RESOLVE_BENEATH | unix.RESOLVE_NO_XDEV
	}
	n, err := unix.Openat2(fd, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: resolve})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(n), "operator directory"), nil
}

func PrepareEndpointRemoval(ctx context.Context, endpoint Endpoint, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("prepared operator withdrawal requires native VM-root infrastructure")
	}
	return prepareEndpointRemovalAt(ctx, Root, endpoint, 0, 0, verifyOwned)
}

// PrepareIncompleteEndpointRemoval captures the separate initial profile left
// when identity publication succeeded but routing metadata did not, or cleanup
// removed routing metadata before completing. Missing client.json is required
// at this original observation; losing it later cannot switch a complete
// observation into this branch. Even an original socket still requires actual
// publisher/Accept/stream settlement and the independent removal ACKs.
func PrepareIncompleteEndpointRemoval(ctx context.Context, endpoint Endpoint, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("prepared operator withdrawal requires native VM-root infrastructure")
	}
	return prepareIncompleteEndpointRemovalAt(ctx, Root, endpoint, 0, 0, verifyOwned)
}

func prepareEndpointRemovalAt(ctx context.Context, root string, endpoint Endpoint, uid, gid int, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	return prepareRegistrationProfileAt(ctx, root, LegacyRegistrationIdentity(endpoint), uid, gid, verifyOwned, endpointCompleteRegistration)
}

func prepareIncompleteEndpointRemovalAt(ctx context.Context, root string, endpoint Endpoint, uid, gid int, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	return prepareRegistrationProfileAt(ctx, root, LegacyRegistrationIdentity(endpoint), uid, gid, verifyOwned, endpointIncompleteRegistration)
}

type endpointRegistrationProfile uint8

const (
	endpointCompleteRegistration endpointRegistrationProfile = iota + 1
	endpointIncompleteRegistration
)

// PrepareRegistrationRemoval preserves the complete selected legacy/native
// tag. Identity equality is observation only; actual native ownership/current
// consumers/publisher/stream proofs and original ACKs remain the caller's duty.
func PrepareRegistrationRemoval(ctx context.Context, identity RegistrationIdentity, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("prepared operator withdrawal requires native VM-root infrastructure")
	}
	return prepareRegistrationProfileAt(ctx, Root, identity, 0, 0, verifyOwned, endpointCompleteRegistration)
}

func PrepareIncompleteRegistrationRemoval(ctx context.Context, identity RegistrationIdentity, verifyOwned func(context.Context) error) (*PreparedEndpointRemoval, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("prepared operator withdrawal requires native VM-root infrastructure")
	}
	return prepareRegistrationProfileAt(ctx, Root, identity, 0, 0, verifyOwned, endpointIncompleteRegistration)
}

func prepareEndpointProfileAt(ctx context.Context, root string, endpoint Endpoint, uid, gid int, verifyOwned func(context.Context) error, profile endpointRegistrationProfile) (*PreparedEndpointRemoval, error) {
	return prepareRegistrationProfileAt(ctx, root, LegacyRegistrationIdentity(endpoint), uid, gid, verifyOwned, profile)
}

// Identity returns a copied original tagged observation, never a deletion grant.
func (p *PreparedEndpointRemoval) Identity() (RegistrationIdentity, error) {
	if p == nil {
		return RegistrationIdentity{}, errors.New("operator withdrawal observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.parent == nil || p.directory == nil {
		return RegistrationIdentity{}, errors.New("operator withdrawal observation is closed or incomplete")
	}
	return p.identity, p.identity.Validate()
}

func prepareRegistrationProfileAt(ctx context.Context, root string, identity RegistrationIdentity, uid, gid int, verifyOwned func(context.Context) error, profile endpointRegistrationProfile) (_ *PreparedEndpointRemoval, err error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	endpoint := identity.Endpoint
	if profile != endpointCompleteRegistration && profile != endpointIncompleteRegistration {
		return nil, errors.New("operator withdrawal registration profile is unavailable")
	}
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || verifyOwned == nil {
		return nil, errors.New("operator withdrawal requires canonical root and actual owner verifier")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyOwned(ctx); err != nil {
		return nil, err
	}
	p := &PreparedEndpointRemoval{root: root, endpoint: endpoint, identity: identity, uid: uint32(uid), gid: uint32(gid), members: map[string]endpointFileID{}, raw: map[string][]byte{}, unlink: unix.Unlinkat, sync: unix.Fsync}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.parent, err = openEndpointDirectory(unix.AT_FDCWD, root, false)
	if err != nil {
		return nil, err
	}
	p.parentID, err = endpointID(int(p.parent.Fd()), "")
	if err != nil {
		return nil, err
	}
	if p.parentID.uid != p.uid || p.parentID.gid != p.gid || p.parentID.mode&0o7022 != 0 {
		return nil, errors.New("operator withdrawal parent is untrusted")
	}
	p.directory, err = openEndpointDirectory(int(p.parent.Fd()), endpoint.Incarnation, true)
	if err != nil {
		return nil, err
	}
	p.dirID, err = endpointID(int(p.directory.Fd()), "")
	if err != nil {
		return nil, err
	}
	if p.dirID.uid != p.uid || p.dirID.gid != p.gid || p.dirID.mode&0o7777 != 0o755 || p.dirID.mount != p.parentID.mount {
		return nil, errors.New("operator withdrawal directory is untrusted")
	}
	names, err := p.names()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		id, err := endpointID(int(p.directory.Fd()), name)
		if err != nil {
			return nil, err
		}
		kind, perm := uint32(unix.S_IFREG), uint32(0o644)
		switch name {
		case "identity.json", "client.json":
		case "control.sock":
			kind, perm = unix.S_IFSOCK, 0o666
		default:
			return nil, errors.New("unexpected operator withdrawal member")
		}
		if id.mode&unix.S_IFMT != kind || id.mode&0o7777 != perm || id.uid != p.uid || id.gid != p.gid || id.links != 1 || id.mount != p.dirID.mount {
			return nil, errors.New("operator withdrawal member is untrusted")
		}
		p.members[name] = id
		if kind == unix.S_IFREG {
			raw, err := p.readMetadata(name, id)
			if err != nil {
				return nil, err
			}
			p.raw[name] = raw
		}
	}
	observed, identityErr := DecodeRegistrationIdentity(p.raw["identity.json"])
	if identityErr != nil || observed != identity {
		return nil, errors.New("operator withdrawal lacks its original identity")
	}
	_, hasClient := p.members["client.json"]
	if profile == endpointCompleteRegistration && !hasClient {
		return nil, errors.New("operator withdrawal lacks original complete registration")
	}
	if profile == endpointIncompleteRegistration && hasClient {
		return nil, errors.New("incomplete operator withdrawal requires originally absent routing metadata")
	}
	if err := p.check(ctx, verifyOwned); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PreparedEndpointRemoval) names() ([]string, error) {
	f, err := openEndpointDirectory(int(p.directory.Fd()), ".", true)
	if err != nil {
		return nil, err
	}
	names, readErr := f.Readdirnames(4)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if len(names) > 3 {
		return nil, errors.New("unexpected operator withdrawal members")
	}
	return names, closeErr
}

func (p *PreparedEndpointRemoval) readMetadata(name string, expected endpointFileID) ([]byte, error) {
	n, err := unix.Openat2(int(p.directory.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(n), "operator metadata")
	defer func() { _ = f.Close() }()
	before, err := endpointID(n, "")
	if err != nil || before != expected {
		return nil, errors.New("operator withdrawal metadata changed before read")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("operator withdrawal metadata is unreadable or oversized")
	}
	after, err := endpointID(n, "")
	if err != nil || after != before {
		return nil, errors.New("operator withdrawal metadata changed during read")
	}
	return raw, nil
}

func (p *PreparedEndpointRemoval) check(ctx context.Context, current func(context.Context) error) error {
	if p == nil || p.closed || current == nil {
		return errors.New("operator withdrawal observation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := current(ctx); err != nil {
		return err
	}
	named, err := openEndpointDirectory(unix.AT_FDCWD, p.root, false)
	if err != nil {
		return err
	}
	id, statErr := endpointID(int(named.Fd()), "")
	closeErr := named.Close()
	if statErr != nil || closeErr != nil || !p.parentID.sameDirectory(id) {
		return errors.New("operator withdrawal parent mapping changed")
	}
	parent, err := endpointID(int(p.parent.Fd()), "")
	if err != nil || !p.parentID.sameDirectory(parent) {
		return errors.New("operator withdrawal retained parent changed")
	}
	dir, err := endpointID(int(p.parent.Fd()), p.endpoint.Incarnation)
	if err != nil || !p.dirID.sameDirectory(dir) {
		return errors.New("operator withdrawal directory mapping changed")
	}
	retained, err := endpointID(int(p.directory.Fd()), "")
	if err != nil || !p.dirID.sameDirectory(retained) {
		return errors.New("operator withdrawal retained directory changed")
	}
	names, err := p.names()
	if err != nil || len(names) != len(p.members) {
		return errors.New("operator withdrawal complete member inventory changed")
	}
	for _, name := range names {
		old, ok := p.members[name]
		id, err := endpointID(int(p.directory.Fd()), name)
		if !ok || err != nil || id != old {
			return errors.New("operator withdrawal original member changed")
		}
		if raw, ok := p.raw[name]; ok {
			current, err := p.readMetadata(name, old)
			if err != nil || !bytes.Equal(current, raw) {
				return errors.New("operator withdrawal original metadata changed")
			}
		}
	}
	return ctx.Err()
}

// Withdraw is a one-use suffix of the original native removal transaction.
// current must independently prove current storage/admission/remaining consumers;
// it cannot be derived from this snapshot. The caller must already hold actual
// Stop, fixed-CID rm and namespace-cleanup acknowledgements. Any error, including
// visible unlink followed by failed sync, poisons this object without retry.
func (p *PreparedEndpointRemoval) Withdraw(ctx context.Context, current func(context.Context) error) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("prepared operator withdrawal requires native VM-root infrastructure")
	}
	return p.withdraw(ctx, current)
}

func (p *PreparedEndpointRemoval) withdraw(ctx context.Context, current func(context.Context) error) error {
	if p == nil {
		return errors.New("operator withdrawal observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.attempted {
		return errors.New("operator withdrawal is closed or already attempted")
	}
	p.attempted = true
	for _, name := range []string{"control.sock", "client.json", "identity.json"} {
		if _, exists := p.members[name]; !exists {
			continue // only a member absent in the ORIGINAL complete observation
		}
		if err := p.check(ctx, current); err != nil {
			return err
		}
		if err := p.unlink(int(p.directory.Fd()), name, 0); err != nil {
			return err
		}
		if err := p.sync(int(p.directory.Fd())); err != nil {
			return err
		}
		delete(p.members, name)
		delete(p.raw, name)
	}
	if err := p.check(ctx, current); err != nil {
		return err
	}
	if err := p.unlink(int(p.parent.Fd()), p.endpoint.Incarnation, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return p.sync(int(p.parent.Fd()))
}

func (p *PreparedEndpointRemoval) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	for _, f := range []*os.File{p.directory, p.parent} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	clear(p.raw)
	clear(p.members)
	return err
}
