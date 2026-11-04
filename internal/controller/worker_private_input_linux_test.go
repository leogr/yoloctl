package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Ordinary project FIFO/socket fixtures ONLY, never a fake native context,
// namespace/VM anchor or positive original worker/parent receipt proof.
type draftWorkerIntakePair struct {
	payloadFD, checkpointFD int
	writer, peer            *os.File
	writerFD, peerFD        int
	transferred             bool
}

func draftWorkerIntakePairFixture(t *testing.T) *draftWorkerIntakePair {
	t.Helper()
	var pipes [2]int
	if err := unix.Pipe2(pipes[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		_ = unix.Close(pipes[0])
		_ = unix.Close(pipes[1])
		t.Fatal(err)
	}
	p := &draftWorkerIntakePair{payloadFD: pipes[0], checkpointFD: sockets[1], writerFD: pipes[1], peerFD: sockets[0]}
	p.writer = os.NewFile(uintptr(p.writerFD), "fixture-private-payload-writer")
	p.peer = os.NewFile(uintptr(p.peerFD), "fixture-private-checkpoint-peer")
	t.Cleanup(func() {
		if !p.transferred {
			_ = unix.Close(p.payloadFD)
			_ = unix.Close(p.checkpointFD)
		}
		for _, f := range []*os.File{p.writer, p.peer} {
			if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
				_ = f.Close()
			}
		}
	})
	for _, fd := range []int{p.payloadFD, p.checkpointFD} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			t.Fatal(err)
		}
	}
	return p
}
func (p *draftWorkerIntakePair) claim(t *testing.T, ctx context.Context) (*WorkerPrivateIntake, bool, error) {
	t.Helper()
	want := unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid())}
	owned, claimed, err := claimWorkerPrivateIntakeFDs(ctx, p.payloadFD, p.checkpointFD, want, func() int { return os.Getpid() })
	if claimed {
		p.transferred = true
		if owned != nil {
			t.Cleanup(func() { _ = owned.Close() })
		}
	}
	return owned, claimed, err
}
func TestWorkerPrivateIntakeCLOEXECBeforeLoaderChildAndCheckpointBytes(t *testing.T) {
	pair := draftWorkerIntakePairFixture(t)
	p, claimed, err := pair.claim(t, t.Context())
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	// A source fixture models the first loader operation, and actually runs a
	// short ordinary project child; no VM/sandbox/root helper probe is invoked.
	loader := func() error {
		for _, fd := range []int{p.payloadFD, p.checkpointFD} {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC == 0 {
				return errors.New("loader reached before original channels CLOEXEC")
			}
		}
		return exec.CommandContext(t.Context(), "/bin/sh", "-c", fmt.Sprintf("test ! -e /proc/self/fd/%d && test ! -e /proc/self/fd/%d", p.payloadFD, p.checkpointFD)).Run()
	}
	if err := loader(); err != nil {
		t.Fatal(err)
	}
	work := draftPrivateWorkerInput()
	raw, digest, err := CanonicalWorkerInput(work)
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		_, err := pair.writer.Write(append(bytes.Clone(raw), '\n'))
		err = errors.Join(err, pair.writer.Close())
		written <- err
	}()
	got, hash, err := p.Decode(t.Context())
	if err != nil || hash != digest || got.Request.ID != work.Request.ID {
		t.Fatal(hash, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	checkpoint, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	// Intake never consumed a checkpoint prelude/frame. Only the existing
	// protocol owner borrows this exact original stream after intake.
	if _, err := pair.peer.Write([]byte("original-checkpoint")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("original-checkpoint"))
	if _, err := io.ReadFull(checkpoint, buf); err != nil || string(buf) != "original-checkpoint" {
		t.Fatal(string(buf), err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.CheckOriginal() == nil {
		t.Fatal("closed intake recovered")
	}
}
func TestWorkerPrivateIntakeRawRefusalLeavesBothOriginalsUntouched(t *testing.T) {
	for _, kind := range []string{"payload-cloexec", "checkpoint-cloexec", "wrong-creator", "duplicate-slot", "wrong-payload-type", "wrong-checkpoint-type", "wrong-checkpoint-kind", "payload-wrong-access"} {
		t.Run(kind, func(t *testing.T) {
			pair := draftWorkerIntakePairFixture(t)
			payloadFD, checkpointFD := pair.payloadFD, pair.checkpointFD
			var extra *os.File
			if kind == "wrong-payload-type" || kind == "wrong-checkpoint-type" {
				var err error
				extra, err = os.Open("/dev/null")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = extra.Close() }()
				fd := int(extra.Fd())
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
					t.Fatal(err)
				}
				if kind == "wrong-payload-type" {
					payloadFD = fd
				} else {
					checkpointFD = fd
				}
			}
			if kind == "wrong-checkpoint-kind" {
				fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = unix.Close(fds[1]) }()
				extra = os.NewFile(uintptr(fds[0]), "fixture-wrong-socket-kind")
				defer func() { _ = extra.Close() }()
				checkpointFD = fds[0]
				if _, err := unix.FcntlInt(uintptr(checkpointFD), unix.F_SETFD, 0); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "payload-wrong-access" {
				payloadFD = pair.writerFD
				if _, err := unix.FcntlInt(uintptr(payloadFD), unix.F_SETFD, 0); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "payload-cloexec" {
				if _, err := unix.FcntlInt(uintptr(payloadFD), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "checkpoint-cloexec" {
				if _, err := unix.FcntlInt(uintptr(checkpointFD), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "duplicate-slot" {
				checkpointFD = payloadFD
			}
			want := unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid())}
			if kind == "wrong-creator" {
				want.Pid++
			}
			before := map[int]int{}
			for _, fd := range []int{pair.payloadFD, pair.checkpointFD} {
				flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
				if err != nil {
					t.Fatal(err)
				}
				before[fd] = flags
			}
			p, claimed, err := claimWorkerPrivateIntakeFDs(t.Context(), payloadFD, checkpointFD, want, func() int { return int(want.Pid) })
			if err == nil || claimed || p != nil {
				t.Fatal("unrecognized mandatory pair claimed", claimed, err)
			}
			for fd, flags := range before {
				after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
				if err != nil || after != flags {
					t.Fatal("raw refusal closed/changed original", fd, after, err)
				}
			}
		})
	}
}
func TestWorkerPrivateIntakeReadCancellationAndOriginalCause(t *testing.T) {
	for _, kind := range []string{"caller", "original"} {
		t.Run(kind, func(t *testing.T) {
			original, cancelOriginal := context.WithCancelCause(t.Context())
			defer cancelOriginal(nil)
			pair := draftWorkerIntakePairFixture(t)
			p, claimed, err := pair.claim(t, original)
			if err != nil || !claimed {
				t.Fatal(err)
			}
			// Writer deliberately stays open with no complete input. This actual
			// owned nonblocking FIFO read must settle on context Close, not EOF.
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			boom := errors.New("original intake execution canceled")
			if kind == "original" {
				done := make(chan struct{})
				go func() { defer close(done); time.Sleep(10 * time.Millisecond); cancelOriginal(boom) }()
				t.Cleanup(func() { <-done })
			}
			_, digest, err := p.Decode(ctx)
			if err == nil || digest != "" {
				t.Fatal("canceled read returned data")
			}
			if kind == "caller" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if kind == "original" && !errors.Is(err, boom) {
				t.Fatal("original context cause lost", err)
			}
			if p.CheckOriginal() == nil {
				t.Fatal("canceled original intake repaired")
			}
			if _, err := p.Checkpoint(); err == nil {
				t.Fatal("failed intake still supplied checkpoint")
			}
		})
	}
}
func TestWorkerPrivateIntakeMalformedBoundedInputClosesOnlyOwnedChannels(t *testing.T) {
	for _, wire := range [][]byte{[]byte(`{}`), []byte(`{"principal":{},"Principal":{},"request":{}}`), bytes.Repeat([]byte("x"), MaxFrame+1)} {
		pair := draftWorkerIntakePairFixture(t)
		p, claimed, err := pair.claim(t, t.Context())
		if err != nil || !claimed {
			t.Fatal(err)
		}
		// Independent extra FD models an unrecognized optional descriptor. It
		// is never interpreted, wrapped, changed or closed by mandatory intake.
		extra, err := os.Open("/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = extra.Close() }()
		written := make(chan error, 1)
		go func() { _, err := pair.writer.Write(wire); err = errors.Join(err, pair.writer.Close()); written <- err }()
		got, digest, err := p.Decode(t.Context())
		if err == nil || digest != "" || got.Request.ID != "" {
			t.Fatal("malformed payload yielded data")
		}
		<-written
		if _, err := extra.Stat(); err != nil {
			t.Fatal("unrecognized optional descriptor closed", err)
		}
		if err := p.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	}
}
func TestWorkerPrivateIntakeOriginalCheckpointCloseFailureIsSticky(t *testing.T) {
	pair := draftWorkerIntakePairFixture(t)
	p, claimed, err := pair.claim(t, t.Context())
	if err != nil || !claimed {
		t.Fatal(err)
	}
	checkpoint, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Close(); err != nil {
		t.Fatal(err)
	} // negative ownership-violation fixture ONLY
	first := p.Close()
	if !errors.Is(first, os.ErrClosed) {
		t.Fatal(first)
	}
	if err := p.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("original Close failure lost", err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", p, p), "original-checkpoint") {
		t.Fatal("formatter exposed private bytes")
	}
}

func TestWorkerPrivateIntakeTransfersSameCheckpointSoleCloseOwner(t *testing.T) {
	pair := draftWorkerIntakePairFixture(t)
	p, claimed, err := pair.claim(t, t.Context())
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	borrowed, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := p.TakeCheckpoint()
	if err != nil || actual != borrowed {
		t.Fatal("checkpoint object replaced", err)
	}
	if _, err := p.TakeCheckpoint(); err == nil {
		t.Fatal("checkpoint transferred twice")
	}
	if err := p.Close(); err != nil {
		t.Fatal("intake closed transferred checkpoint", err)
	}
	if _, err := actual.Stat(); err != nil {
		t.Fatal("new owner lost original checkpoint", err)
	}
	if err := actual.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("intake closed transferred object twice", err)
	}
}
