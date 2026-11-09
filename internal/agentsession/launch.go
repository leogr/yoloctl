package agentsession

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

// BoxLock is the box entity lock. Every record write requires it. Box
// lifecycle and attach commands already hold the native space-operation lock
// when they take it; nothing here acquires the space lock.
type BoxLock struct {
	space, box string
	release    func()
}

// LockBox takes the box entity lock. Callers that already hold it (the same
// process cannot take it twice) must pass their existing BoxLock instead.
func LockBox(space, box string) (*BoxLock, error) {
	release, err := agentbox.LockConfig(space, box)
	if err != nil {
		return nil, err
	}
	return &BoxLock{space: space, box: box, release: release}, nil
}

// Release drops the lock; a second call is a no-op.
func (l *BoxLock) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

func (l *BoxLock) held() error {
	if l == nil || l.release == nil {
		return errors.New("session record changed without its box lock")
	}
	return nil
}

// Space and Box name the locked box.
func (l *BoxLock) Space() string { return l.space }
func (l *BoxLock) Box() string   { return l.box }

// Scope is the identity a box's record must carry.
type Scope struct {
	BoxID, Account, Provider string
}

// LoadLocked reads the box's record under its lock. A record of another
// account or agent is stale: its conversation is retired (account-changed)
// and the record is rewritten for scope, so it never selects argv.
func LoadLocked(l *BoxLock, scope Scope, now time.Time) (*Record, error) {
	if err := l.held(); err != nil {
		return nil, err
	}
	rec, ok, err := Load(l.space, l.box)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if rec.Account == scope.Account && rec.Provider == scope.Provider && (rec.BoxID == "" || scope.BoxID == "" || rec.BoxID == scope.BoxID) {
		return rec, nil
	}
	rec.retire(WhyAccountChanged, now)
	rec.BoxID, rec.Account, rec.Provider = scope.BoxID, scope.Account, scope.Provider
	return rec, Save(l, rec)
}

// Save replaces the box's record under its lock.
func Save(l *BoxLock, rec *Record) error {
	if err := l.held(); err != nil {
		return err
	}
	return save(l.space, l.box, rec)
}

// RemoveLocked deletes the box's record under its lock.
func RemoveLocked(l *BoxLock) error {
	if err := l.held(); err != nil {
		return err
	}
	return Remove(l.space, l.box)
}

// NewRecord starts an empty record for scope.
func NewRecord(scope Scope) *Record {
	return &Record{SchemaVersion: SchemaVersion, BoxID: scope.BoxID, Account: scope.Account, Provider: scope.Provider}
}

// Pending reports whether the record's launch has not reached a settled
// state: allocated, dispatching or uncertain.
func (r *Record) Pending() bool {
	return r != nil && r.Launch != nil && (r.Launch.State == StateAllocated || r.Launch.State == StateDispatching || r.Launch.State == StateUncertain)
}

// Retire replaces the current conversation: it moves to Previous with why,
// and the next launch assigns a fresh ID.
func (r *Record) Retire(why string, now time.Time) { r.retire(why, now) }

// Allocate reserves the next launch. A launch that is still allocated was
// never dispatched, so it is reused once with the same generation and token.
// Otherwise kind assign mints a fresh conversation ID, kind learn starts one
// the agent names in its first report, and kind resume requests Target, each
// under a new generation and token. A resume whose target is a last observed
// conversation other than the assigned one first adopts it: the assigned one
// moves to Previous as switched, so the request, the argv and the ownership
// check name one conversation.
func (r *Record) Allocate(kind string, now time.Time) error {
	if r.Launch != nil && r.Launch.State == StateAllocated {
		if r.Launch.Kind != kind {
			return fmt.Errorf("allocated launch is a %s, not a %s", r.Launch.Kind, kind)
		}
		return nil
	}
	if r.Pending() {
		return errors.New("the previous launch is unresolved")
	}
	if r.LastGeneration == math.MaxUint64 {
		return errors.New("this conversation record has issued its last launch generation; no further launch can be recorded")
	}
	token, err := NewToken()
	if err != nil {
		return err
	}
	generation := r.LastGeneration + 1
	switch kind {
	case KindAssign:
		id, err := NewID()
		if err != nil {
			return err
		}
		r.Assigned = &Assigned{ID: id, At: now, Source: AssignedChosen}
		r.Current, r.Resume, r.Observed = nil, nil, nil
	case KindLearn:
		if r.Assigned != nil {
			return errors.New("a learn launch needs a record without a conversation")
		}
		r.Current, r.Resume, r.Observed = nil, nil, nil
	case KindResume:
		if r.Assigned == nil {
			return errors.New("no conversation to resume")
		}
		if c := r.Current; c != nil && c.ID != r.Assigned.ID {
			r.addPrevious(Previous{ID: r.Assigned.ID, EndedAt: now, Why: WhySwitched, To: c.ID, Source: c.Source})
			r.Assigned = &Assigned{ID: c.ID, At: now, Source: AssignedSwitched}
		}
		r.Resume = &Resume{Requested: r.Assigned.ID, Generation: generation, At: now, Outcome: OutcomeUnverified}
	default:
		return fmt.Errorf("unknown launch kind %q", kind)
	}
	r.Launch = &Launch{Generation: generation, Kind: kind, State: StateAllocated, Token: token, At: now}
	r.LastGeneration = generation
	return nil
}

