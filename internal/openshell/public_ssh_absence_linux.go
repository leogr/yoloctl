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

// This stamp describes only the admitted CURRENT kernel. Historical boot
// classification belongs to independently bound immutable origin evidence;
// neither a missing /run directory nor this object establishes that relation.
type publicSSHKernelStamp struct {
	boot           string
	mount, network namespaceIdentity
}

func (s publicSSHKernelStamp) valid() bool {
	return canonicalUUID(s.boot) && s.mount.Device != 0 && s.mount.Inode != 0 && s.network.Device != 0 && s.network.Inode != 0
}

func publicSSHKernelObserver(k *kernelInfrastructure) func(context.Context) (publicSSHKernelStamp, error) {
	return func(ctx context.Context) (publicSSHKernelStamp, error) {
		if k == nil {
			return publicSSHKernelStamp{}, errors.New("cold SSH observation lacks admitted kernel scope")
		}
		if err := k.revalidate(ctx); err != nil {
			return publicSSHKernelStamp{}, err
		}
		return publicSSHKernelStamp{k.bootID, k.mountID, k.networkID}, nil
	}
}

// PreparedPublicSSHDirectoryAbsence is a separate original-call branch for an
// already-INACTIVE container whose fixed public directory is initially absent.
// It cannot replace an existing-directory observation after effects. Actual
// owner/inactivity/consumer/stream proofs and live Stop/rm/namespace ACKs remain
// independent; this is not an old socket inode, reboot proof or cleanup receipt.
type PreparedPublicSSHDirectoryAbsence struct {
	mu        sync.Mutex
	parent    *os.File
	parentID  publicSSHFileIdentity
	box       string
	open      func() (*os.File, error)
	uid, gid  uint32
	kernel    publicSSHKernelStamp
	attempted bool
	closed    bool
	lookup    func(int, string) (publicSSHFileIdentity, error)
	sync      func(int) error
}

func (*PreparedPublicSSHDirectoryAbsence) String() string {
	return "<original public SSH directory absence>"
}
func (*PreparedPublicSSHDirectoryAbsence) GoString() string {
	return "<original public SSH directory absence>"
}

type PublicSSHDirectoryAbsenceResult struct {
	ObservedDirectoryAbsent bool
	ParentSynced            bool
}

// PreparePublicSSHDirectoryAbsence requires explicit initial actual inactivity
// in addition to fixed-CID/runtime/storage admission, before any lifecycle
// effects. The root/boot/PID1 namespace checks are the existing real reader.
func PreparePublicSSHDirectoryAbsence(ctx context.Context, box string, verifyOwned, verifyInactive func(context.Context) error) (_ *PreparedPublicSSHDirectoryAbsence, err error) {
	var p *PreparedPublicSSHDirectoryAbsence
	err = withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		var captureErr error
		p, captureErr = snapshotPublicSSHDirectoryAbsence(ctx, openPublicSSHParent, box, 0, 0, verifyOwned, verifyInactive, publicSSHKernelObserver(k))
		return captureErr
	})
	if err != nil {
		if p != nil {
			err = errors.Join(err, p.Close())
		}
		return nil, err
	}
	return p, nil
}

func openPublicSSHParent() (*os.File, error) {
	anchor, err := openRuntimeInfrastructure("/run/yoloctl")
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchor.Close() }()
	fd, err := unix.Openat2(int(anchor.Fd()), ".", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "public SSH parent"), nil
}

