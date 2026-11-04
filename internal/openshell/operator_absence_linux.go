package openshell

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// PreparedOperatorEndpointAbsence is an original, pre-effect observation for
// an INACTIVE container's selected operator registration. Actual fixed-CID /
// label / read-only OCI projection ownership is independently required from
// verifyOwned. Neither this selection nor a missing selected registration
// discharges older same-box registrations or the legacy complete/keep roster.
type PreparedOperatorEndpointAbsence struct {
	mu         sync.Mutex
	base, root *os.File
	baseID     publicSSHFileIdentity
	rootID     publicSSHFileIdentity
	endpoint   operator.Endpoint
	rootAbsent bool
	openBase   func() (*os.File, error)
	uid, gid   uint32
	kernel     publicSSHKernelStamp // genuine CURRENT boot/PID1 namespaces only
	attempted  bool
	closed     bool
	captured   bool
	lookup     func(int, string) (publicSSHFileIdentity, error)
	sync       func(int) error
}

func (*PreparedOperatorEndpointAbsence) String() string {
	return "<original operator registration absence>"
}
func (p *PreparedOperatorEndpointAbsence) GoString() string { return p.String() }

type OperatorEndpointAbsenceResult struct {
	ObservedRegistrationRootAbsent     bool
	ObservedSelectedRegistrationAbsent bool
	ParentSynced                       bool
}

