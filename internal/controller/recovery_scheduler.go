package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

// Recovery budgets (PLAN-r1 D4): one checker, a bounded check per receipt,
// a bounded pass including lock waits, and a capped retry backoff.
var (
	recoveryReceiptBudget = 5 * time.Second
	recoveryPassBudget    = 30 * time.Second
	recoveryBackoff       = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
)

// ledgerEntry is the in-memory account of a receipt whose durable state is
// unknown after a failed flush, or that was written while storage was
// failed. intended is the controller's own last decision for it.
type ledgerEntry struct {
	intended Receipt
	// settled: no execution can change the intended state any more (the
	// worker was finalized, never launched, or this is a host decision).
	settled bool
	// review: intended is an acknowledgment; its durability is read back,
	// never repeated.
	review bool
	// prior is the state before a failed acknowledgment.
	prior string
}

// receiptWriteError marks a failed receipt flush in Receipts.begin.
type receiptWriteError struct{ err error }

func (e *receiptWriteError) Error() string { return e.err.Error() }
func (e *receiptWriteError) Unwrap() error { return e.err }

// record notes a receipt flush in the failure ledger: a failed one sets the
// storage hold; any flush while the hold stands must be read back before
// the hold can be dropped. Callers hold s.mu.
func (s *Server) record(r Receipt, err error, settled, review bool, prior string) {
	if err == nil && s.receiptFailure == nil {
		return
	}
	// An eviction failure names no new receipt; only the inventory check
	// below reconciles it.
	if r.ID != "" {
		if s.ledger == nil {
			s.ledger = map[string]ledgerEntry{}
		}
		entry := s.ledger[r.ID]
		entry.intended, entry.settled, entry.review = r, settled, review
		if review {
			entry.prior = prior
		}
		s.ledger[r.ID] = entry
	}
	if err != nil {
		if s.receiptFailure == nil {
			s.receiptFailure = err
		}
		s.wakeRecovery()
	}
}

// wakeRecovery schedules an immediate pass. It never blocks.
func (s *Server) wakeRecovery() {
	if s.recoveryWake == nil {
		return
	}
	select {
	case s.recoveryWake <- struct{}{}:
	default:
	}
}

// Degrade starts the server read-only after a receipt-store or recovery
// metadata failure. repair re-establishes that metadata; until it and the
// receipt reconciliation succeed, every mutation is refused. Invalid disk
// identity or store authority is not repairable and never reaches here.
func (s *Server) Degrade(cause error, repair func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receiptFailure = fmt.Errorf("controller started read-only: %w", cause)
	s.startupRepair = repair
	s.RecoveryRequired = true
}

// RunRecovery is the controller's single recovery checker. It runs until ctx
// ends, starting immediately and again after every new unresolved outcome
// or storage failure. It never replays or repairs an operation's effects.
func (s *Server) RunRecovery(ctx context.Context) {
	s.mu.Lock()
	if s.recoveryWake == nil {
		s.recoveryWake = make(chan struct{}, 1)
	}
	wake := s.recoveryWake
	s.mu.Unlock()
	s.wakeRecovery()
	attempt := 0
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer:
		}
		timer = nil
		retry := s.recoveryPass(ctx)
		if !retry {
			attempt = 0
			continue
		}
		delay := recoveryBackoff[min(attempt, len(recoveryBackoff)-1)]
		attempt++
		timer = time.After(delay)
	}
}

// recoveryPass reports whether something remains that a later pass may
// resolve. It is skipped while the server drains; Drain cancels it.
func (s *Server) recoveryPass(parent context.Context) bool {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithTimeout(parent, recoveryPassBudget)
	defer cancel()
	s.recoveryCancel = cancel
	s.recoveryIdle = make(chan struct{})
	idle := s.recoveryIdle
	failed := s.receiptFailure != nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.recoveryCancel = nil
		close(idle)
		s.mu.Unlock()
	}()
	if failed {
		if err := s.repairStorage(ctx); err != nil {
			return true
		}
	}
	return s.verifyOutcomes(ctx)
}

