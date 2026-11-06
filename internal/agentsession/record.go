// Package agentsession keeps the controller-owned conversation identity of an
// agent box: the conversation yoloctl assigned or resumes, the launch that
// carries it, and what holder evidence says about that launch. The record lives
// on the native disk beside the declarations, never in the manifest. It is
// evidence for argv and display; it grants nothing and stops nothing.
//
// A launch moves allocated → dispatching → launched | closed | uncertain. The
// dispatching write precedes StartHolderUnit and is the proof boundary: a
// launch cannot have started without it. The holder writes the launch token
// into its marker and tombstone, so only evidence carrying the same token
// promotes or closes a launch. Closing a launch never ends its conversation;
// previous lists only conversations that a new one replaced.
package agentsession

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/manifestio"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only record schema this build reads or writes.
const SchemaVersion = 1

// MaxPrevious bounds the replaced conversations a record keeps.
const MaxPrevious = 8

// Launch kinds: a new conversation under a fresh ID, the recorded one, or a
// new conversation whose ID the agent chooses and its first report names.
const (
	KindAssign = "assign"
	KindResume = "resume"
	KindLearn  = "learn"
)

// Assignment sources: how the assigned conversation became the record's.
// Records written before sources existed have none, which means chosen.
const (
	AssignedChosen   = "chosen"
	AssignedLearned  = "learned"
	AssignedSwitched = "switched"
)

// Launch states.
const (
	StateAllocated   = "allocated"
	StateDispatching = "dispatching"
	StateLaunched    = "launched"
	StateClosed      = "closed"
	StateUncertain   = "uncertain"
)

// Resume outcomes. Only agent-reported evidence (D2) leaves unverified.
const (
	OutcomeUnverified = "unverified"
	OutcomeResumed    = "resumed"
	OutcomeMismatch   = "mismatch"
	// OutcomeFailed: the resume launch exited nonzero without any report of
	// a main-session start. The next start requests the same ID again.
	OutcomeFailed = "failed"
)

// Reasons a conversation was replaced.
const (
	WhyNewSession      = "new-session"
	WhyNever           = "never"
	WhyLaunchUncertain = "launch-uncertain"
	WhyAccountChanged  = "account-changed"
	// WhySwitched: the agent last reported another conversation, which the
	// next resume adopted.
	WhySwitched = "switched"
)

// Record is one box's conversation identity. Account and Provider scope it:
// a record whose account or agent differs from the box's declaration is
// stale and never selects argv.
type Record struct {
	SchemaVersion int    `yaml:"schema_version"`
	BoxID         string `yaml:"box_id,omitempty"`
	Account       string `yaml:"account"`
	Provider      string `yaml:"provider"`
	// LastGeneration is the highest launch generation ever issued for this
	// record. It survives retirement, so a generation is never reused: the
	// observation directories and cursors of earlier launches can never be
	// attributed to a newer one.
	LastGeneration uint64    `yaml:"last_generation,omitempty"`
	Assigned       *Assigned `yaml:"assigned,omitempty"`
	// Current is the conversation the agent last reported starting, across
	// launches; the next resume requests it. It is the last observed
	// conversation, never a claim about what the agent shows now.
	Current  *Current   `yaml:"current,omitempty"`
	Launch   *Launch    `yaml:"launch,omitempty"`
	Resume   *Resume    `yaml:"resume,omitempty"`
	Observed *Observed  `yaml:"observed,omitempty"`
	Previous []Previous `yaml:"previous,omitempty"`
}

// Assigned is the record's conversation: the ID yoloctl passed when it was
// first launched (chosen), the ID a learn launch's first report named
// (learned), or the last observed conversation a resume adopted (switched).
// A resume requests Current when it differs, and adopts it here first.
type Assigned struct {
	ID     string    `yaml:"id"`
	At     time.Time `yaml:"at"`
	Source string    `yaml:"source,omitempty"`
}

// Current is the last main-session start the agent reported in a launch of
// this record: its conversation, the start's source, the agent's timestamp,
// the controller's read time and the launch generation of that report. It
// is agent-reported evidence; it selects the next resume and grants nothing.
type Current struct {
	ID         string    `yaml:"id"`
	Source     string    `yaml:"source,omitempty"`
	At         time.Time `yaml:"at"`
	ReadAt     time.Time `yaml:"read_at"`
	Generation uint64    `yaml:"generation"`
}