// PrepareOperatorEndpointAbsence chooses exactly one initially absent branch:
// fixed operators prefix, or selected incarnation beneath the original root.
// A present selected registration, including a partial one, is not absence.
// Actual full-CID/box/label/OCI/storage admission and explicit inactivity must
// precede effects; root/boot/PID1 observations never provide that authority.
func PrepareOperatorEndpointAbsence(ctx context.Context, endpoint operator.Endpoint, verifyOwned, verifyInactive func(context.Context) error) (_ *PreparedOperatorEndpointAbsence, err error) {
	var p *PreparedOperatorEndpointAbsence
	err = withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		var captureErr error
		p, captureErr = snapshotOperatorEndpointAbsence(ctx, openPublicSSHParent, endpoint, 0, 0, verifyOwned, verifyInactive, publicSSHKernelObserver(k))
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

func snapshotOperatorEndpointAbsence(ctx context.Context, openBase func() (*os.File, error), endpoint operator.Endpoint, uid, gid int, verifyOwned, verifyInactive func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (_ *PreparedOperatorEndpointAbsence, err error) {
	if endpoint.Validate() != nil || openBase == nil || verifyOwned == nil || verifyInactive == nil || observeKernel == nil {
		return nil, errors.New("cold operator observation lacks original selection/owner/inactivity/kernel checks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stamp, err := observeKernel(ctx)
	if err != nil || !stamp.valid() {
		return nil, errors.Join(err, errors.New("cold operator observation lacks current boot/namespace binding"))
	}
	initial := func(c context.Context) error {
		if err := verifyOwned(c); err != nil {
			return err
		}
		return verifyInactive(c)
	}
	if err := initial(ctx); err != nil {
		return nil, err
	}
	p := &PreparedOperatorEndpointAbsence{openBase: openBase, endpoint: endpoint, uid: uint32(uid), gid: uint32(gid), kernel: stamp, lookup: publicSSHIdentity, sync: unix.Fsync}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.base, err = openBase()
	if err != nil || p.base == nil {
		return nil, errors.Join(err, errors.New("cold operator protected parent is unavailable"))
	}
	p.baseID, err = p.lookup(int(p.base.Fd()), "")
	if err != nil || !p.trusted(p.baseID) {
		return nil, errors.New("cold operator protected parent is untrusted")
	}
	// This exact lookup selects the branch ONCE, retaining the initial base.
	// A joined/wrapped error is not an observation of missing operators.
	rootID, lookupErr := p.lookup(int(p.base.Fd()), "operators")
	if lookupErr == unix.ENOENT {
		p.rootAbsent = true
	} else if lookupErr != nil {
		return nil, lookupErr
	} else {
		if !p.trusted(rootID) {
			return nil, errors.New("cold operator existing registration root is untrusted")
		}
		fd, openErr := unix.Openat2(int(p.base.Fd()), "operators", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if openErr != nil {
			return nil, openErr
		}
		p.root = os.NewFile(uintptr(fd), "original operator registration root")
		p.rootID, err = p.lookup(fd, "")
		if err != nil || !p.same(rootID, p.rootID) {
			return nil, errors.New("cold operator root changed while opening")
		}
	}
	p.captured = true
	if err := p.check(ctx, initial, observeKernel); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PreparedOperatorEndpointAbsence) trusted(i publicSSHFileIdentity) bool {
	return i.mode&unix.S_IFMT == unix.S_IFDIR && i.mode&0o7022 == 0 && i.uid == p.uid && i.gid == p.gid
}

func (p *PreparedOperatorEndpointAbsence) same(a, b publicSSHFileIdentity) bool {
	// Sibling publication may change directory size/timestamps/link count.
	return p.trusted(a) && p.trusted(b) && a.file == b.file && a.mode == b.mode
}

func (p *PreparedOperatorEndpointAbsence) check(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) error {
	if p == nil || p.closed || !p.captured || p.base == nil || p.openBase == nil || current == nil || observeKernel == nil || p.endpoint.Validate() != nil || p.rootAbsent == (p.root != nil) {
		return errors.New("cold operator original observation/current proof is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stamp, err := observeKernel(ctx)
	if err != nil || !stamp.valid() || stamp != p.kernel {
		return errors.Join(err, errors.New("cold operator current boot/namespace binding changed"))
	}
	if err := current(ctx); err != nil {
		return err
	}
	retained, err := p.lookup(int(p.base.Fd()), "")
	if err != nil || !p.same(p.baseID, retained) {
		return errors.New("cold operator retained parent changed")
	}
	named, err := p.openBase()
	if err != nil || named == nil {
		return errors.Join(err, errors.New("cold operator named parent is unavailable"))
	}
	namedID, statErr := p.lookup(int(named.Fd()), "")
	closeErr := named.Close()
	if statErr != nil || closeErr != nil || !p.same(p.baseID, namedID) {
		return errors.New("cold operator named parent mapping changed")
	}
	if p.rootAbsent {
		if _, err := p.lookup(int(p.base.Fd()), "operators"); err != unix.ENOENT {
			return errors.Join(err, errors.New("cold operator fixed root absence is not established"))
		}
	} else {
		namedID, err := p.lookup(int(p.base.Fd()), "operators")
		if err != nil || !p.same(p.rootID, namedID) {
			return errors.New("cold operator original root mapping changed")
		}
		retainedID, err := p.lookup(int(p.root.Fd()), "")
		if err != nil || !p.same(p.rootID, retainedID) {
			return errors.New("cold operator retained root changed")
		}
		if _, err := p.lookup(int(p.root.Fd()), p.endpoint.Incarnation); err != unix.ENOENT {
			return errors.Join(err, errors.New("cold operator selected registration absence is not established"))
		}
	}
	stamp, err = observeKernel(ctx)
	if err != nil || !stamp.valid() || stamp != p.kernel {
		return errors.Join(err, errors.New("cold operator current kernel changed during observation"))
	}
	return ctx.Err()
}

// Withdraw is only the original live Stop/inactivity/full-CID rm/namespace
// completion suffix. current independently proves actual admission/storage,
// complete remaining consumers/listeners/accepted streams/projections. The
// caller cannot infer that proof, a historical boot or an unlink ACK from
// absence. Existing-present loss cannot select this branch after effects.
func (p *PreparedOperatorEndpointAbsence) Withdraw(ctx context.Context, current func(context.Context) error) (OperatorEndpointAbsenceResult, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return OperatorEndpointAbsenceResult{}, errors.New("cold operator withdrawal requires admitted native VM-root infrastructure")
	}
	if p == nil {
		return OperatorEndpointAbsenceResult{}, errors.New("cold operator observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(); err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	var result OperatorEndpointAbsenceResult
	err := withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		var err error
		result, err = p.withdrawOnce(ctx, current, publicSSHKernelObserver(k))
		return err
	})
	if err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	return result, nil
}

func (p *PreparedOperatorEndpointAbsence) begin() error {
	if p.closed || p.attempted {
		return errors.New("cold operator attempt is closed or consumed")
	}
	p.attempted = true // before every check, including genuine kernel admission
	return nil
}

func (p *PreparedOperatorEndpointAbsence) withdraw(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (OperatorEndpointAbsenceResult, error) {
	if p == nil {
		return OperatorEndpointAbsenceResult{}, errors.New("cold operator observation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(); err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	return p.withdrawOnce(ctx, current, observeKernel)
}

func (p *PreparedOperatorEndpointAbsence) withdrawOnce(ctx context.Context, current func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (OperatorEndpointAbsenceResult, error) {
	if p == nil || p.closed || !p.attempted {
		return OperatorEndpointAbsenceResult{}, errors.New("cold operator suffix lacks original consumed attempt")
	}
	if err := p.check(ctx, current, observeKernel); err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	parent := p.base
	if !p.rootAbsent {
		parent = p.root
	}
	if err := p.sync(int(parent.Fd())); err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	if err := p.check(ctx, current, observeKernel); err != nil {
		return OperatorEndpointAbsenceResult{}, err
	}
	return OperatorEndpointAbsenceResult{ObservedRegistrationRootAbsent: p.rootAbsent, ObservedSelectedRegistrationAbsent: !p.rootAbsent, ParentSynced: true}, nil
}

func (p *PreparedOperatorEndpointAbsence) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.openBase = nil
	var err error
	for _, file := range []*os.File{p.root, p.base} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	return err
}