// Dispatch marks the allocated launch as about to start. It must be saved
// before StartHolderUnit runs.
func (r *Record) Dispatch(generation uint64, token, operation, incarnation string, now time.Time) error {
	if err := r.current(generation, token); err != nil {
		return err
	}
	if r.Launch.State != StateAllocated {
		return fmt.Errorf("launch generation %d is %s, not allocated", generation, r.Launch.State)
	}
	r.Launch.State, r.Launch.Operation, r.Launch.Incarnation, r.Launch.At = StateDispatching, operation, incarnation, now
	return nil
}

// Bind settles a dispatched launch with the holder whose marker carries the
// launch's token.
func (r *Record) Bind(token, holder string, now time.Time) error {
	if r.Launch == nil || r.Launch.Token != token || token == "" {
		return errors.New("holder evidence belongs to another launch")
	}
	switch r.Launch.State {
	case StateDispatching, StateUncertain:
		r.Launch.State, r.Launch.Holder, r.Launch.At, r.Launch.Detail = StateLaunched, holder, now, ""
	case StateLaunched:
	default:
		return fmt.Errorf("launch generation %d is %s; a holder cannot bind it", r.Launch.Generation, r.Launch.State)
	}
	return nil
}

// CloseExit closes the launch with its own tombstone's exit status, which a
// later SettleResume reads.
func (r *Record) CloseExit(token string, code int, exitedAt, now time.Time) error {
	detail := fmt.Sprintf("agent exited with status %d at %s", code, exitedAt.UTC().Format(time.RFC3339))
	closed := r.Launch != nil && r.Launch.State == StateClosed
	if err := r.Close(token, detail, now); err != nil || closed {
		return err
	}
	r.Launch.ExitCode = &code
	return nil
}

// SettleResume decides a failed resume once the generation's reports are
// read: a closed resume launch that exited nonzero, while its readable
// reports (health ok or no-report) name no main-session conversation, is
// failed. A start report always decides the outcome itself; a status line
// that named one (see Fold) leaves it unverified, never failed.
// It reports whether the record changed. The next start requests the same
// conversation again; nothing here starts a new one.
func (r *Record) SettleResume() bool {
	l, s, o := r.Launch, r.Resume, r.Observed
	if l == nil || s == nil || l.Kind != KindResume || l.State != StateClosed || l.ExitCode == nil || *l.ExitCode == 0 ||
		s.Generation != l.Generation || s.Outcome != OutcomeUnverified || o == nil || o.Generation != l.Generation || o.ID != "" ||
		(o.Health != string(observe.HealthOK) && o.Health != string(observe.HealthNoReport)) {
		return false
	}
	s.Outcome, s.Detail = OutcomeFailed, l.Detail
	return true
}

// Close ends the launch whose tombstone carries its token. The conversation
// stays current and resumable.
func (r *Record) Close(token, detail string, now time.Time) error {
	if r.Launch == nil || r.Launch.Token != token || token == "" {
		return errors.New("exit evidence belongs to another launch")
	}
	switch r.Launch.State {
	case StateDispatching, StateLaunched, StateUncertain:
		r.Launch.State, r.Launch.At, r.Launch.Detail = StateClosed, now, detail
		r.noteResumeDetail(detail)
	case StateClosed:
	default:
		return fmt.Errorf("launch generation %d is %s; an exit cannot close it", r.Launch.Generation, r.Launch.State)
	}
	return nil
}

// SandboxReplaced applies proof that no holder or agent of this box can
// survive: a positively stopped or missing runtime was started or reconciled
// again, or the container was recreated. A launched launch closes. An
// interrupted resume closes too, because its conversation existed before it.
// An interrupted first assignment may never have created its conversation,
// so it stays uncertain until an explicit new session.
func (r *Record) SandboxReplaced(detail string, now time.Time) bool {
	if r == nil || r.Launch == nil {
		return false
	}
	l := r.Launch
	switch {
	case l.State == StateClosed, l.State == StateAllocated:
		// Already ended, or never dispatched: the reservation stays usable.
		return false
	case l.State == StateLaunched, l.Kind == KindResume:
		l.State, l.At, l.Detail = StateClosed, now, detail
		r.noteResumeDetail(detail)
		return true
	case l.State == StateUncertain:
		return false
	default:
		l.State, l.At = StateUncertain, now
		l.Detail = "the first launch of this conversation was interrupted (" + detail + "); whether the conversation exists is unknown"
		return true
	}
}

// Uncertain records that a dispatched launch has no holder evidence.
func (r *Record) Uncertain(generation uint64, token, detail string, now time.Time) error {
	if err := r.current(generation, token); err != nil {
		return err
	}
	r.Launch.State, r.Launch.At, r.Launch.Detail = StateUncertain, now, detail
	return nil
}

func (r *Record) current(generation uint64, token string) error {
	if r.Launch == nil || r.Launch.Generation != generation || r.Launch.Token != token {
		return fmt.Errorf("launch generation %d is no longer current", generation)
	}
	return nil
}

func (r *Record) noteResumeDetail(detail string) {
	if r.Resume != nil && r.Launch != nil && r.Resume.Generation == r.Launch.Generation && r.Resume.Outcome == OutcomeUnverified {
		r.Resume.Detail = detail
	}
}
