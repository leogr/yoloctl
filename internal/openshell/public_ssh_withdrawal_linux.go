package openshell

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// PreparedPublicSSHWithdrawal observes the original public directory/socket
// before lifecycle effects. It is not ownership, a listener/drain proof, or a
// Stop/rm acknowledgement. Use only inside the original removal transaction's
// live Stop/inactive, fixed-CID rm and namespace-completion scope. The caller
// independently proves actual binding, admission, storage and remaining users.
type PreparedPublicSSHWithdrawal struct {
	mu        sync.Mutex
	directory *os.File
	socket    *os.File // O_PATH pins the original inode even after helper Close
	dirID     publicSSHFileIdentity
	socketID  publicSSHFileIdentity
	open      func() (*os.File, error) // repeats the fixed named mapping lookup
	uid, gid  uint32
	attempted bool
	closed    bool
	unlink    func(int, string, int) error
	sync      func(int) error
	lookup    func(int, string) (publicSSHFileIdentity, error)
}

func (*PreparedPublicSSHWithdrawal) String() string   { return "<original public SSH withdrawal>" }
func (*PreparedPublicSSHWithdrawal) GoString() string { return "<original public SSH withdrawal>" }

type publicSSHFileIdentity struct {
	file            pinFileIdentity
	mode            uint16
	uid, gid, links uint32
}

func publicSSHIdentity(fd int, name string) (publicSSHFileIdentity, error) {
	var s unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	const required = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_NLINK | unix.STATX_INO | unix.STATX_MNT_ID
	if err := unix.Statx(fd, name, flags, required, &s); err != nil {
		return publicSSHFileIdentity{}, err
	}
	dev := unix.Mkdev(s.Dev_major, s.Dev_minor)
	if s.Mask&required != required || dev == 0 || s.Ino == 0 || s.Mnt_id == 0 {
		return publicSSHFileIdentity{}, errors.New("public SSH observation lacks complete file/mount identity")
	}
	return publicSSHFileIdentity{pinFileIdentity{dev, s.Ino, s.Mnt_id}, s.Mode, s.Uid, s.Gid, s.Nlink}, nil
}

// PublicSSHWithdrawalResult distinguishes a fresh absence observation from
// this invocation's successful unlink. Neither is historical cleanup evidence.
// Fields are meaningful only with a nil error; both branches require fresh sync
// and repeated original mapping/absence checks. Stream lifetime is separate.
type PublicSSHWithdrawalResult struct {
	ObservedAbsent     bool
	UnlinkAcknowledged bool
	DirectorySynced    bool
}

// PreparePublicSSHWithdrawal must precede Stop. Missing directories refuse;
// only exact absence of ssh.sock in a retained unchanged directory is supported
// here. A cold caller with no original directory needs separate complete proof.
func PreparePublicSSHWithdrawal(ctx context.Context, box string, verifyOwned func(context.Context) error) (*PreparedPublicSSHWithdrawal, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return nil, errors.New("public SSH withdrawal requires native VM-root infrastructure")
	}
	if err := layout.ValidateName("agentbox", box); err != nil {
		return nil, err
	}
	return snapshotPublicSSHWithdrawal(ctx, func() (*os.File, error) {
		// Same host directory as sandbox.SocketDirHost. The existing runtime
		// walker checks every ancestor's owner/writers without following links.
		anchor, err := openRuntimeInfrastructure("/run/yoloctl/" + box)
		if err != nil {
			return nil, err
		}
		defer func() { _ = anchor.Close() }()
		fd, err := unix.Openat2(int(anchor.Fd()), ".", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "public SSH directory"), nil
	}, 0, 0, verifyOwned)
}

