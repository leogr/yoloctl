package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// breakStore makes receipt flushes fail until restore is called. Renaming the
// directory away works even when tests run as root.
func breakStore(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if err := os.Rename(dir, dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir+".saved", dir); err != nil {
			t.Fatal(err)
		}
	}
}

func storeServer(t *testing.T) *Server {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := LoadRecoveryKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Hello: hello(), Receipts: Receipts{dir}, DiskID: testDisk, RecoveryKey: key,
		Operations: map[string]Operation{"box.start": {Mutates: true, EffectGated: true}}}
}

func TestRepairedReceiptStorageReopensWithoutRestart(t *testing.T) {
	s := storeServer(t)
	var restore func()
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		if err := streams.BeginEffects(ctx, scopeIntent(writes(box("web")))); err != nil {
			return 2, err
		}
		restore = breakStore(t, s.Receipts.Dir) // the finalization flush fails
		return 0, nil
	}
	if err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard); err == nil || s.receiptFailure == nil {
		t.Fatal("a failed finalization flush was hidden", err)
	}
	// Still broken: the pass fails and keeps the hold.
	if !s.recoveryPass(t.Context()) || s.receiptFailure == nil {
		t.Fatal("unhealthy storage reopened admission")
	}
	restore()
	if s.recoveryPass(t.Context()) || s.receiptFailure != nil || len(s.ledger) != 0 {
		t.Fatalf("healthy reconciled storage stayed held: %v", s.receiptFailure)
	}
	r, err := s.Receipts.Read(request("box.start").ID)
	if err != nil || r.State != "complete" || r.EffectPhase != EffectStarted {
		t.Fatal("the original execution's outcome was not reconciled", r, err)
	}
	next := request("box.start")
	next.ID += "-next"
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		return 0, streams.BeginEffects(ctx, scopeIntent(writes(box("api"))))
	}
	if err := invoke(t, s, next, nil, io.Discard, io.Discard); err != nil {
		t.Fatal("repaired controller refused a mutation", err)
	}
	if _, err := os.Stat(filepath.Join(s.Receipts.Dir, ".recovery-probe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe left behind", err)
	}
}

func TestProbeSuccessWithCorruptReceiptKeepsTheHold(t *testing.T) {
	s := storeServer(t)
	s.receiptFailure = errors.New("simulated ENOSPC")
	if err := os.WriteFile(filepath.Join(s.Receipts.Dir, "corrupt-0123456789abcdef.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.recoveryPass(t.Context()) || s.receiptFailure == nil {
		t.Fatal("a corrupt receipt inventory reopened admission")
	}
}

func TestStorageRecoveryRequiresCurrentIdentityAfterFilesystemRepair(t *testing.T) {
	for _, problem := range []string{"UUID changed", "ownership marker unavailable", "native account bind lost"} {
		t.Run(problem, func(t *testing.T) {
			s := storeServer(t)
			effects, healthy := 0, false
			var restore func()
			s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
				if err := streams.BeginEffects(ctx, scopeIntent(writes(box("web")))); err != nil {
					return 2, err
				}
				effects++
				restore = breakStore(t, s.Receipts.Dir)
				return 0, nil
			}
			if err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard); err == nil {
				t.Fatal("receipt fault was hidden")
			}
			restore() // directory ownership/mode and probe I/O now succeed
			s.VerifyStorage = func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("current identity check is unbounded")
				}
				if !healthy {
					return errors.New(problem)
				}
				return nil
			}
			if !s.recoveryPass(t.Context()) || s.receiptFailure == nil || effects != 1 {
				t.Fatal("healthy directory/probe substituted for current identity", effects)
			}
			if err := invoke(t, s, request("control.ping"), nil, io.Discard, io.Discard); err != nil {
				t.Fatal("identity hold blocked inspection", err)
			}
			healthy = true
			if s.recoveryPass(t.Context()) || s.receiptFailure != nil || effects != 1 {
				t.Fatal("restored identity did not reopen, or replayed the command", effects)
			}
			if r, err := s.Receipts.Read(request("box.start").ID); err != nil || r.State != "complete" {
				t.Fatal(r, err)
			}
		})
	}
}

func TestStorageIdentityVerificationLeavesReadsResponsive(t *testing.T) {
	s := storeServer(t)
	s.receiptFailure = errors.New("simulated receipt fsync failure")
	started, release := make(chan struct{}), make(chan struct{})
	s.VerifyStorage = func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan bool, 1)
	go func() { done <- s.recoveryPass(t.Context()) }()
	<-started
	ping := make(chan error, 1)
	go func() { ping <- invoke(t, s, request("control.ping"), nil, io.Discard, io.Discard) }()
	select {
	case err := <-ping:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("native identity helper held the server mutex and blocked reads")
	}
	close(release)
	<-done
}

