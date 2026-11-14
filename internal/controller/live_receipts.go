package controller

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/safepath"
)

// liveReceipt belongs to one admission, not just an ID. Every revision and
// its actual publication are serialized by Server.mu, including rename/fsync.
// A completed dirty receipt stays here until publication or collision logging.
type liveReceipt struct {
	receipt       *Receipt
	revision      uint64
	written       uint64
	unpublished   bool
	late          bool
	settled       bool
	strict        bool
	durableWarned bool
	collision     *Receipt
}

type receiptCollision struct {
	later      Receipt
	historical Receipt
}

func sameAdmission(a, b Receipt) bool {
	return a.ID == b.ID && a.Started.Equal(b.Started) && a.Generation == b.Generation && a.DiskID == b.DiskID && a.Method == b.Method && a.Principal == b.Principal
}

// beginExecution never treats an unavailable record as absence. It admits the
// command, but protects that path from ALL publication until a conclusive read.
// Callers hold s.mu; strict image.job working facts use Receipts.begin instead.
func (s *Server) beginExecution(req Request, p Principal, gated bool) (*Receipt, error) {
	if s.executions == nil {
		s.executions = map[string]*liveReceipt{}
	}
	if s.executions[req.ID] != nil {
		return nil, fmt.Errorf("request %s is already executing or awaiting recording; do not replay it", req.ID)
	}
	if _, ok := s.ledger[req.ID]; ok {
		return nil, fmt.Errorf("request %s has a known record awaiting publication; do not replay it", req.ID)
	}
	if _, ok := s.historical[req.ID]; ok {
		return nil, fmt.Errorf("request %s has a known historical record; do not replay it", req.ID)
	}
	if _, ok := s.collisions[req.ID]; ok {
		return nil, fmt.Errorf("request %s was already used; do not replay it", req.ID)
	}
	old, err := s.Receipts.Read(req.ID)
	if err == nil {
		return nil, fmt.Errorf("request %s already recorded (%s); query its outcome instead of replaying", req.ID, old.State)
	}
	unknown := !errors.Is(err, fs.ErrNotExist)
	r := &Receipt{ID: req.ID, Method: req.Method, Principal: p, State: "pending", Started: time.Now().UTC(), Generation: s.Hello.Generation}
	if s.DiskID != "" {
		r.ProofVersion, r.DiskID = documentProofVersion, s.DiskID
	}
	if gated {
		r.EffectPhase = EffectWaiting
	}
	e := &liveReceipt{receipt: r, unpublished: unknown, late: unknown}
	s.executions[r.ID] = e
	logging.L().Info("Operation admitted", "actor", logging.Controller, "request", r.ID, "method", r.Method, "started", r.Started)
	_ = s.persistExecution(*r, false)
	return r, nil
}

// persistExecution retains the newest ordinary receipt on every storage error.
// The return is reserved for invalid internal ownership, never record health.
func (s *Server) persistExecution(r Receipt, settled bool) error {
	if r.Method == "image.job" {
		return s.Receipts.Save(r)
	}
	if s.executions == nil {
		s.executions = map[string]*liveReceipt{}
	}
	e := s.executions[r.ID]
	if e == nil {
		return errors.New("original live receipt registration is unavailable")
	}
	if !sameAdmission(*e.receipt, r) {
		return errors.New("receipt update does not belong to its original admission")
	}
	*e.receipt = r
	e.revision++
	if settled && !e.settled {
		logging.L().Info("Operation ended", "actor", logging.Controller, "request", r.ID, "method", r.Method, "result", r.Status, "state", r.State)
	}
	e.settled = e.settled || settled
	return s.publishExecution(e)
}

func (s *Server) publishExecution(e *liveReceipt) error {
	r := *e.receipt
	if s.nativeStorageFailure != nil {
		return s.deferExecution(e, s.nativeStorageFailure)
	}
	if e.unpublished {
		old, err := s.Receipts.Read(r.ID)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			e.unpublished = false
		case err != nil:
			return s.deferExecution(e, err)
		case sameAdmission(old, r):
			e.unpublished = false
		default:
			e.collision = &old
			e.unpublished = false
			if s.collisions == nil {
				s.collisions = map[string]receiptCollision{}
			}
			s.collisions[r.ID] = receiptCollision{later: r, historical: old}
		}
	}
	if e.collision != nil {
		if e.settled {
			logging.L().Warn("Operation result was not stored: request ID belongs to an earlier execution", "actor", logging.Controller, "request", r.ID, "method", r.Method, "result", r.Status)
			note := s.collisions[r.ID]
			note.later = r
			s.collisions[r.ID] = note
			delete(s.executions, r.ID)
		}
		return nil
	}
	stored := r
	stored.RecordedLate = e.late
	if err := s.Receipts.Save(stored); err != nil {
		return s.deferExecution(e, err)
	}
	e.written = e.revision
	if e.settled {
		delete(s.executions, r.ID)
		s.trimRecordedHistory()
	}
	return nil
}

