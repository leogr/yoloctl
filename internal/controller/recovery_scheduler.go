package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

// Recovery has one checker and a capped retry backoff. A pass follows the
// controller's lifetime and Drain's cancel. Nothing else can end one of its
// waits, so each gets a 2-minute fail-safe: a storage verify, and a
// verifier's observation from the moment it holds the space lock (its lock
// wait follows the pass alone).
var (
	recoveryObservationFailSafe = 2 * time.Minute
	recoveryBackoff             = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
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

// record tracks strict image working facts until their metadata owner settles
// and their stored state is reconciled. Ordinary execution publication is
// tracked separately and never fences other commands. Callers hold s.mu.
func (s *Server) record(r Receipt, err error, settled, review bool, prior string) {
	if r.Method != "image.job" {
		if r.State == "interrupted" && r.Observation == nil {
			s.wakeRecovery()
		}
		return
	}
	if settled {
		if e := s.executions[r.ID]; e != nil && sameAdmission(*e.receipt, r) {
			if retainsImageMetadata(r) {
				e.settled = true
			} else {
				delete(s.executions, r.ID)
			}
		}
	}

	if r.State == "interrupted" && r.Observation == nil {
		s.wakeRecovery()
	}
	if err == nil {
		if _, dirty := s.ledger[r.ID]; !dirty {
			return
		}
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

// Degrade records a receipt-store or recovery metadata failure. Ordinary
// commands retain their live state while recording is repaired. An actual
// native identity failure remains a separate guard until verified; invalid
// recovery-key authority is rejected before the server is exposed.
func (s *Server) Degrade(cause error, repair func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receiptFailure = fmt.Errorf("controller records are awaiting storage repair: %w", cause)
	var native *NativeStorageUnavailable
	if errors.As(cause, &native) {
		s.noteNativeStorageFailure(native)
	}
	s.startupRepair = repair
	s.RecoveryRequired = false
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
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.recoveryCancel = cancel
	s.recoveryIdle = make(chan struct{})
	idle := s.recoveryIdle
	failed := s.receiptFailure != nil || s.nativeStorageFailure != nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.recoveryCancel = nil
		close(idle)
		s.mu.Unlock()
	}()
	if failed {
		if err := s.repairStorage(ctx); err != nil {
			_ = s.verifyOutcomes(ctx)
			return true
		}
	}
	return s.verifyOutcomes(ctx)
}

// repairStorage clears the recording error only after the store is healthy and
// every ledger entry is reconciled with a complete, strictly decoded
// inventory. A probe alone never clears anything.
func (s *Server) repairStorage(ctx context.Context) error {
	s.mu.Lock()
	repair := s.startupRepair
	s.mu.Unlock()
	var prepareErr error
	if repair != nil {
		prepareErr = repair(ctx)
		if prepareErr == nil {
			s.mu.Lock()
			s.startupRepair = nil
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	if s.receiptFailure == nil && s.nativeStorageFailure == nil {
		s.mu.Unlock()
		return nil
	}
	verify := s.VerifyStorage
	nativeRevision := s.nativeStorageRevision
	hadNativeFailure := s.nativeStorageFailure != nil
	s.mu.Unlock()
	// Receipt recording failure does not prevent effects. A separate actual
	// native-identity failure remains guarded until positively verified. Never
	// run the native identity helper while holding the server mutex.
	if verify == nil && hadNativeFailure {
		return errors.New("native storage identity has not been verified")
	}
	if verify != nil {
		checkCtx, cancel := context.WithTimeout(ctx, recoveryObservationFailSafe)
		err := verify(checkCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.mu.Lock()
				s.noteNativeStorageFailure(err)
				s.mu.Unlock()
			}
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativeStorageRevision != nativeRevision && s.nativeStorageFailure != nil {
		return errors.New("native storage verification changed during recovery")
	}
	s.nativeStorageFailure = nil
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.draining {
		return errors.New("controller is draining")
	}
	if s.receiptFailure == nil {
		return nil
	}
	if err := s.flushExecutions(); err != nil {
		return err
	}
	// Every newly readable inventory is recovered against full live admission
	// identities, not just startup's snapshot or request ID.
	if _, err := s.Receipts.recoverExcept(s.ownsExecution, s.publishHistorical); err != nil {
		return err
	}
	if err := s.flushExecutions(); err != nil {
		return err
	}
	if err := probeReceiptStore(s.Receipts.Dir); err != nil {
		return err
	}
	if prepareErr != nil {
		return prepareErr
	}
	for id, entry := range s.ledger {
		// Strict job facts are not ordinary late records. Never close a current
		// job while its owner is still updating that working state.
		if !entry.settled {
			return errors.New("strict image job metadata is still owned by its execution")
		}
		if e := s.executions[id]; e != nil && !e.settled && sameAdmission(*e.receipt, entry.intended) {
			return errors.New("strict image job metadata is still owned by its execution")
		}
		onDisk, err := s.Receipts.Read(id)
		missing := errors.Is(err, fs.ErrNotExist)
		if err == nil && !sameAdmission(onDisk, entry.intended) {
			return fmt.Errorf("strict image fact %s has another admission identity", id)
		}
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
type pendingClosingCheck struct {
	checked  Receipt
	next     Receipt
	observed []DocumentIntent
}

func (s *Server) verifyOutcomes(ctx context.Context) bool {
	s.mu.Lock()
	all, historyErr := s.Receipts.List()
	indices := map[string]int{}
	for i, r := range all {
		indices[receiptCheckKey(r)] = i
	}
	add := func(r Receipt) {
		key := receiptCheckKey(r)
		if i, ok := indices[key]; ok {
			all[i] = r
		} else {
			indices[key] = len(all)
			all = append(all, r)
		}
	}
	for _, e := range s.executions {
		r := *e.receipt
		r.RecordedLate = e.late
		add(r)
	}
	for _, r := range s.historical {
		add(r)
	}
	for _, note := range s.collisions {
		add(note.later)
	}
	if s.pendingChecks == nil {
		s.pendingChecks = map[string]pendingClosingCheck{}
	}
	pending := make(map[string]pendingClosingCheck, len(s.pendingChecks))
	for key, p := range s.pendingChecks {
		pending[key] = p
	}
	s.mu.Unlock()
	retry := historyErr != nil
	for _, p := range pending {
		if ctx.Err() != nil {
			return true
		}
		if s.publishClosingCheck(p) != nil {
			retry = true
		}
	}
	for _, r := range all {
		if r.State != "interrupted" || r.Observation != nil {
			continue
		}
		s.mu.Lock()
		_, waiting := s.pendingChecks[receiptCheckKey(r)]
		s.mu.Unlock()
		// A check already made is retried only as publication, never as observation.
		if waiting {
			continue
		}
		if _, wasPending := pending[receiptCheckKey(r)]; wasPending {
			continue
		}
		if ctx.Err() != nil {
			return true
		}
		next, observed, notStarted := s.checkInterrupted(ctx, r)
		if notStarted {
			// The pass ended while the verifier waited for the space lock:
			// nothing was observed, so nothing is recorded and the receipt
			// stays eligible for a later pass.
			return true
		}
		p := pendingClosingCheck{checked: r, next: next, observed: observed}
		s.mu.Lock()
		s.pendingChecks[receiptCheckKey(r)] = p
		s.mu.Unlock()
		if s.publishClosingCheck(p) != nil {
			retry = true
		}
	}
	return retry
}

func (s *Server) publishClosingCheck(p pendingClosingCheck) error {
	s.mu.Lock()
	current, err := s.receiptForCheck(p.checked)
	if err != nil {
		// A missing receipt is conclusive only while its store itself exists.
		if errors.Is(err, fs.ErrNotExist) {
			if info, rootErr := os.Lstat(s.Receipts.Dir); rootErr == nil && info.IsDir() {
				delete(s.pendingChecks, receiptCheckKey(p.checked))
				s.mu.Unlock()
				return nil
			}
		}
		if s.receiptFailure == nil {
			s.receiptFailure = err
		}
		s.wakeRecovery()
		s.mu.Unlock()
		return err
	}
	if current.State != "interrupted" || current.Observation != nil {
		delete(s.pendingChecks, receiptCheckKey(p.checked))
		s.mu.Unlock()
		return nil
	}
	if !sameAdmission(current, p.checked) {
		delete(s.pendingChecks, receiptCheckKey(p.checked))
		s.wakeRecovery()
		s.mu.Unlock()
		return nil
	}
	checked := p.checked
	checked.RecordedLate = current.RecordedLate
	next := p.next
	if !sameReceipt(current, checked) {
		oldWithoutImage, newWithoutImage := checked, current
		oldWithoutImage.Image, newWithoutImage.Image = nil, nil
		next = current
		if sameReceipt(oldWithoutImage, newWithoutImage) {
			// Preserve a newer strict image fact; its metadata does not change the
			// scope or document evidence which was actually observed.
			next.Observation = p.next.Observation
		} else {
			next.Observation = &Observation{At: p.next.Observation.At, Reason: "record changed before its closing observation was published"}
			p.observed = nil
		}
		checked = current
	}
	s.mu.Unlock()
	if next.Observation.Proof == "matched" && len(p.observed) == len(checked.Proofs) {
		err = s.publishApplied(checked, p.observed, next.Observation)
	} else {
		err = s.publishObservation(checked, next)
	}
	s.mu.Lock()
	if err == nil {
		delete(s.pendingChecks, receiptCheckKey(p.checked))
	} else {
		s.wakeRecovery()
	}
	s.mu.Unlock()
	return err
}

var errProofMismatch = errors.New("observed state does not match the recorded postimage")

// publishApplied records an exact verified postcondition. The receipt must
// be unchanged since it was read, storage healthy, and every proof matched.
// A mismatch is remembered so it is not retried; no finished outcome holds.
func (s *Server) publishApplied(checked Receipt, observed []DocumentIntent, observation ...*Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.receiptForCheck(checked)
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

			return errProofMismatch
		}
	}
	next := current
	if len(observation) != 0 {
		next.Observation = observation[0]
	}
	next.recovered(DispositionApplied, ProofDocumentImage)
	err = s.publishCheckedReceipt(checked, next)
	if err != nil {
		s.keepClosingCheck(checked, next, observed, err)
		return err
	}
	_ = s.refreshRecovery()
	s.notifyReceipts()
	return nil
}

// publishObservation publishes only an unchanged ended receipt, under the same
// controller mutex used for proof publication and execution finalization.
func (s *Server) publishObservation(checked, next Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.receiptForCheck(checked)
	if err != nil {
		return err
	}
	if !sameReceipt(current, checked) {
		return errors.New("receipt changed during its closing check")
	}
	err = s.publishCheckedReceipt(checked, next)
	if err != nil {
		s.keepClosingCheck(checked, next, nil, err)
	}
	s.notifyReceipts()
	return err
}

// keepClosingCheck retains publication work rather than an obsolete whole
// receipt in the failure ledger. A newer strict image fact must survive repair.
func (s *Server) keepClosingCheck(checked, next Receipt, observed []DocumentIntent, err error) {
	if s.pendingChecks == nil {
		s.pendingChecks = map[string]pendingClosingCheck{}
	}
	if next.Observation == nil {
		next.Observation = &Observation{At: time.Now().UTC(), Proof: "matched"}
	}
	if _, exists := s.pendingChecks[receiptCheckKey(checked)]; !exists {
		s.pendingChecks[receiptCheckKey(checked)] = pendingClosingCheck{checked: checked, next: next, observed: observed}
	}
	if s.receiptFailure == nil {
		s.receiptFailure = err
	}
	s.wakeRecovery()
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
	if s.RecoveryKey == nil {
		s.RecoveryKey = key
	}
	return s.refreshRecovery()
}