// repairStorage drops the storage hold only after the store is healthy and
// every ledger entry is reconciled with a complete, strictly decoded
// inventory. A probe alone never clears anything.
func (s *Server) repairStorage(ctx context.Context) error {
	s.mu.Lock()
	repair := s.startupRepair
	s.mu.Unlock()
	if repair != nil {
		if err := repair(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		s.startupRepair = nil
		s.mu.Unlock()
	}
	s.mu.Lock()
	if s.receiptFailure == nil {
		s.mu.Unlock()
		return nil
	}
	for _, entry := range s.ledger {
		if !entry.settled {
			s.mu.Unlock()
			return errors.New("an execution with an unrecorded receipt is still running")
		}
	}
	verify := s.VerifyStorage
	s.mu.Unlock()
	// The storage hold prevents new effects throughout this check. Existing
	// executions may settle, and reads/drain must remain responsive. Never
	// run the native identity helper while holding the server mutex.
	if verify != nil {
		checkCtx, cancel := context.WithTimeout(ctx, recoveryReceiptBudget)
		err := verify(checkCtx)
		cancel()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.draining {
		return errors.New("controller is draining")
	}
	if s.receiptFailure == nil {
		return nil
	}
	for _, entry := range s.ledger {
		if !entry.settled {
			return errors.New("an execution changed while storage identity was checked")
		}
	}
	if err := probeReceiptStore(s.Receipts.Dir); err != nil {
		return err
	}
	for id, entry := range s.ledger {
		onDisk, err := s.Receipts.Read(id)
		missing := errors.Is(err, fs.ErrNotExist)
		if err != nil && !missing {
			return err
		}
		switch {
		case entry.review:
			// A host decision is honored only if durable; otherwise the
			// outcome stays as it was. It is never repeated automatically.
			if missing || onDisk.State != "acknowledged" && onDisk.State != entry.prior {
				return fmt.Errorf("receipt %s does not match its reviewed state", id)
			}
		case missing && entry.intended.State == "pending":
			// Admission failed before any worker ran: nothing to classify.
		default:
			want := entry.intended
			if want.Image != nil && want.abandonedImage() && want.Image.Residual && !want.Image.OrphanRecorded {
				if err := s.Receipts.SaveImageOrphan(want); err != nil {
					return err
				}
				want.Image.OrphanRecorded = true
			}
			if !missing && onDisk.Image != nil && want.Image != nil && onDisk.Completion != nil && want.Completion == nil {
				want.Completion = onDisk.Completion
				want.Status = onDisk.Completion.Status
				want.State = "complete"
				want.Image.Output, want.Image.Lease = onDisk.Image.Output, onDisk.Image.Lease
			}
			if want.State == "pending" {
				// Never executed: a waiting gate proves nothing changed.
				want.interrupt()
				want.Finished = time.Now().UTC()
			}
			if missing || !sameReceipt(onDisk, want) {
				if err := s.Receipts.Save(want); err != nil {
					return err
				}
				readBack, err := s.Receipts.Read(id)
				if err != nil || !sameReceipt(readBack, want) {
					return fmt.Errorf("receipt %s did not read back as written", id)
				}
			}
		}
	}
	if _, err := s.Receipts.List(); err != nil {
		return err
	}
	s.ledger, s.receiptFailure = nil, nil
	_ = s.refreshRecovery()
	s.notifyReceipts()
	return nil
}

// probeReceiptStore writes, syncs, reads back and removes one named probe.
func probeReceiptStore(dir string) error {
	const name = ".recovery-probe"
	want := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := safepath.WriteFile(dir, name, want, 0o600); err != nil {
		return err
	}
	got, err := safepath.ReadFile(dir, name, 256)
	if err != nil {
		return err
	}
	if string(got) != string(want) {
		return errors.New("receipt store probe read back different bytes")
	}
	return safepath.RemoveFile(dir, name)
}

// verifyOutcomes runs the reviewed read-only verifiers over unresolved
// receipts that recorded proofs. Each check is bounded; a pass resumes after
// the last receipt it reached so one entry cannot starve the others.
func (s *Server) verifyOutcomes(ctx context.Context) bool {
	s.mu.Lock()
	if s.Verify == nil || s.RecoveryKey == nil || s.receiptFailure != nil {
		s.mu.Unlock()
		return false
	}
	all, err := s.Receipts.List()
	s.mu.Unlock()
	if err != nil {
		return true
	}
	var candidates []Receipt
	for _, r := range all {
		if r.unresolved() && r.Verifier != "" && len(r.Proofs) != 0 && !s.checkedProof(r) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	start := 0
	for i, r := range candidates {
		if r.ID > s.recoveryCursor {
			start = i
			break
		}
	}
	retry := false
	for n := range candidates {
		r := candidates[(start+n)%len(candidates)]
		if ctx.Err() != nil {
			return true
		}
		s.recoveryCursor = r.ID
		checkCtx, cancel := context.WithTimeout(ctx, recoveryReceiptBudget)
		observed, err := s.Verify(checkCtx, r)
		cancel()
		if err != nil {
			retry = true // locks or evidence unavailable: defer, keep the hold
			continue
		}
		if err := s.publishApplied(r, observed); err != nil {
			retry = retry || !errors.Is(err, errProofMismatch)
		}
	}
	return retry
}

var errProofMismatch = errors.New("observed state does not match the recorded postimage")

func (s *Server) checkedProof(r Receipt) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mismatched[r.ID]
}

// publishApplied records an exact verified postcondition. The receipt must
// be unchanged since it was read, storage healthy, and every proof matched.
// A mismatch is remembered so it is not retried; the hold remains.
func (s *Server) publishApplied(checked Receipt, observed []DocumentIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receiptFailure != nil {
		return errors.New("receipt storage failed")
	}
	current, err := s.Receipts.Read(checked.ID)
	if err != nil {
		return err
	}
	if !sameReceipt(current, checked) {
		return errors.New("receipt changed during its check")
	}
	if len(observed) != len(checked.Proofs) {
		return errProofMismatch
	}
	for i, p := range checked.Proofs {
		if !s.RecoveryKey.matchesVersion(checked.ProofVersion, checked.DiskID, checked.ID, checked.Verifier, p, observed[i]) {
			if s.mismatched == nil {
				s.mismatched = map[string]bool{}
			}
			s.mismatched[checked.ID] = true
			return errProofMismatch
		}
	}
	next := current
	next.recovered(DispositionApplied, ProofDocumentImage)
	err = s.Receipts.Save(next)
	s.record(next, err, true, false, "")
	if err != nil {
		return err
	}
	_ = s.refreshRecovery()
	s.notifyReceipts()
	return nil
}

// sameReceipt compares durable encodings, which is what a reader observes.
func sameReceipt(a, b Receipt) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

// ActivateRecovery installs the recovery key once the receipt store has been
// recovered at startup, and derives the current holds from it.
func (s *Server) ActivateRecovery(key *RecoveryKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecoveryKey = key
	return s.refreshRecovery()
}
