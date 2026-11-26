package controller

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func withObservationFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	saved := recoveryObservationFailSafe
	recoveryObservationFailSafe = d
	t.Cleanup(func() { recoveryObservationFailSafe = saved })
}

func documentReceipt(name string) Receipt {
	return Receipt{ID: "proof-0123456789abcd", Verifier: VerifierAccountDocument,
		Proofs: []Proof{{Kind: DocumentAccount, Name: name, Present: true}}}
}

// A lock held longer than the fail-safe by another operation says nothing
// about the interrupted one: the fail-safe starts once the verifier holds it.
func TestVerifierFailSafeStartsAfterTheSpaceLock(t *testing.T) {
	withObservationFailSafe(t, 150*time.Millisecond)
	got, err := RunVerifier(t.Context(), "work", documentReceipt("lockwait"))
	if err != nil || len(got) != 1 || got[0].Name != "lockwait" || string(got[0].Data) != "observed" {
		t.Fatal("a check behind a long lock wait was not observed", got, err)
	}
}

func TestVerifierFailSafeEndsAnObservationThatHangs(t *testing.T) {
	withObservationFailSafe(t, 150*time.Millisecond)
	start := time.Now()
	_, err := RunVerifier(t.Context(), "work", documentReceipt("observehang"))
	if err == nil || !strings.Contains(err.Error(), "fail-safe") || errors.Is(err, errObservationNotStarted) {
		t.Fatal("a hung observation did not end at its fail-safe", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the fail-safe did not bound the observation", time.Since(start))
	}
}

func TestVerifierCancelledDuringTheLockWaitObservedNothing(t *testing.T) {
	withObservationFailSafe(t, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err := RunVerifier(ctx, "work", documentReceipt("lockhang"))
	if !errors.Is(err, errObservationNotStarted) {
		t.Fatal("a cancelled lock wait was reported as an observation", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation did not end the lock wait", time.Since(start))
	}
}

// A verifier that ends by itself before the lock (bad input, a lock error) is
// an ordinary failure, recorded once like any other.
func TestVerifierFailingBeforeTheLockIsAnOrdinaryError(t *testing.T) {
	_, err := RunVerifier(t.Context(), "work", documentReceipt("lockfail"))
	if err == nil || errors.Is(err, errObservationNotStarted) {
		t.Fatal("a verifier failing before the lock was not an ordinary error", err)
	}
}

// Drain or controller end during the lock wait leaves the receipt eligible;
// the next pass makes exactly one real observation.
func TestRecoveryCancelledBeforeObservationLeavesTheReceiptEligible(t *testing.T) {
	s := storeServer(t)
	r, want := provenReceipt(t, s, "secret: new")
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	s.Verify = func(ctx context.Context, got Receipt) ([]DocumentIntent, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-ctx.Done()
			return nil, errors.Join(errObservationNotStarted, ctx.Err())
		}
		return []DocumentIntent{want}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool, 1)
	go func() { done <- s.recoveryPass(ctx) }()
	<-entered
	cancel()
	if !<-done {
		t.Fatal("a cancelled pass reported nothing left to do")
	}
	stored, err := s.Receipts.Read(r.ID)
	if err != nil || stored.Observation != nil {
		t.Fatal("a check cancelled before observation was recorded", stored.Observation, err)
	}
	s.mu.Lock()
	pending := len(s.pendingChecks)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatal("a check cancelled before observation was kept as pending")
	}
	if s.recoveryPass(t.Context()) {
		t.Fatal("the next pass did not complete")
	}
	stored, err = s.Receipts.Read(r.ID)
	if err != nil || stored.Observation == nil || calls.Load() != 2 {
		t.Fatal("the next pass did not observe once", stored.Observation, calls.Load(), err)
	}
	_ = s.recoveryPass(t.Context())
	if calls.Load() != 2 {
		t.Fatal("an observed receipt was checked again", calls.Load())
	}
}

func TestRecoveryVerifierFailingBeforeTheLockIsRecordedOnce(t *testing.T) {
	s := storeServer(t)
	r, _ := provenReceipt(t, s, "secret: new")
	var calls atomic.Int32
	s.Verify = func(context.Context, Receipt) ([]DocumentIntent, error) {
		calls.Add(1)
		return nil, errors.New("invalid verifier request")
	}
	_ = s.recoveryPass(t.Context())
	stored, err := s.Receipts.Read(r.ID)
	if err != nil || stored.Observation == nil || !strings.Contains(stored.Observation.Proof, "invalid verifier request") {
		t.Fatal("a verifier failing by itself was not recorded", stored.Observation, err)
	}
	_ = s.recoveryPass(t.Context())
	_ = s.recoveryPass(t.Context())
	if n := calls.Load(); n != 1 {
		t.Fatal("a verifier failing by itself was started again", n)
	}
}

// The pass no longer has its own 30 s budget: a long pass completes, and only
// the controller's end or Drain cancels it.
func TestRecoveryPassCarriesNoDeadlineOfItsOwn(t *testing.T) {
	s := storeServer(t)
	_, want := provenReceipt(t, s, "secret: new")
	var bounded atomic.Bool
	s.Verify = func(ctx context.Context, _ Receipt) ([]DocumentIntent, error) {
		_, ok := ctx.Deadline()
		bounded.Store(ok)
		return []DocumentIntent{want}, nil
	}
	if s.recoveryPass(context.Background()) {
		t.Fatal("the pass did not complete")
	}
	if bounded.Load() {
		t.Fatal("the check inherited a pass deadline")
	}
}

func TestRecoveryStorageVerifyEndsAtItsFailSafe(t *testing.T) {
	withObservationFailSafe(t, 150*time.Millisecond)
	s := storeServer(t)
	s.receiptFailure = errors.New("simulated receipt fsync failure")
	s.VerifyStorage = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	if !s.recoveryPass(context.Background()) {
		t.Fatal("a storage verify that never answered cleared the failure")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the storage verify was not bounded by its fail-safe", time.Since(start))
	}
}

func withVerifierCommand(t *testing.T, args ...string) {
	t.Helper()
	saved := verifierCommand
	verifierCommand = args
	t.Cleanup(func() { verifierCommand = saved })
}

// Cancelled before it read its input: the input write breaks, and still
// nothing was observed, so the receipt's one observation is not consumed.
func TestVerifierCancelledBeforeReadingItsInputObservedNothing(t *testing.T) {
	withVerifierCommand(t, "controller-internal", "verify-closeinput")
	withObservationFailSafe(t, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	saved := verifierStarted
	t.Cleanup(func() { verifierStarted = saved })
	// The verifier closes its input at once; the pass is cancelled before the
	// parent writes it, so that write breaks.
	verifierStarted = func() { time.Sleep(300 * time.Millisecond); cancel(); time.Sleep(300 * time.Millisecond) }
	_, err := RunVerifier(ctx, "work", documentReceipt("personal"))
	if !errors.Is(err, errObservationNotStarted) {
		t.Fatal("a cancelled verifier whose input broke was reported as an observation", err)
	}
}

// A verifier that fails by itself before its input or the lock is an ordinary
// error, recorded once, never "not started".
func TestVerifierFailingBeforeItsInputIsAnOrdinaryError(t *testing.T) {
	withVerifierCommand(t, "controller-internal", "verify-exitearly")
	_, err := RunVerifier(t.Context(), "work", documentReceipt("personal"))
	if err == nil || errors.Is(err, errObservationNotStarted) {
		t.Fatal("an independent pre-marker failure was not an ordinary error", err)
	}
}

// A result over the bound while the verifier is still writing: the verifier is
// ended and reaped at once, well within the fail-safe, and its lock is free.
func TestVerifierOversizedResultEndsTheWriterAndReleasesItsLock(t *testing.T) {
	withObservationFailSafe(t, time.Hour)
	space := "ovr" + strconv.FormatInt(time.Now().UnixNano(), 36)
	path := testVerifierLockPath(space)
	t.Cleanup(func() { _ = os.Remove(path) })
	start := time.Now()
	_, err := RunVerifier(t.Context(), space, documentReceipt("oversize"))
	if err == nil || !strings.Contains(err.Error(), "exceeds its limit") {
		t.Fatal("an oversized result was not refused", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("the writing verifier was not ended before reaping", time.Since(start))
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("the verifier's lock was still held after it returned", err)
	}
}
