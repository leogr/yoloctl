package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

const receiptLimit = 256

// Receipt deliberately excludes command arguments, output and payloads, which
// can contain user code or secrets. A pending record after a restart is an
// uncertain outcome, never permission to replay.
type Receipt struct {
	ID            string    `json:"id"`
	Method        string    `json:"method"`
	Principal     Principal `json:"principal"`
	State         string    `json:"state"`
	Status        int       `json:"status"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished,omitempty"`
	Generation    string    `json:"generation"`
	SetupComplete bool      `json:"setup_complete,omitempty"`
	// Empty is the legacy conservative protocol. Waiting proves that the
	// private effect gate never granted permission; started permits effects.
	EffectPhase string `json:"effect_phase,omitempty"`
	// Informational facts about an owned setup's client. They never change
	// the outcome: the controller finished that setup on its own evidence.
	ClientDetached bool `json:"client_detached,omitempty"`
	// DetachSignal names the client signal that ended the client before handoff.
	DetachSignal string `json:"detach_signal,omitempty"`
	// Recovery evidence (ProofVersion >= 1). The scope and verifier are
	// durable before effect permission; proofs precede their writes; the
	// completion checkpoint follows every effect. None holds a payload.
	ProofVersion int         `json:"proof_version,omitempty"`
	DiskID       string      `json:"disk_id,omitempty"`
	Scope        *Scope      `json:"scope,omitempty"`
	Verifier     string      `json:"verifier,omitempty"`
	Proofs       []Proof     `json:"proofs,omitempty"`
	Completion   *Completion `json:"completion,omitempty"`
	Recovery     *Recovery   `json:"recovery,omitempty"`
	Image        *ImagePhase `json:"image,omitempty"`
}

// Receipts is serialized by Server.mu. Finished receipts, including unknown
// command results, are bounded; pending or uncertain receipts are never
// evicted to admit another mutation.
type Receipts struct{ Dir string }