func TestStorageRepairRechecksExecutionsAfterIdentityVerification(t *testing.T) {
	s := storeServer(t)
	s.receiptFailure = errors.New("simulated receipt failure")
	started, release := make(chan struct{}), make(chan struct{})
	s.VerifyStorage = func(context.Context) error {
		close(started)
		<-release
		return nil
	}
	done := make(chan bool, 1)
	go func() { done <- s.recoveryPass(t.Context()) }()
	<-started
	// An execution whose flush failed must not disappear merely because it
	// was added while the current-identity helper ran outside the mutex.
	s.mu.Lock()
	r, err := s.Receipts.begin(request("box.start"), Principal{}, "generation", testDisk, true)
	if err == nil {
		s.ledger = map[string]ledgerEntry{r.ID: {intended: r, settled: false}}
	}
	s.mu.Unlock()
	close(release)
	if !<-done || s.receiptFailure == nil || err != nil {
		t.Fatal("identity check erased an unsettled execution", err)
	}
	s.VerifyStorage = func(context.Context) error { return nil }
	s.ledger[r.ID] = ledgerEntry{intended: r, settled: true}
	if s.recoveryPass(t.Context()) || s.receiptFailure != nil {
		t.Fatal("settled, reconciled execution did not reopen storage")
	}
}

func TestStorageRepairWaitsForUnsettledExecutions(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "running-0123456789ab", "pending", writes(box("web")), nil)
	s.mu.Lock()
	s.record(r, errors.New("simulated fsync failure"), false, false, "")
	s.mu.Unlock()
	if !s.recoveryPass(t.Context()) || s.receiptFailure == nil {
		t.Fatal("storage reopened while an execution's receipt was unsettled")
	}
}

func TestFailedAcknowledgmentIsReadBackNeverRepeated(t *testing.T) {
	for _, durable := range []bool{true, false} {
		s := storeServer(t)
		r := seedReceipt(t, s, "held-0123456789abcd", "uncertain", writes(box("web")), nil)
		reviewed := r
		reviewed.State = "acknowledged"
		if durable {
			if err := s.Receipts.Save(reviewed); err != nil {
				t.Fatal(err)
			}
		}
		s.mu.Lock()
		s.record(reviewed, errors.New("lost fsync reply"), true, true, "uncertain")
		s.mu.Unlock()
		if s.recoveryPass(t.Context()) || s.receiptFailure != nil {
			t.Fatal("read-back acknowledgment kept the storage hold", s.receiptFailure)
		}
		got, err := s.Receipts.Read(r.ID)
		want := "uncertain"
		if durable {
			want = "acknowledged"
		}
		if err != nil || got.State != want || s.RecoveryRequired == durable {
			t.Fatalf("durable=%v state=%s recovery=%v err=%v", durable, got.State, s.RecoveryRequired, err)
		}
	}
}

func TestFailedAdmissionFlushIsClassifiedNotApplied(t *testing.T) {
	s := storeServer(t)
	saved := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error { return errors.New("simulated ENOSPC") }
	err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard)
	writeReceipt = saved
	if err == nil || s.receiptFailure == nil {
		t.Fatal("a failed admission flush did not hold storage", err)
	}
	// A pending record the failed flush may have published anyway.
	r, err := s.Receipts.begin(request("box.start"), Principal{}, "generation-1", testDisk, true)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.ledger[r.ID] = ledgerEntry{intended: r, settled: true}
	s.mu.Unlock()
	if s.recoveryPass(t.Context()) || s.receiptFailure != nil {
		t.Fatal(s.receiptFailure)
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.State != "recovered" || got.Recovery.Disposition != DispositionNotApplied {
		t.Fatal(got, err)
	}
}

func provenReceipt(t *testing.T, s *Server, postimage string) (Receipt, DocumentIntent) {
	t.Helper()
	d := DocumentIntent{Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte(postimage), Mode: 0o600}
	r := seedReceipt(t, s, "proof-0123456789abcd", "uncertain", writes(acct("personal")), func(r *Receipt) {
		r.Method, r.Verifier = "account.secret.set", VerifierAccountDocument
		r.Proofs = []Proof{s.RecoveryKey.proof(r.DiskID, r.ID, r.Verifier, d)}
	})
	return r, d
}

