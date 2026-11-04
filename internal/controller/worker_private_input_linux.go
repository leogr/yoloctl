package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type workerPrivateInputID struct {
	device, inode  uint64
	kind, uid, gid uint32
}
type workerPrivateInputPair struct {
	payload, checkpoint workerPrivateInputID
	creator             unix.Ucred // PRIMARY creation credentials; NOT boot/source admission
}

// WorkerPrivateIntake owns only the positively recognized mandatory payload
// FIFO and checkpoint stream. It does NOT inspect/claim optional descriptors,
// authenticate VM/SDK origin, authorize a request or alter checkpoint bytes.
// Original parent/process/receipt handoff remains the separate sol3 receiver.
// No namespace/path/numeric constructor is exported; production slots are3/4.
type WorkerPrivateIntake struct {
	mu                         sync.Mutex
	lifetime                   context.Context
	payloadFD, checkpointFD    int
	pair                       workerPrivateInputPair
	payload, checkpoint        *os.File
	owned, readStarted, closed bool
	checkpointTransferred      bool
	failure, closeErr          error
	closeOnce                  sync.Once
}

func (*WorkerPrivateIntake) String() string {
	return "private worker mandatory channel ownership [not admission]"
}
func (p *WorkerPrivateIntake) GoString() string { return p.String() }

func workerPrivateInputContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("private worker input lacks original context")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}
func workerPrivateInputIdentity(fd int, kind uint32) (workerPrivateInputID, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return workerPrivateInputID{}, err
	}
	if st.Mode&unix.S_IFMT != kind || st.Dev == 0 || st.Ino == 0 {
		return workerPrivateInputID{}, errors.New("mandatory worker descriptor has wrong type/identity")
	}
	return workerPrivateInputID{uint64(st.Dev), st.Ino, st.Mode & unix.S_IFMT, st.Uid, st.Gid}, nil
}
func inspectWorkerPrivateInputPair(payloadFD, checkpointFD int, want unix.Ucred, inherited bool) (workerPrivateInputPair, error) {
	var pair workerPrivateInputPair
	if payloadFD < 3 || checkpointFD < 3 || payloadFD == checkpointFD || want.Pid <= 0 {
		return pair, errors.New("invalid mandatory private worker slots")
	}
	var err error
	pair.payload, err = workerPrivateInputIdentity(payloadFD, unix.S_IFIFO)
	if err != nil {
		return pair, err
	}
	pair.checkpoint, err = workerPrivateInputIdentity(checkpointFD, unix.S_IFSOCK)
	if err != nil {
		return pair, err
	}
	for _, entry := range []struct {
		fd, access int
		id         workerPrivateInputID
	}{{payloadFD, unix.O_RDONLY, pair.payload}, {checkpointFD, unix.O_RDWR, pair.checkpoint}} {
		flags, err := unix.FcntlInt(uintptr(entry.fd), unix.F_GETFD, 0)
		if err != nil || (flags&unix.FD_CLOEXEC == 0) != inherited {
			return pair, errors.Join(errors.New("mandatory worker inherited/CLOEXEC state invalid"), err)
		}
		status, err := unix.FcntlInt(uintptr(entry.fd), unix.F_GETFL, 0)
		if err != nil || status&unix.O_ACCMODE != entry.access {
			return pair, errors.Join(errors.New("mandatory worker descriptor access is invalid"), err)
		}
		if entry.id.uid != want.Uid || entry.id.gid != want.Gid {
			return pair, errors.New("mandatory worker descriptor creator ownership differs")
		}
	}
	kind, err := unix.GetsockoptInt(checkpointFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_STREAM {
		return pair, errors.Join(errors.New("mandatory worker checkpoint is not a stream socket"), err)
	}
	domain, err := unix.GetsockoptInt(checkpointFD, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return pair, errors.Join(errors.New("mandatory worker checkpoint is not AF_UNIX"), err)
	}
	peer, err := unix.Getpeername(checkpointFD)
	if err != nil {
		return pair, err
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return pair, errors.New("mandatory checkpoint is not connected AF_UNIX")
	}
	creator, err := unix.GetsockoptUcred(checkpointFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || creator == nil || *creator != want {
		return pair, errors.Join(errors.New("mandatory checkpoint creation peer differs from expected current parent"), err)
	}
	pair.creator = *creator
	return pair, nil
}

// Before the whole raw pair passes, BOTH originals remain untouched. After a
// positive claim, this function owns both even on error and closes owned state.
// Current-parent credential agreement is only PRIMARY channel eligibility;
// it is NOT retained parent/start/boot, original controller source or SDK proof.
func ClaimWorkerPrivateIntake(ctx context.Context) (*WorkerPrivateIntake, bool, error) {
	want := unix.Ucred{Pid: int32(os.Getppid()), Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid())}
	return claimWorkerPrivateIntakeFDs(ctx, 3, 4, want, func() int { return os.Getppid() })
}

// Private ordinary-project FD seam ONLY; not an anchor/admission constructor.
func claimWorkerPrivateIntakeFDs(ctx context.Context, payloadFD, checkpointFD int, want unix.Ucred, parentNow func() int) (_ *WorkerPrivateIntake, claimed bool, result error) {
	if err := workerPrivateInputContext(ctx); err != nil {
		return nil, false, err
	}
	if parentNow == nil || parentNow() != int(want.Pid) {
		return nil, false, errors.New("mandatory worker parent eligibility unavailable")
	}
	before, err := inspectWorkerPrivateInputPair(payloadFD, checkpointFD, want, true)
	if err != nil {
		return nil, false, err
	}
	after, err := inspectWorkerPrivateInputPair(payloadFD, checkpointFD, want, true)
	if err != nil || after != before || parentNow() != int(want.Pid) {
		return nil, false, errors.Join(errors.New("mandatory worker pair changed before claim"), err)
	}
	if err := workerPrivateInputContext(ctx); err != nil {
		return nil, false, err
	}
	claimed = true
	p := &WorkerPrivateIntake{lifetime: ctx, payloadFD: payloadFD, checkpointFD: checkpointFD, pair: before, owned: true}
	defer func() {
		if result != nil {
			result = errors.Join(result, p.Close())
		}
	}()
	for _, fd := range []int{payloadFD, checkpointFD} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			return p, true, err
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
			return p, true, err
		}
	}
	// Owned payload read must be pollable so context Close interrupts a retained
	// writer rather than leaving a blocking read in a kernel pipe indefinitely.
	for _, fd := range []int{payloadFD, checkpointFD} {
		if err := unix.SetNonblock(fd, true); err != nil {
			return p, true, err
		}
	}
	p.payload = os.NewFile(uintptr(payloadFD), "original private worker payload")
	p.checkpoint = os.NewFile(uintptr(checkpointFD), "original private worker checkpoints")
	if p.payload == nil || p.checkpoint == nil {
		return p, true, errors.New("owned mandatory worker descriptors unavailable")
	}
	if err := p.checkLocked(); err != nil {
		return p, true, err
	}
	return p, true, nil
}
func (p *WorkerPrivateIntake) checkLocked() error {
	if p.failure != nil {
		return p.failure
	}
	if p.closed || !p.owned || p.payload == nil || p.checkpoint == nil {
		return errors.New("mandatory worker channel ownership expired/incomplete")
	}
	if err := workerPrivateInputContext(p.lifetime); err != nil {
		return err
	}
	if _, err := p.payload.Stat(); err != nil {
		return err
	}
	if _, err := p.checkpoint.Stat(); err != nil {
		return err
	}
	// Use retained raw slots, not File.Fd(), which can switch a pollable file
	// back to blocking mode and defeat cancellation of an open input writer.
	current, err := inspectWorkerPrivateInputPair(p.payloadFD, p.checkpointFD, p.pair.creator, false)
	if err != nil || current != p.pair {
		return errors.Join(errors.New("original mandatory worker channel changed"), err)
	}
	return nil
}
func (p *WorkerPrivateIntake) CheckOriginal() error {
	if p == nil {
		return errors.New("mandatory worker intake unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		p.failure = err
		return err
	}
	return nil
}
func (p *WorkerPrivateIntake) Checkpoint() (*os.File, error) {
	if p == nil {
		return nil, errors.New("mandatory worker intake unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		p.failure = err
		return nil, err
	}
	return p.checkpoint, nil // BORROWED, never Close here
}

// TakeCheckpoint transfers this SAME already-CLOEXEC File to the existing
// checkpoint protocol exactly once. Intake never wraps FD4 again or closes it
// after transfer. Optional raw handoff recognition must precede this step.
func (p *WorkerPrivateIntake) TakeCheckpoint() (*os.File, error) {
	if p == nil {
		return nil, errors.New("mandatory worker intake unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		p.failure = err
		return nil, err
	}
	p.checkpointTransferred = true
	file := p.checkpoint
	p.checkpoint = nil
	return file, nil
}

// Decode consumes payload bytes only ONCE, never checkpoint/optional bytes.
// Cancellation closes only this object's owned mandatory pair. Original context
// and caller deadline/cause stay mandatory even with a replacement context.
func (p *WorkerPrivateIntake) Decode(ctx context.Context) (work WorkerInput, digest string, result error) {
	if p == nil {
		return WorkerInput{}, "", errors.New("mandatory worker intake unavailable")
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, p.Close())
		}
	}()
	p.mu.Lock()
	if err := p.checkLocked(); err != nil {
		p.failure = err
		p.mu.Unlock()
		return WorkerInput{}, "", err
	}
	if err := workerPrivateInputContext(ctx); err != nil {
		p.failure = err
		p.mu.Unlock()
		return WorkerInput{}, "", err
	}
	if p.readStarted {
		p.failure = errors.New("private worker payload was already consumed")
		err := p.failure
		p.mu.Unlock()
		return WorkerInput{}, "", err
	}
	p.readStarted = true
	file := p.payload
	p.mu.Unlock()
	readCtx, cancel := context.WithCancel(ctx)
	stopOriginal := context.AfterFunc(p.lifetime, cancel)
	closed := make(chan struct{})
	stopClose := context.AfterFunc(readCtx, func() { _ = p.Close(); close(closed) })
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(MaxFrame)+1))
	if !stopClose() {
		<-closed
	}
	stopOriginal()
	readOutcome := workerPrivateInputContext(readCtx) // capture BEFORE local cleanup cancellation
	cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	err := errors.Join(readErr, readOutcome, workerPrivateInputContext(ctx), workerPrivateInputContext(p.lifetime))
	if err == nil {
		err = p.checkLocked()
	}
	if err != nil {
		p.failure = err
		return WorkerInput{}, "", err
	}
	work, digest, err = DecodeWorkerInput(raw)
	if err != nil {
		p.failure = err
		return WorkerInput{}, "", err
	}
	return work, digest, nil
}
func (p *WorkerPrivateIntake) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		owned := p.owned
		payload, checkpoint := p.payload, p.checkpoint
		payloadFD, checkpointFD := p.payloadFD, p.checkpointFD
		checkpointTransferred := p.checkpointTransferred
		p.mu.Unlock()
		var err error
		if owned {
			if payload != nil {
				err = errors.Join(err, payload.Close())
			} else {
				err = errors.Join(err, unix.Close(payloadFD))
			}
			if !checkpointTransferred {
				if checkpoint != nil {
					err = errors.Join(err, checkpoint.Close())
				} else {
					err = errors.Join(err, unix.Close(checkpointFD))
				}
			}
		}
		p.mu.Lock()
		p.closeErr = err
		p.mu.Unlock()
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeErr
}