func snapshotPublicSSHDirectoryAbsence(ctx context.Context, open func() (*os.File, error), box string, uid, gid int, verifyOwned, verifyInactive func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (_ *PreparedPublicSSHDirectoryAbsence, err error) {
	if err := layout.ValidateName("agentbox", box); err != nil {
		return nil, err
	}
	return snapshotFixedPublicDirectoryAbsence(ctx, open, box, uid, gid, verifyOwned, verifyInactive, observeKernel)
}

// Only the separate operator-root wrapper supplies the literal reserved
// operators component. SSH construction above still requires a valid box,
// including its reserved-name refusals. No arbitrary path/prefix is accepted.
// The historical box field in the private backing object stores this child
// component only; it never supplies container/CID or historical authority.
func snapshotFixedPublicDirectoryAbsence(ctx context.Context, open func() (*os.File, error), box string, uid, gid int, verifyOwned, verifyInactive func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (_ *PreparedPublicSSHDirectoryAbsence, err error) {
	if box != "operators" && layout.ValidateName("agentbox", box) != nil {
		return nil, errors.New("cold directory absence requires a validated fixed child")
	}
	if open == nil || verifyOwned == nil || verifyInactive == nil || observeKernel == nil {
		return nil, errors.New("cold SSH observation lacks actual owner/inactivity/current kernel checks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kernel, err := observeKernel(ctx)
	if err != nil {
		return nil, err
	}
	if !kernel.valid() {
		return nil, errors.New("cold SSH observation lacks complete current boot/namespace identity")
	}
	initial := func(ctx context.Context) error {
		if err := verifyOwned(ctx); err != nil {
			return err
		}
		return verifyInactive(ctx)
	}
	if err := initial(ctx); err != nil {
		return nil, err
	}
	p := &PreparedPublicSSHDirectoryAbsence{open: open, box: box, uid: uint32(uid), gid: uint32(gid), kernel: kernel, lookup: publicSSHIdentity, sync: unix.Fsync}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.parent, err = open()
	if err != nil {
		return nil, err
	}
	if p.parent == nil {
		return nil, errors.New("cold SSH observation lacks original protected parent")
	}
	p.parentID, err = p.lookup(int(p.parent.Fd()), "")
	if err != nil || !p.validParent(p.parentID) {
		return nil, errors.New("cold SSH parent is untrusted")
	}
	if err := p.check(ctx, initial, observeKernel); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PreparedPublicSSHDirectoryAbsence) validParent(i publicSSHFileIdentity) bool {
	return i.mode&unix.S_IFMT == unix.S_IFDIR && i.mode&0o7022 == 0 && i.uid == p.uid && i.gid == p.gid
}

func (p *PreparedPublicSSHDirectoryAbsence) checkKernel(ctx context.Context, observe func(context.Context) (publicSSHKernelStamp, error)) error {
	current, err := observe(ctx)
	if err != nil {
		return err
	}
	if !current.valid() || current != p.kernel {
		return errors.New("cold SSH current boot/infrastructure namespace binding changed")
	}
	return nil
}

func (p *PreparedPublicSSHDirectoryAbsence) check(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) error {
	if p == nil || p.closed || p.parent == nil || current == nil || observeKernel == nil {
		return errors.New("cold SSH observation/current proof is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.checkKernel(ctx, observeKernel); err != nil {
		return err
	}
	if err := current(ctx); err != nil {
		return err
	}
	retained, err := p.lookup(int(p.parent.Fd()), "")
	if err != nil || !p.validParent(retained) || retained.file != p.parentID.file || retained.mode != p.parentID.mode {
		return errors.New("cold SSH retained parent changed")
	}
	named, err := p.open()
	if err != nil {
		return err
	}
	if named == nil {
		return errors.New("cold SSH named parent is unavailable")
	}
	id, statErr := p.lookup(int(named.Fd()), "")
	closeErr := named.Close()
	if statErr != nil || closeErr != nil || !p.validParent(id) || id.file != p.parentID.file || id.mode != p.parentID.mode {
		return errors.New("cold SSH named parent mapping changed")
	}
	// Exact no-follow lookup of the validated single component. A present
	// child of ANY kind, or a joined/unrelated error, is not this branch.
	if _, err := p.lookup(int(p.parent.Fd()), p.box); err != unix.ENOENT {
		return errors.Join(err, errors.New("cold SSH fixed directory absence is not established"))
	}
	if err := p.checkKernel(ctx, observeKernel); err != nil {
		return err
	}
	return ctx.Err()
}

// Withdraw belongs only to the original live Stop/inactive/full-CID rm and
// namespace-completion suffix. current proves complete actual admission,
// storage, remaining consumers/streams/projections; it must not derive this
// proof from pathname absence or the removed CID. Historical boot relation, if
// needed elsewhere, still requires independent bound immutable origin evidence.
func (p *PreparedPublicSSHDirectoryAbsence) Withdraw(ctx context.Context, current func(context.Context) error) (PublicSSHDirectoryAbsenceResult, error) {
	var result PublicSSHDirectoryAbsenceResult
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return result, errors.New("cold SSH withdrawal requires admitted native VM-root infrastructure")
	}
	if p == nil {
		return result, errors.New("cold SSH observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(); err != nil {
		return result, err
	}
	// Consume BEFORE the real kernel read too; failed kernel admission cannot
	// leave a native attempt reusable. Ordinary executors are rejected above.
	err := withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		var err error
		result, err = p.withdrawOnce(ctx, current, publicSSHKernelObserver(k))
		return err
	})
	if err != nil {
		return PublicSSHDirectoryAbsenceResult{}, err
	}
	return result, nil
}

func (p *PreparedPublicSSHDirectoryAbsence) withdraw(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (PublicSSHDirectoryAbsenceResult, error) {
	var result PublicSSHDirectoryAbsenceResult
	if p == nil {
		return result, errors.New("cold SSH observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(); err != nil {
		return result, err
	}
	return p.withdrawOnce(ctx, current, observeKernel)
}

// Caller holds mu; no syscall or current-state check precedes consumption.
func (p *PreparedPublicSSHDirectoryAbsence) begin() error {
	if p.closed || p.attempted {
		return errors.New("cold SSH attempt is closed or consumed")
	}
	p.attempted = true
	return nil
}

func (p *PreparedPublicSSHDirectoryAbsence) withdrawOnce(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (PublicSSHDirectoryAbsenceResult, error) {
	var result PublicSSHDirectoryAbsenceResult
	if p == nil || p.closed || !p.attempted {
		return result, errors.New("cold SSH suffix lacks original consumed attempt")
	}
	if err := p.check(ctx, current, observeKernel); err != nil {
		return result, err
	}
	if err := p.sync(int(p.parent.Fd())); err != nil {
		return result, err
	}
	if err := p.check(ctx, current, observeKernel); err != nil {
		return result, err
	}
	return PublicSSHDirectoryAbsenceResult{ObservedDirectoryAbsent: true, ParentSynced: true}, nil
}

func (p *PreparedPublicSSHDirectoryAbsence) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.open = nil
	if p.parent != nil {
		return p.parent.Close()
	}
	return nil
}
