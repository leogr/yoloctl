package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These exercise the ACTUAL admission primitive with owned counters/channels.
// They never Configure NativeCtx, construct LaunchAnchor or grant VM admission.
func TestControllerSourceCanceledWaiterCannotTouchActiveBorrow(t *testing.T) {
	var gate controllerSourceAdmission
	release, err := gate.acquire(t.Context(), t.Context())
	if err != nil {
		t.Fatal(err)
	}
	active := atomic.Bool{}
	active.Store(true)
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("original queued caller canceled")
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		own, err := gate.acquire(ctx, t.Context())
		if own != nil {
			own()
			done <- errors.New("canceled waiter acquired active token")
			return
		}
		done <- err
	}()
	<-started
	cancel(cause)
	if err := <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatal("queued caller cancellation lost", err)
	}
	if !active.Load() {
		t.Fatal("waiter touched unrelated active borrow")
	}
	select {
	case <-gate.token:
		gate.token <- struct{}{}
		t.Fatal("canceled waiter released the ACTIVE borrow token")
	default:
	}
	select {
	case <-gate.stopping:
		t.Fatal("waiter closed source admission")
	default:
	}
	release()
	active.Store(false)
	// Cancellation of one request cannot poison this original service source.
	next, err := gate.acquire(t.Context(), t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next()
	if err := gate.closeOwned(nil); err != nil {
		t.Fatal(err)
	}
}
func TestControllerSourceCloseStopsAdmissionAndWaitsActualBorrowSettlement(t *testing.T) {
	var gate controllerSourceAdmission
	release, err := gate.acquire(t.Context(), t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closed := atomic.Int32{}
	done := make(chan error, 1)
	closeErr := errors.New("owned observer close result")
	go func() { done <- gate.closeOwned(func() error { closed.Add(1); return closeErr }) }()
	<-gate.stopping // deterministic Close-started barrier
	if closed.Load() != 0 {
		t.Fatal("Close touched own observer before actual borrower settled")
	}
	own, err := gate.acquire(t.Context(), t.Context())
	if err == nil || own != nil {
		t.Fatal("Close admitted another borrower")
	}
	select {
	case <-done:
		t.Fatal("Close settled while original borrow remained active")
	default:
	}
	release()
	if err := <-done; !errors.Is(err, closeErr) || closed.Load() != 1 {
		t.Fatal("owned close missing/result lost", err)
	}
	if err := gate.closeOwned(func() error { closed.Add(1); return nil }); !errors.Is(err, closeErr) || closed.Load() != 1 {
		t.Fatal("Close repeated owned resource effect", err)
	}
}
func TestControllerSourceCandidateMutexWaitPreservesCallerContext(t *testing.T) {
	var held sync.Mutex
	held.Lock()
	defer held.Unlock()
	stopping := make(chan struct{})
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("candidate waiter canceled")
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(ready)
		unlock, err := controllerSourceLock(ctx, t.Context(), stopping, &held)
		if unlock != nil {
			unlock()
			done <- errors.New("waiter reached active candidate")
			return
		}
		done <- err
	}()
	<-ready
	cancel(cause)
	if err := <-done; !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatal("candidate mutex wait ignored original caller", err)
	}
	// The active mutex remains owned by THIS fixture; no source was touched.
	if held.TryLock() {
		held.Unlock()
		t.Fatal("canceled waiter unlocked active original candidate")
	}
}
func TestControllerSourceQueueDeadlineAndOriginalLifetimeCannotBecomeHealthy(t *testing.T) {
	var gate controllerSourceAdmission
	release, err := gate.acquire(t.Context(), t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if own, err := gate.acquire(ctx, t.Context()); own != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued caller deadline acquired source", err)
	}
	lifetime, stop := context.WithCancelCause(t.Context())
	cause := errors.New("original service ended")
	stop(cause)
	if own, err := gate.acquire(t.Context(), lifetime); own != nil || !errors.Is(err, cause) {
		t.Fatal("new caller revived original ended lifetime", err)
	}
}