func (s Receipts) Read(id string) (Receipt, error) {
	var r Receipt
	if err := (Request{ID: id, Dash: -1}).Validate(); err != nil {
		return r, err
	}
	raw, err := safepath.ReadFile(s.Dir, id+".json", MaxDocument)
	if err != nil {
		return r, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil || !json.Valid(raw) {
		return r, errors.New("invalid operation receipt")
	}
	if r.ID != id {
		return r, errors.New("operation receipt identity mismatch")
	}
	switch r.State {
	case "pending", "complete", "uncertain", "setup-complete", "acknowledged", "command-uncertain", "recovered":
	default:
		return r, errors.New("invalid operation receipt state")
	}
	if err := r.validateEvidence(); err != nil {
		return r, err
	}
	if r.EffectPhase != "" && r.EffectPhase != EffectWaiting && r.EffectPhase != EffectStarted {
		return r, errors.New("invalid operation receipt effect phase")
	}
	if r.EffectPhase == EffectWaiting && r.SetupComplete {
		return r, errors.New("operation setup completed before effect authorization")
	}
	if r.State == "command-uncertain" && !authorizedCommand(r) {
		return r, errors.New("command outcome lacks authorized exec proof")
	}
	return r, nil
}

// authorizedCommand reports a box exec whose remote command was durably
// permitted. Its lost result concerns that command, not controller-managed
// state. Legacy exec records without the effect phase prove nothing.
func authorizedCommand(r Receipt) bool {
	return r.Method == "box.exec" && r.EffectPhase == EffectStarted && !r.SetupComplete
}

// interrupt classifies an outcome without a proven result, both at
// finalization and at startup recovery. Only durable setup, effect-phase or
// completion proof narrows it; anything else needs controller recovery.
func (r *Receipt) interrupt() {
	switch {
	case r.SetupComplete:
		r.State = "setup-complete"
	case r.EffectPhase == EffectWaiting:
		// The private gate never authorized an effect.
		r.State = "complete"
		if r.Status == 0 {
			r.Status = 2
		}
		if r.ProofVersion > 0 {
			r.recovered(DispositionNotApplied, ProofGateWaiting)
		}
	case r.Completion != nil:
		// Every effect ended with this known result before the loss.
		r.Status = r.Completion.Status
		r.recovered(DispositionRecordedCompletion, ProofEffectsDone)
	case authorizedCommand(*r):
		// The command may have run. Its result stays unknown and is never
		// replayed, but no controller state awaits review.
		r.State = "command-uncertain"
	default:
		r.State = "uncertain"
	}
}

func (r *Receipt) recovered(disposition, proof string) {
	r.State = "recovered"
	r.Recovery = &Recovery{Disposition: disposition, Proof: proof, Version: r.ProofVersion, At: time.Now().UTC()}
}

// terminal reports a receipt that is finished and evictable: no running work
// and no unresolved outcome depend on it.
func (r Receipt) abandonedImage() bool {
	return r.Image != nil && r.Image.PrivateVersion == 1 && !r.Image.AbandonedAt.IsZero()
}

func (r Receipt) terminal() bool {
	// The independent image receipt is also the durable orphan ledger.
	// Request receipt eviction cannot erase possible surviving build work.
	if r.abandonedImage() && r.Image.OrphanRecorded {
		return true
	}
	if r.Image != nil && r.Image.Residual {
		return false
	}

	switch r.State {
	case "complete", "setup-complete", "acknowledged", "command-uncertain", "recovered":
		return true
	}
	return false
}

// Recover runs with the controller singleton held, after the guest supervisor
// (systemd or Firecracker PID 1) has killed former workers. It preserves uncertainty and never replays command input.
func (s Receipts) Recover() (bool, error) {
	if _, err := s.ImageOrphans(); err != nil {
		return false, err
	}
	all, err := s.List()
	if err != nil {
		return false, err
	}
	interrupted := false
	for _, r := range all {
		if r.Image != nil && r.Image.PrivateVersion == 1 && r.Completion == nil && r.Image.AbandonedAt.IsZero() {
			r.Image.AbandonedAt, r.Image.Residual = time.Now().UTC(), true
			r.State, r.Status, r.Finished = "uncertain", 2, time.Now().UTC()
			if r.Image.Stage == "preflight" {
				r.Image.Residual = false
				r.State = "complete"
				r.Completion = &Completion{At: r.Finished, Status: 2}
			} else {
				if err := s.SaveImageOrphan(r); err != nil {
					return false, err
				}
				r.Image.OrphanRecorded = true
			}
			if err := s.Save(r); err != nil {
				return false, err
			}
		}
		if r.State == "pending" {
			// Old workers are already terminated by the supervisor.
			r.interrupt()
			r.Finished = time.Now().UTC()
			if err := s.Save(r); err != nil {
				return false, err
			}
		}
		interrupted = interrupted || r.unresolved()
	}
	return interrupted, nil
}

func (s Receipts) Save(r Receipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeReceipt(s.Dir, r.ID+".json", raw, 0o600)
}

// writeReceipt is a fault-injection seam; production writes durably.
var writeReceipt = safepath.WriteFile

func (s Receipts) Begin(req Request, p Principal, generation string) (Receipt, error) {
	return s.begin(req, p, generation, "", false)
}

// begin records a pending receipt. A disk identity marks this build's
// evidence schema; without one the record is the legacy unscoped form.
func (s Receipts) begin(req Request, p Principal, generation, disk string, effectGated bool) (Receipt, error) {
	var r Receipt
	if existing, err := s.Read(req.ID); err == nil {
		return r, fmt.Errorf("request %s already recorded (%s); query its outcome instead of replaying", req.ID, existing.State)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return r, err
	}
	all, err := s.List()
	if err != nil {
		return r, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Started.Before(all[j].Started) })
	for len(all) >= receiptLimit {
		removed := false
		for i, item := range all {
			if item.terminal() {
				if err := safepath.RemoveFile(s.Dir, item.ID+".json"); err != nil {
					return r, &receiptWriteError{err}
				}
				all = append(all[:i], all[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return r, errors.New("operation receipts require recovery before more mutations")
		}
	}
	r = Receipt{ID: req.ID, Method: req.Method, Principal: p, State: "pending", Started: time.Now().UTC(), Generation: generation}
	if disk != "" {
		r.ProofVersion, r.DiskID = documentProofVersion, disk
	}
	if effectGated {
		r.EffectPhase = EffectWaiting
	}
	if err := s.Save(r); err != nil {
		return r, &receiptWriteError{err}
	}
	return r, nil
}

func (s Receipts) List() ([]Receipt, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Receipt
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := s.Read(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, nil
}