// Retention is best effort after publication, never an admission condition.
// A complete inventory is required before any eviction; live admissions and
// strict residual/lease owners are retained even above the ordinary budget.
func (s *Server) trimRecordedHistory() {
	all, err := s.Receipts.List()
	if err != nil {
		if s.receiptFailure == nil {
			s.receiptFailure = err
		}
		s.wakeRecovery()
		return
	}
	count := 0
	for _, r := range all {
		if !r.legacyImageResidual() || r.State == "pending" {
			count++
		}
	}
	if count <= receiptLimit {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Started.Before(all[j].Started) })
	for _, r := range all {
		if count <= receiptLimit {
			return
		}
		if s.executions[r.ID] != nil || !r.terminal() {
			continue
		}
		if err := safepath.RemoveFile(s.Receipts.Dir, r.ID+".json"); err != nil {
			logging.L().Warn("Operation record retention is deferred", "actor", logging.Controller, "request", r.ID, "error", err)
			return
		}
		count--
	}
}

func (s *Server) deferExecution(e *liveReceipt, err error) error {
	e.late = true
	if s.receiptFailure == nil {
		s.receiptFailure = err
	}
	s.wakeRecovery()
	return nil
}

func (s *Server) ownsExecution(r Receipt) bool {
	e := s.executions[r.ID]
	return e != nil && sameAdmission(*e.receipt, r)
}

// currentReceipt prefers the original live revision even when an older file
// with this ID becomes readable. Callers hold s.mu.
func (s *Server) currentReceipt(id string) (Receipt, error) {
	if e := s.executions[id]; e != nil {
		r := *e.receipt
		r.RecordedLate = e.late
		return r, nil
	}
	if s.nativeStorageFailure != nil {
		return Receipt{}, s.nativeStorageFailure
	}
	return s.Receipts.Read(id)
}

// saveImageJob keeps stage/orphan ownership strict. Memory registers only the
// running fact; failed stage publication returns its own error to the job.
func retainsImageMetadata(r Receipt) bool {
	return r.Image != nil && r.Completion != nil && r.Image.Lease != "" && r.Image.ReleasedAt.IsZero() && r.Image.AbandonedAt.IsZero()
}

func (s *Server) saveImageJob(r Receipt) error {
	existing := s.executions[r.ID]
	if existing != nil && !sameAdmission(*existing.receipt, r) {
		return errors.New("image job ID belongs to another admission")
	}
	err := s.Receipts.Save(r)
	if s.executions == nil {
		s.executions = map[string]*liveReceipt{}
	}
	if r.State != "pending" && !retainsImageMetadata(r) {
		delete(s.executions, r.ID)
	} else if err == nil {
		copy := r
		s.executions[r.ID] = &liveReceipt{receipt: &copy, strict: true, settled: r.State != "pending"}
	}
	return err
}

// RecoverReceipts is coordinated with admission and uses the full admission
// identity. An older record sharing an ID never closes a current execution.
func (s *Server) RecoverReceipts() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Receipts.recoverExcept(s.ownsExecution, s.publishHistorical)
}

func (s *Server) publishHistorical(r Receipt) error {
	if s.nativeStorageFailure != nil {
		return s.deferHistorical(r, s.nativeStorageFailure)
	}
	if r.Method == "image.job" {
		return s.Receipts.Save(r)
	}
	old, err := s.Receipts.Read(r.ID)
	if err == nil && !sameAdmission(old, r) {
		return errors.New("historical receipt path changed admission identity")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s.deferHistorical(r, err)
	}
	r.RecordedLate = r.RecordedLate || s.historical[r.ID].ID != ""
	if err := s.Receipts.Save(r); err != nil {
		return s.deferHistorical(r, err)
	}
	delete(s.historical, r.ID)
	return nil
}

func (s *Server) deferHistorical(r Receipt, err error) error {
	if s.historical == nil {
		s.historical = map[string]Receipt{}
	}
	s.historical[r.ID] = r
	if s.receiptFailure == nil {
		s.receiptFailure = err
	}
	s.wakeRecovery()
	return nil
}