func TestVerifiedPostimagePublishesAppliedOnce(t *testing.T) {
	s := storeServer(t)
	r, want := provenReceipt(t, s, "secret: new")
	observed := want
	calls := 0
	s.Verify = func(_ context.Context, got Receipt) ([]DocumentIntent, error) {
		calls++
		if got.ID != r.ID {
			t.Fatal("verified another receipt")
		}
		return []DocumentIntent{observed}, nil
	}
	if s.recoveryPass(t.Context()) {
		t.Fatal("a proven receipt asked for a retry")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.State != "recovered" || got.Recovery.Disposition != DispositionApplied || s.RecoveryRequired {
		t.Fatal(got, s.RecoveryRequired, err)
	}
	if s.recoveryPass(t.Context()); calls != 1 {
		t.Fatal("a terminal receipt was checked again", calls)
	}
}

func TestVerifierFailuresKeepTheHold(t *testing.T) {
	for name, tc := range map[string]struct {
		observe func(DocumentIntent) ([]DocumentIntent, error)
		retry   bool
	}{
		"preimage": {func(d DocumentIntent) ([]DocumentIntent, error) {
			d.Data = []byte("secret: old")
			return []DocumentIntent{d}, nil
		}, false},
		"mode": {func(d DocumentIntent) ([]DocumentIntent, error) {
			d.Mode = 0o644
			return []DocumentIntent{d}, nil
		}, false},
		"absent": {func(d DocumentIntent) ([]DocumentIntent, error) {
			return []DocumentIntent{{Kind: d.Kind, Name: d.Name}}, nil
		}, false},
		"lock busy": {func(DocumentIntent) ([]DocumentIntent, error) {
			return nil, context.DeadlineExceeded
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			s := storeServer(t)
			r, want := provenReceipt(t, s, "secret: new")
			calls := 0
			s.Verify = func(context.Context, Receipt) ([]DocumentIntent, error) { calls++; return tc.observe(want) }
			if retry := s.recoveryPass(t.Context()); retry != tc.retry {
				t.Fatal("retry", retry)
			}
			got, err := s.Receipts.Read(r.ID)
			if err != nil || got.State != "uncertain" {
				t.Fatal(got, err)
			}
			s.recoveryPass(t.Context())
			if wantCalls := map[bool]int{true: 2, false: 1}[tc.retry]; calls != wantCalls {
				t.Fatal("checks", calls)
			}
		})
	}
}

func TestReceiptChangedDuringCheckIsNotPublished(t *testing.T) {
	s := storeServer(t)
	r, want := provenReceipt(t, s, "secret: new")
	s.Verify = func(context.Context, Receipt) ([]DocumentIntent, error) {
		reviewed := r
		reviewed.State = "acknowledged"
		if err := s.Receipts.Save(reviewed); err != nil {
			t.Fatal(err)
		}
		return []DocumentIntent{want}, nil
	}
	s.recoveryPass(t.Context())
	if got, err := s.Receipts.Read(r.ID); err != nil || got.State != "acknowledged" {
		t.Fatal("a check overwrote a concurrent host decision", got, err)
	}
}

func TestDegradedStartupServesReadsAndRepairsItself(t *testing.T) {
	s := storeServer(t)
	attempts := 0
	s.Degrade(errors.New("receipt directory flush failed"), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("still failing")
		}
		return nil
	})
	if err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard); err == nil {
		t.Fatal("degraded controller admitted a mutation")
	}
	ping := request("control.ping")
	if err := invoke(t, s, ping, nil, io.Discard, io.Discard); err != nil {
		t.Fatal("degraded controller does not serve", err)
	}
	if !s.recoveryPass(t.Context()) || s.receiptFailure == nil {
		t.Fatal("failed repair reopened admission")
	}
	if s.recoveryPass(t.Context()) || s.receiptFailure != nil || attempts != 2 {
		t.Fatal("repair did not reopen admission", attempts, s.receiptFailure)
	}
}

func TestDrainCancelsAndWaitsForTheRecoveryCheck(t *testing.T) {
	s := storeServer(t)
	provenReceipt(t, s, "secret: new")
	started := make(chan struct{})
	s.Verify = func(ctx context.Context, _ Receipt) ([]DocumentIntent, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan bool)
	go func() { done <- s.recoveryPass(context.Background()) }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("drain returned before the recovery check ended")
	}
	if s.recoveryPass(context.Background()) {
		t.Fatal("a recovery pass ran while draining")
	}
}

func TestRecoverySchedulerBacksOffWithoutSpinning(t *testing.T) {
	saved := recoveryBackoff
	t.Cleanup(func() { recoveryBackoff = saved })
	recoveryBackoff = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	s := storeServer(t)
	provenReceipt(t, s, "secret: new")
	calls := make(chan struct{}, 100)
	s.Verify = func(context.Context, Receipt) ([]DocumentIntent, error) {
		calls <- struct{}{}
		return nil, errors.New("evidence unavailable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunRecovery(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if n := len(calls); n < 2 || n > 12 {
		t.Fatal("checks in 300ms:", n)
	}
}

func TestVerifierWorkerUsesPrivateDescriptorsAndBounds(t *testing.T) {
	receipt := func(name string) Receipt {
		return Receipt{ID: "proof-0123456789abcd", Verifier: VerifierAccountDocument,
			Proofs: []Proof{{Kind: DocumentAccount, Name: name, Present: true}}}
	}
	got, err := RunVerifier(t.Context(), "work", receipt("personal"))
	if err != nil || len(got) != 1 || string(got[0].Data) != "observed" || got[0].Name != "personal" {
		t.Fatal(got, err)
	}
	if _, err := RunVerifier(t.Context(), "work", receipt("garbage")); err == nil {
		t.Fatal("malformed verifier output accepted")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := RunVerifier(ctx, "work", receipt("hang")); err == nil || time.Since(start) > 5*time.Second {
		t.Fatal("hung verifier was not bounded", err, time.Since(start))
	}
	if _, err := RunVerifier(t.Context(), "work", Receipt{Verifier: "looks-healthy", Proofs: []Proof{{Kind: DocumentAccount, Name: "x"}}}); err == nil {
		t.Fatal("an unreviewed verifier ran")
	}
}
