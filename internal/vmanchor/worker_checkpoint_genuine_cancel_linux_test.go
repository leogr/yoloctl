//go:build linux

package vmanchor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/workercheckpoint"
	"golang.org/x/sys/unix"
)

// This extends the EXISTING original test-parent/controller/worker harness.
// WorkerHandoff is obtained ONLY from the real authenticated private producer/
// receiver over retained kernel pins; no struct/token/NativeCtx is fabricated.
// Manual private wire replies below test mechanics ONLY: they are NOT a real
// controller receipt/SDK/VM launch/current-storage/native effect qualification.
func TestWorkerHandoffCheckpointCancellation(t *testing.T) {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("genuine checkpoint creation-peer binding requires appropriate root project executor; no positive coverage here")
	}
	for _, mode := range []string{"checkpoint-waiter", "checkpoint-active-cancel"} {
		t.Run(mode, func(t *testing.T) {
			source := fixtureSource(t) //existing EPERM/ENOSYS limit is an explicit SKIP
			transfer, err := source.PrepareLaunch()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transfer.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerHandoffControllerChild$")
			cmd.Env = append(os.Environ(), "YOLOCTL_WORKER_HANDOFF_CONTROLLER="+mode)
			cmd.ExtraFiles = transfer.ChildFiles()
			var diagnostic workerFixtureDiagnostics
			cmd.Stdout, cmd.Stderr = io.Discard, &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := transfer.PublishStarted(cmd.Process); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("genuine checkpoint %s: %v; diagnostics: %s", mode, err, diagnostic.raw)
			}
			if ctx.Err() != nil {
				t.Fatal("project fixture deadline elapsed", ctx.Err())
			}
		})
	}
}
func workerCheckpointFixtureMode(mode string) bool {
	return mode == "checkpoint-waiter" || mode == "checkpoint-active-cancel"
}
func workerCheckpointFixtureViewPair(mode string) (*os.File, *os.File, error) {
	if !workerCheckpointFixtureMode(mode) {
		return workerFixtureSocket()
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(pair[0]), "checkpoint-fixture-parent-view"), os.NewFile(uintptr(pair[1]), "checkpoint-fixture-child-view"), nil
}
func workerCheckpointFixtureWrite(file *os.File, raw []byte) error {
	for len(raw) > 0 {
		n, err := file.Write(raw)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}
func workerCheckpointFixtureReadFrame(file *os.File, kind byte, expected []byte) error {
	var header [5]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return err
	}
	if header[0] != kind {
		return fmt.Errorf("checkpoint fixture opcode=%d want=%d", header[0], kind)
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size == 0 || size > workercheckpoint.MaxFrame {
		return errors.New("invalid checkpoint fixture length")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(file, raw); err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return fmt.Errorf("checkpoint fixture body changed: opcode=%d", kind)
	}
	return nil
}
func workerCheckpointFixtureReply(file *os.File, raw []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	return workerCheckpointFixtureWrite(file, append(header[:], raw...))
}
func workerCheckpointFixtureControl(file *os.File, want byte) error {
	var message [1]byte
	if _, err := io.ReadFull(file, message[:]); err != nil {
		return err
	}
	if message[0] != want {
		return fmt.Errorf("checkpoint fixture control=%d want=%d", message[0], want)
	}
	return nil
}

// Parent-side fixture called only after PublishStarted and canonical payload
// delivery, with its child-side CHECKPOINT and VIEW copies already closed.
// View is the EXISTING authenticated optional infrastructure slot, scheduling
// only; physical protocol always runs through original checkpoint socket.
func serveWorkerCheckpointCancellationFixture(checkpoint, view *os.File, mode string) error {
	if view == nil {
		return errors.New("checkpoint fixture lacks original view scheduler")
	}
	if err := view.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err := workerCheckpointFixtureReadFrame(checkpoint, 2, []byte(`{}`)); err != nil {
		return err
	}
	if err := workerCheckpointFixtureWrite(checkpoint, []byte{3}); err != nil {
		return err
	}
	if mode == "checkpoint-waiter" {
		if err := workerCheckpointFixtureReadFrame(checkpoint, 13, []byte(`{"fixture":"active"}`)); err != nil {
			return err
		}
		if err := workerCheckpointFixtureWrite(view, []byte{'I'}); err != nil {
			return err
		} //actual Image bytes reached parent
		if err := workerCheckpointFixtureControl(view, 'C'); err != nil {
			return err
		} //queued caller canceled; no Image result yet
		if err := workerCheckpointFixtureReply(checkpoint, []byte(`{}`)); err != nil {
			return err
		}
		if err := workerCheckpointFixtureReadFrame(checkpoint, 16, []byte(`{"box":"box"}`)); err != nil {
			return err
		}
		if err := workerCheckpointFixtureReply(checkpoint, []byte(`{}`)); err != nil {
			return err
		}
		return workerCheckpointFixtureControl(checkpoint, 5) //ordinary Finish survives local cancellation
	}
	if mode != "checkpoint-active-cancel" {
		return errors.New("unknown checkpoint fixture mode")
	}
	if err := workerCheckpointFixtureReadFrame(checkpoint, 16, []byte(`{"box":"box"}`)); err != nil {
		return err
	}
	if err := workerCheckpointFixtureWrite(view, []byte{'A'}); err != nil {
		return err
	} //real16 request fully written, reply withheld
	if err := workerCheckpointFixtureControl(view, 'D'); err != nil {
		return err
	} //active cancel settled +token sticky
	var message [1]byte
	n, err := checkpoint.Read(message[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return fmt.Errorf("active canceled original did not close/no-retry: n=%d err=%v", n, err)
	}
	return nil
}

// Context wrapper schedules a REAL standard context cancellation. It supplies
// no process/namespace/context-configuration/source fact or success callback.
// CheckParent calls Err once at entry; Observe.acquire is its SECOND check.
// Capture nil before signaling: the parent still withholds Image reply, so that
// acquire has passed precheck and can only wait for physical gate or cancellation.
type workerCheckpointWaitContext struct {
	context.Context
	checks atomic.Int32
	atGate chan struct{}
}

func (c *workerCheckpointWaitContext) Err() error {
	err := c.Context.Err()
	if c.checks.Add(1) == 2 {
		close(c.atGate)
	}
	return err
}
func runWorkerCheckpointCancellationFixture(w *WorkerHandoff, checkpoint *os.File, request WorkerHandoffRequest, mode string) (result error) {
	if w == nil || !workerCheckpointFixtureMode(mode) || request.ViewFD != 5 {
		return errors.New("checkpoint fixture lacks genuine original token/view slot")
	}
	// Exact SAME inherited mandatory File object goes to one owner ONCE. Caller
	// must not wrap fd4 again or retain another checkpoint Close owner.
	channel := workercheckpoint.Take(checkpoint)
	defer func() { result = errors.Join(result, channel.Close(), w.Close()) }()
	if _, err := unix.FcntlInt(uintptr(request.ViewFD), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return err
	}
	if err := unix.SetNonblock(request.ViewFD, true); err != nil {
		return err
	}
	view := os.NewFile(uintptr(request.ViewFD), "genuine-checkpoint-fixture-view")
	defer func() { result = errors.Join(result, view.Close()) }()
	if err := view.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.BindCheckpoint(ctx, channel); err != nil {
		return err
	}
	if err := w.Check(); err != nil {
		return err
	}
	channel.Gate()
	if err := channel.Begin(ctx, struct{}{}); err != nil {
		return err
	}
	if mode == "checkpoint-waiter" {
		imageDone := make(chan error, 1)
		go func() {
			raw, err := channel.Image(ctx, map[string]string{"fixture": "active"})
			if err == nil && !bytes.Equal(raw, []byte(`{}`)) {
				err = errors.New("active Image reply changed")
			}
			imageDone <- err
		}()
		if err := workerCheckpointFixtureControl(view, 'I'); err != nil {
			return err
		}
		local, cancelLocal := context.WithCancelCause(ctx)
		defer cancelLocal(nil)
		queued := &workerCheckpointWaitContext{Context: local, atGate: make(chan struct{})}
		waiterDone := make(chan error, 1)
		go func() { waiterDone <- w.CheckParent(queued, "box") }()
		select {
		case <-queued.atGate:
		case <-ctx.Done():
			return ctx.Err()
		}
		cause := errors.New("genuine queued checkpoint caller canceled")
		cancelLocal(cause)
		var waiterErr error
		select {
		case waiterErr = <-waiterDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !errors.Is(waiterErr, context.Canceled) || !errors.Is(waiterErr, cause) || !workercheckpoint.IsLocalCancellation(waiterErr) {
			return fmt.Errorf("queued cancellation classification/cause lost: %v", waiterErr)
		}
		if err := w.Check(); err != nil {
			return fmt.Errorf("canceled waiter poisoned genuine SAME handoff: %w", err)
		}
		select {
		case err := <-imageDone:
			return fmt.Errorf("canceled waiter touched active Image: %v", err)
		default:
		}
		if err := workerCheckpointFixtureWrite(view, []byte{'C'}); err != nil {
			return err
		}
		select {
		case err := <-imageDone:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		// SAME actual once-bound w/channel and real16 request/reply, no replacement,
		// caller-provided success closure, namespace factory or native Context grant.
		if err := w.CheckParent(ctx, "box"); err != nil {
			return fmt.Errorf("genuine handoff reuse after local cancellation failed: %w", err)
		}
		if err := w.Check(); err != nil {
			return err
		}
		return channel.Finish(false)
	}
	// Obtain a genuine prior local tag without active protocol; a caller can retain
	// this error and reuse it as a context CAUSE. It must not relabel active cancel.
	oldCtx, cancelOld := context.WithCancelCause(ctx)
	oldCause := errors.New("older local cancellation")
	cancelOld(oldCause)
	_, oldLocal := channel.Observe(oldCtx)
	if !workercheckpoint.IsLocalCancellation(oldLocal) {
		return errors.New("fixture could not retain real prior local cancellation")
	}
	active, cancelActive := context.WithCancelCause(ctx)
	defer cancelActive(nil)
	activeDone := make(chan error, 1)
	go func() { activeDone <- w.CheckParent(active, "box") }()
	if err := workerCheckpointFixtureControl(view, 'A'); err != nil {
		return err
	}
	cancelActive(oldLocal) //actual16 is already on wire; recycled private marker is only its cause
	var activeErr error
	select {
	case activeErr = <-activeDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !errors.Is(activeErr, context.Canceled) || !errors.Is(activeErr, oldCause) || workercheckpoint.IsLocalCancellation(activeErr) {
		return fmt.Errorf("active canceled exchange was mislabeled local: %v", activeErr)
	}
	if err := w.Check(); err == nil {
		return errors.New("active cancel repaired genuine original token")
	}
	if _, err := channel.Observe(ctx); err == nil {
		return errors.New("active canceled physical owner revived")
	}
	if err := w.CheckParent(ctx, "box"); err == nil {
		return errors.New("active canceled source replayed/reused parent exchange")
	}
	if err := channel.Finish(true); err == nil {
		return errors.New("closed original stream reported successful Finish")
	}
	return workerCheckpointFixtureWrite(view, []byte{'D'})
}