// flushExecutions writes current revisions while their workers are still live.
// It never finalizes, grants permission or removes an active reservation.
func (s *Server) flushExecutions() error {
	for _, e := range s.executions {
		if e.strict || e.collision != nil {
			continue
		}
		if e.written != e.revision || e.unpublished {
			if err := s.publishExecution(e); err != nil {
				return err
			}
		}
	}
	for _, r := range s.historical {
		if err := s.publishHistorical(r); err != nil {
			return err
		}
	}
	for _, e := range s.executions {
		if !e.strict && e.collision == nil && (e.written != e.revision || e.unpublished) {
			return errors.New("operation records are still awaiting publication")
		}
	}
	if len(s.historical) != 0 {
		return errors.New("historical operation records are still awaiting publication")
	}
	return nil
}

// beginImageJob has no receipt-inventory admission budget. Its own identity
// and first job fact remain strict; unlike ordinary records they are working
// state required before preparation can start.
func (s *Server) beginImageJob(id string) (Receipt, error) {
	if s.executions[id] != nil {
		return Receipt{}, fmt.Errorf("image job %s is already registered", id)
	}
	if _, ok := s.ledger[id]; ok {
		return Receipt{}, fmt.Errorf("image job %s has known metadata", id)
	}
	r := Receipt{ID: id, Method: "image.job", Principal: Principal{}, State: "pending", Started: time.Now().UTC(), Generation: s.Hello.Generation, DiskID: s.DiskID, ProofVersion: documentProofVersion, EffectPhase: EffectWaiting}
	if s.executions == nil {
		s.executions = map[string]*liveReceipt{}
	}
	copy := r
	s.executions[id] = &liveReceipt{receipt: &copy, strict: true}
	if err := s.Receipts.Create(r); err != nil {
		// A refused create owns no receipt: returning a populated r would let
		// the job's abandonment/leave paths replace an unrelated existing ID.
		// A post-publication failure is different: this first fact was ours,
		// so its metadata owner must still finalize/reconcile that strict fact.
		var committed *atomicfile.CommitError
		owned := errors.As(err, &committed)
		if !owned && !errors.Is(err, fs.ErrExist) {
			if saved, readErr := s.Receipts.Read(id); readErr == nil && sameAdmission(saved, r) {
				owned = true
			}
		}
		if owned {
			return r, err
		}
		delete(s.executions, id)
		return Receipt{}, err
	}
	return r, nil
}

func receiptCheckKey(r Receipt) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", r.ID, r.Started.UTC().Format(time.RFC3339Nano), r.Generation, r.DiskID, r.Method, r.Principal.Box, r.Principal.Incarnation)
}

func (s *Server) receiptForCheck(checked Receipt) (Receipt, error) {
	if e := s.executions[checked.ID]; e != nil && sameAdmission(*e.receipt, checked) {
		r := *e.receipt
		r.RecordedLate = e.late
		return r, nil
	}
	if r, ok := s.historical[checked.ID]; ok && sameAdmission(r, checked) {
		return r, nil
	}
	if note, ok := s.collisions[checked.ID]; ok && sameAdmission(note.later, checked) {
		return note.later, nil
	}
	if s.nativeStorageFailure != nil {
		return Receipt{}, s.nativeStorageFailure
	}
	return s.Receipts.Read(checked.ID)
}

func (s *Server) publishCheckedReceipt(checked, next Receipt) error {
	if e := s.executions[checked.ID]; e != nil && sameAdmission(*e.receipt, checked) && !e.strict {
		return s.persistExecution(next, e.settled)
	}
	if note, ok := s.collisions[checked.ID]; ok && sameAdmission(note.later, checked) {
		note.later = next
		s.collisions[checked.ID] = note
		logging.L().Info("Interrupted operation observed without a stored receipt", "actor", logging.Controller, "request", next.ID, "method", next.Method, "observation", next.Observation)
		return nil
	}
	return s.publishHistorical(next)
}

// A failed native UUID/owner/live-bind check is not a receipt-store failure.
// Preserve its original refusal independently of best-effort record writes.
// Caller holds s.mu.
func (s *Server) noteNativeStorageFailure(err error) {
	s.nativeStorageFailure = err
	s.nativeStorageRevision++
	s.wakeRecovery()
}

// NativeStorageUnavailable identifies an actual UUID/owner/live-bind failure,
// distinct from unavailable diagnostic record storage.
type NativeStorageUnavailable struct{ Cause error }

func (e *NativeStorageUnavailable) Error() string {
	return "native storage identity is unavailable: " + e.Cause.Error()
}
func (e *NativeStorageUnavailable) Unwrap() error { return e.Cause }