// Launch is the latest launch of the current conversation.
type Launch struct {
	Generation uint64 `yaml:"generation"`
	Kind       string `yaml:"kind"`
	State      string `yaml:"state"`
	Token      string `yaml:"launch_token"`
	// Operation and Incarnation name the controller request and controller
	// generation that wrote dispatching, for inspection.
	Operation   string    `yaml:"operation,omitempty"`
	Incarnation string    `yaml:"incarnation,omitempty"`
	Holder      string    `yaml:"holder,omitempty"`
	At          time.Time `yaml:"at"`
	Detail      string    `yaml:"detail,omitempty"`
	// ExitCode is the agent's exit status from the launch's own tombstone;
	// absent when it closed otherwise.
	ExitCode *int `yaml:"exit_code,omitempty"`
}

// Resume describes the latest resume request. Exit status and holder
// evidence are details, never a verdict.
type Resume struct {
	Requested  string    `yaml:"requested"`
	Generation uint64    `yaml:"generation"`
	At         time.Time `yaml:"at"`
	Outcome    string    `yaml:"outcome"`
	Detail     string    `yaml:"detail,omitempty"`
}

// Observed is agent-reported evidence about one launch generation. Apply
// (apply.go) owns its updates; it never authorizes anything. At is the
// agent's timestamp of the latest main-session start, ReadAt the
// controller's read time. Cursor and Applied are the reader's position and
// the highest line already applied, so rereads and re-emitted lines change
// nothing.
type Observed struct {
	ID         string         `yaml:"id,omitempty"`
	At         time.Time      `yaml:"at,omitempty"`
	ReadAt     time.Time      `yaml:"read_at,omitempty"`
	Source     string         `yaml:"source,omitempty"`
	Generation uint64         `yaml:"generation,omitempty"`
	Health     string         `yaml:"health,omitempty"`
	Detail     string         `yaml:"detail,omitempty"`
	Model      *ObservedValue `yaml:"model,omitempty"`
	Effort     *ObservedValue `yaml:"effort,omitempty"`
	// Version is the agent CLI's: from status line reports, or a query of
	// the agent's own server (applied only for its own thread).
	Version *ObservedValue   `yaml:"version,omitempty"`
	Context *ObservedContext `yaml:"context,omitempty"`
	// State is the agent's reported state in this launch (see Apply).
	State *ObservedState `yaml:"state,omitempty"`
	// Diverged is the generation's first main-session report of a
	// conversation other than the assigned one: evidence that the agent
	// switched. A later report of the assigned ID never clears it; a new
	// launch or retirement does.
	Diverged *Divergence `yaml:"diverged,omitempty"`
	Cursor   []byte      `yaml:"cursor,omitempty"`
	Applied  []byte      `yaml:"applied,omitempty"`
}

// Divergence is an agent-reported conversation other than the assigned one.
// It is shown as a switch and decides nothing; Current selects the resume.
type Divergence struct {
	ID     string    `yaml:"id"`
	Source string    `yaml:"source,omitempty"`
	At     time.Time `yaml:"at"`
	ReadAt time.Time `yaml:"read_at"`
}

// Observation sources: which agent report last set a value. Records written
// before sources existed have none.
const (
	SourceHook       = "hook"
	SourceStatusLine = "statusline"
	// SourceQuery is the Codex writer's read of the box's own daemon.
	SourceQuery = "query"
)

// ObservedValue is one agent-reported runtime value with its source, the
// agent's timestamp and the controller's read time.
type ObservedValue struct {
	Value  string    `yaml:"value"`
	Source string    `yaml:"source,omitempty"`
	At     time.Time `yaml:"at"`
	ReadAt time.Time `yaml:"read_at"`
	// Session is the conversation the report that set the value named, when
	// it named a valid one: a resume carries only its own conversation's
	// values (see Carried).
	Session string `yaml:"session,omitempty"`
}

// ObservedContext is the agent-reported context window: its size and used
// percentage as reported, and the used tokens when the report had them.
// An agent that reports only the tokens used leaves Size 0 and Percent nil.
type ObservedContext struct {
	Size    int64     `yaml:"size,omitempty"`
	Used    *int64    `yaml:"used,omitempty"`
	Percent *int      `yaml:"percent,omitempty"`
	Source  string    `yaml:"source,omitempty"`
	At      time.Time `yaml:"at"`
	ReadAt  time.Time `yaml:"read_at"`
}