func snapshotPublicSSHWithdrawal(ctx context.Context, open func() (*os.File, error), uid, gid int, verifyOwned func(context.Context) error) (_ *PreparedPublicSSHWithdrawal, err error) {
	if open == nil || verifyOwned == nil {
		return nil, errors.New("public SSH observation lacks fixed mapping/actual owner checks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyOwned(ctx); err != nil {
		return nil, err
	}
	p := &PreparedPublicSSHWithdrawal{open: open, uid: uint32(uid), gid: uint32(gid), unlink: unix.Unlinkat, sync: unix.Fsync, lookup: publicSSHIdentity}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.directory, err = open()
	if err != nil {
		return nil, err
	}
	if p.directory == nil {
		return nil, errors.New("public SSH observation lacks original directory")
	}
	p.dirID, err = p.lookup(int(p.directory.Fd()), "")
	if err != nil || !p.validDirectory(p.dirID) {
		return nil, errors.New("public SSH observation has an untrusted directory")
	}
	fd, err := unix.Openat2(int(p.directory.Fd()), "ssh.sock", &unix.OpenHow{Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err == nil {
		p.socket = os.NewFile(uintptr(fd), "original public SSH socket")
		p.socketID, err = p.lookup(fd, "")
		if err != nil || !p.validSocket(p.socketID) {
			return nil, errors.New("public SSH observation has an untrusted socket")
		}
	} else if err != unix.ENOENT {
		return nil, err
	}
	if err := p.checkDirectory(ctx, verifyOwned); err != nil {
		return nil, err
	}
	current, lookupErr := p.lookup(int(p.directory.Fd()), "ssh.sock")
	if p.socket == nil && lookupErr != unix.ENOENT || p.socket != nil && (lookupErr != nil || current != p.socketID) {
		return nil, errors.New("public SSH socket changed during original observation")
	}
	return p, nil
}

func (p *PreparedPublicSSHWithdrawal) validDirectory(i publicSSHFileIdentity) bool {
	return i.mode&unix.S_IFMT == unix.S_IFDIR && i.mode&0o7022 == 0 && i.uid == p.uid && i.gid == p.gid
}
func (p *PreparedPublicSSHWithdrawal) validSocket(i publicSSHFileIdentity) bool {
	return i.mode&unix.S_IFMT == unix.S_IFSOCK && i.mode&0o7777 == 0o600 && i.uid == p.uid && i.gid == p.gid && i.links == 1 && i.file.mount == p.dirID.file.mount
}

func (p *PreparedPublicSSHWithdrawal) checkDirectory(ctx context.Context, current func(context.Context) error) error {
	if p == nil || p.closed || current == nil {
		return errors.New("public SSH observation/current verifier is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := current(ctx); err != nil {
		return err
	}
	retained, err := p.lookup(int(p.directory.Fd()), "")
	if err != nil || !p.validDirectory(retained) || retained.file != p.dirID.file || retained.mode != p.dirID.mode {
		return errors.New("public SSH retained directory changed")
	}
	named, err := p.open()
	if err != nil {
		return err // a missing parent/mapping is NEVER socket absence
	}
	if named == nil {
		return errors.New("public SSH named directory is unavailable")
	}
	id, statErr := p.lookup(int(named.Fd()), "")
	closeErr := named.Close()
	if statErr != nil || closeErr != nil || !p.validDirectory(id) || id.file != p.dirID.file || id.mode != p.dirID.mode {
		return errors.New("public SSH named directory mapping changed")
	}
	return ctx.Err()
}

// Withdraw is one-use, under independently held original transaction ACKs.
// current must remain usable after rm and during intentional metadata deletion;
// it proves actual storage/admission and complete remaining consumers, never
// derives them from this socket snapshot or the removed container's metadata.
func (p *PreparedPublicSSHWithdrawal) Withdraw(ctx context.Context, current func(context.Context) error) (PublicSSHWithdrawalResult, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return PublicSSHWithdrawalResult{}, errors.New("public SSH withdrawal requires native VM-root infrastructure")
	}
	return p.withdraw(ctx, current)
}

func (p *PreparedPublicSSHWithdrawal) withdraw(ctx context.Context, current func(context.Context) error) (PublicSSHWithdrawalResult, error) {
	var result PublicSSHWithdrawalResult
	if p == nil {
		return result, errors.New("public SSH withdrawal observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.attempted {
		return result, errors.New("public SSH withdrawal is closed or already attempted")
	}
	p.attempted = true
	if err := p.checkDirectory(ctx, current); err != nil {
		return result, err
	}
	id, err := p.lookup(int(p.directory.Fd()), "ssh.sock")
	absent := err == unix.ENOENT
	if !absent {
		if err != nil {
			return result, err
		}
		if p.socket == nil || !p.validSocket(id) || id != p.socketID {
			return result, errors.New("public SSH socket differs from original observation")
		}
		if err := p.unlink(int(p.directory.Fd()), "ssh.sock", 0); err != nil {
			return result, err // do not convert ENOENT here to successful absence
		}
	}
	if err := p.sync(int(p.directory.Fd())); err != nil {
		return result, err // no re-sync, even if unlink was visible
	}
	if err := p.checkDirectory(ctx, current); err != nil {
		return result, err
	}
	if _, err := p.lookup(int(p.directory.Fd()), "ssh.sock"); err != unix.ENOENT {
		return result, errors.Join(err, errors.New("public SSH absence postcondition is not established"))
	}
	return PublicSSHWithdrawalResult{ObservedAbsent: absent, UnlinkAcknowledged: !absent, DirectorySynced: true}, nil
}

func (p *PreparedPublicSSHWithdrawal) Close() error {
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
	for _, f := range []*os.File{p.socket, p.directory} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	p.open = nil
	return err
}