// context is c in the reader's terms, for its bounds.
func (c *ObservedContext) context() observe.Context {
	used, percent := int64(-1), -1
	if c.Used != nil {
		used = *c.Used
	}
	if c.Percent != nil {
		percent = *c.Percent
	}
	return observe.Context{Size: c.Size, Used: used, Percent: percent}
}

// ObservedState is the agent's reported state: one of the observe
// package's states, with the report that set it.
type ObservedState struct {
	Kind   string    `yaml:"kind"`
	Source string    `yaml:"source,omitempty"`
	At     time.Time `yaml:"at"`
	ReadAt time.Time `yaml:"read_at"`
}

func validSource(s string) bool {
	return s == "" || s == SourceHook || s == SourceStatusLine || s == SourceQuery
}

// Previous is a conversation a newer one replaced. A switched one names the
// conversation that replaced it and the reported start's source.
type Previous struct {
	ID      string    `yaml:"id"`
	EndedAt time.Time `yaml:"ended_at"`
	Why     string    `yaml:"why"`
	To      string    `yaml:"to,omitempty"`
	Source  string    `yaml:"source,omitempty"`
}

// Validate checks the record's shape: known schema, states and reasons, and
// conversation IDs and tokens in their canonical forms.
func (r *Record) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported session record schema %d", r.SchemaVersion)
	}
	if err := layout.ValidateName("account", r.Account); err != nil {
		return err
	}
	if err := layout.ValidateName("agent-profile", r.Provider); err != nil {
		return err
	}
	if a := r.Assigned; a != nil {
		if !ValidID(a.ID) {
			return errors.New("session record has an invalid conversation id")
		}
		switch a.Source {
		case "", AssignedChosen, AssignedLearned, AssignedSwitched:
		default:
			return fmt.Errorf("session record has an invalid assignment source %q", a.Source)
		}
	}
	if c := r.Current; c != nil {
		if !ValidID(c.ID) || c.Generation == 0 || c.Generation > r.LastGeneration {
			return errors.New("session record has an invalid current conversation")
		}
		if !CurrentSource(c.Source) {
			return fmt.Errorf("session record has an invalid current conversation source %q", c.Source)
		}
	}
	if l := r.Launch; l != nil {
		if r.Assigned == nil && l.Kind != KindLearn {
			return errors.New("session record has a launch without a conversation")
		}
		if l.Generation == 0 || l.Generation > r.LastGeneration || !session.ValidLaunchToken(l.Token) {
			return errors.New("session record has an invalid launch generation or token")
		}
		if l.Kind != KindAssign && l.Kind != KindResume && l.Kind != KindLearn {
			return fmt.Errorf("session record has an invalid launch kind %q", l.Kind)
		}
		switch l.State {
		case StateAllocated, StateDispatching, StateLaunched, StateClosed, StateUncertain:
		default:
			return fmt.Errorf("session record has an invalid launch state %q", l.State)
		}
	}
	if s := r.Resume; s != nil {
		if !ValidID(s.Requested) {
			return errors.New("session record has an invalid resume request")
		}
		switch s.Outcome {
		case OutcomeUnverified, OutcomeResumed, OutcomeMismatch, OutcomeFailed:
		default:
			return fmt.Errorf("session record has an invalid resume outcome %q", s.Outcome)
		}
	}
	if o := r.Observed; o != nil {
		if o.ID != "" && !ValidID(o.ID) {
			return errors.New("session record has an invalid observed id")
		}
		if o.Health != "" && !observe.ValidHealth(observe.Health(o.Health)) {
			return fmt.Errorf("session record has an invalid observation health %q", o.Health)
		}
		if (o.Model != nil && !observe.ValidModel(o.Model.Value)) || (o.Effort != nil && !observe.ValidEffort(o.Effort.Value)) ||
			(o.Version != nil && !observe.ValidVersion(o.Version.Value)) || (o.Context != nil && (o.Context.Used != nil && *o.Context.Used < 0 || !o.Context.context().Valid())) {
			return errors.New("session record has an invalid observed model, effort, version or context")
		}
		for _, v := range []*ObservedValue{o.Model, o.Effort, o.Version} {
			if v != nil && !validSource(v.Source) {
				return fmt.Errorf("session record has an invalid observation source %q", v.Source)
			}
			if v != nil && v.Session != "" && !ValidID(v.Session) {
				return errors.New("session record has an invalid observed value session")
			}
		}
		if o.Context != nil && !validSource(o.Context.Source) {
			return fmt.Errorf("session record has an invalid observation source %q", o.Context.Source)
		}
		if s := o.State; s != nil && (!observe.ValidState(s.Kind) || s.Source == "" || !validSource(s.Source)) {
			return errors.New("session record has an invalid observed state")
		}
		if o.Diverged != nil && !ValidID(o.Diverged.ID) {
			return errors.New("session record has an invalid diverged conversation id")
		}
	}
	if len(r.Previous) > MaxPrevious {
		return errors.New("session record keeps too many previous conversations")
	}
	for _, p := range r.Previous {
		if !ValidID(p.ID) {
			return errors.New("session record has an invalid previous conversation id")
		}
		switch p.Why {
		case WhyNewSession, WhyNever, WhyLaunchUncertain, WhyAccountChanged, WhySwitched:
		default:
			return fmt.Errorf("session record has an invalid replacement reason %q", p.Why)
		}
		if (p.To != "" && !ValidID(p.To)) || !CurrentSource(p.Source) || (p.Why != WhySwitched && (p.To != "" || p.Source != "")) {
			return errors.New("session record has an invalid switched conversation")
		}
	}
	return nil
}

// CurrentSource reports whether a main-session start of this source sets the
// current conversation: a new, resumed, cleared or forked conversation, or an
// unlabelled start. A compaction continues the same conversation.
func CurrentSource(source string) bool {
	switch source {
	case "", "startup", "resume", "clear", "fork":
		return true
	}
	return false
}

// ValidID reports whether id is a canonical lowercase UUID, the only
// conversation ID format profiles may declare.
func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// NewID returns a fresh random (version 4) conversation ID.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// NewToken returns a fresh 128-bit launch correlation token.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Load reads a box's record. A missing record is (nil, false, nil); an
// unreadable or invalid one is an error, never an empty record.
func Load(space, box string) (*Record, bool, error) {
	path, err := layout.BoxSessionPath(space, box)
	if err != nil {
		return nil, false, err
	}
	raw, err := manifestio.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read session record of box %s: %w", box, err)
	}
	var r Record
	if err := strictyaml.Decode(raw, &r); err != nil {
		return nil, false, fmt.Errorf("session record of box %s: %w", box, err)
	}
	if err := r.Validate(); err != nil {
		return nil, false, fmt.Errorf("session record of box %s: %w", box, err)
	}
	return &r, true, nil
}

// Remove deletes a box's record; a missing one is fine. Box destruction
// calls it so a later box of the same name starts untracked.
func Remove(space, box string) error {
	path, err := layout.BoxSessionPath(space, box)
	if err != nil {
		return err
	}
	if err := manifestio.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// save atomically replaces a box's record. Readers never take locks, so a
// record is only ever replaced whole.
func save(space, box string, r *Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	raw, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	path, err := layout.BoxSessionPath(space, box)
	if err != nil {
		return err
	}
	return manifestio.Write(path, raw, 0o600)
}

// retire moves the assigned conversation, and the last observed one when it
// differs, to Previous and clears every launch field, keeping the newest
// MaxPrevious entries.
func (r *Record) retire(why string, at time.Time) {
	if r.Assigned != nil {
		r.addPrevious(Previous{ID: r.Assigned.ID, EndedAt: at, Why: why})
	}
	if c := r.Current; c != nil && (r.Assigned == nil || c.ID != r.Assigned.ID) {
		r.addPrevious(Previous{ID: c.ID, EndedAt: at, Why: why})
	}
	r.Assigned, r.Current, r.Launch, r.Resume, r.Observed = nil, nil, nil, nil, nil
}

func (r *Record) addPrevious(p Previous) {
	r.Previous = append(r.Previous, p)
	if n := len(r.Previous) - MaxPrevious; n > 0 {
		r.Previous = r.Previous[n:]
	}
}

// Target is the conversation the next resume requests: the last observed one
// when the agent reported one, else the assigned one; "" when there is none.
func (r *Record) Target() string {
	switch {
	case r == nil:
		return ""
	case r.Current != nil:
		return r.Current.ID
	case r.Assigned != nil:
		return r.Assigned.ID
	}
	return ""
}
