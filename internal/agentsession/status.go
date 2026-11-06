package agentsession

import (
	"fmt"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

// Status is a box's conversation identity as views show it: the one public
// JSON shape that box show, box list and box session share. It is read
// without locks from the atomically replaced record and decides nothing. Its
// sections are explicit view types projected from the record, so a record
// field (such as the launch token or an observation cursor) never reaches a
// view unless a view type names it.
type Status struct {
	Provider string `json:"provider"`
	// Policy is the effective policy with its derivation, e.g.
	// "last (default)" or "manual (legacy: agent_extra_args carries --resume)".
	Policy  string `json:"policy"`
	Tracked bool   `json:"tracked"`
	// ID is the assigned conversation; empty until one is assigned.
	ID         string     `json:"id,omitempty"`
	AssignedAt *time.Time `json:"assigned_at,omitempty"`
	// AssignedSource is how ID became the record's: chosen, learned or
	// switched; records predating sources say chosen.
	AssignedSource string `json:"assigned_source,omitempty"`
	// Current is the last conversation the agent reported starting; the
	// next resume requests it.
	Current *CurrentView `json:"current,omitempty"`
	// Selection is whether the agent reports every in-app conversation
	// switch (reported) or not (unknown), as its profile declares.
	Selection string         `json:"selection,omitempty"`
	Launch    *LaunchView    `json:"launch,omitempty"`
	Resume    *ResumeView    `json:"resume,omitempty"`
	Observed  *ObservedView  `json:"observed,omitempty"`
	Previous  []PreviousView `json:"previous,omitempty"`
	// Note explains a state without a record: an untracked running session,
	// a stale record or an unreadable one.
	Note string `json:"note,omitempty"`

	// liveLaunch reports that the running holder's marker carries the
	// recorded launch's token. It is not part of the JSON shape.
	liveLaunch bool
	// learns reports that the agent names new conversations itself (the
	// profile has no assign_args). It is not part of the JSON shape.
	learns bool
}

// CurrentView is the public view of Record.Current.
type CurrentView struct {
	ID         string    `json:"id"`
	Source     string    `json:"source,omitempty"`
	At         time.Time `json:"at"`
	ReadAt     time.Time `json:"read_at"`
	Generation uint64    `json:"generation"`
}

// LaunchView is the public view of Record.Launch, without its token.
type LaunchView struct {
	Generation  uint64    `json:"generation"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Operation   string    `json:"operation,omitempty"`
	Incarnation string    `json:"incarnation,omitempty"`
	Holder      string    `json:"holder,omitempty"`
	At          time.Time `json:"at"`
	Detail      string    `json:"detail,omitempty"`
}

// ResumeView is the public view of Record.Resume.
type ResumeView struct {
	Requested  string    `json:"requested"`
	Generation uint64    `json:"generation"`
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}

// ObservedView is the public view of agent-reported evidence, without the
// reader's cursor or applied position. Every value is labelled by its source
// and times: At is the agent's timestamp, ReadAt the controller's read time.
// The times use omitzero (Go 1.24+), so an unset time is left out rather
// than printed as year 1.
type ObservedView struct {
	ID         string             `json:"id,omitempty"`
	At         time.Time          `json:"at,omitzero"`
	ReadAt     time.Time          `json:"read_at,omitzero"`
	Source     string             `json:"source,omitempty"`
	Generation uint64             `json:"generation,omitempty"`
	Health     string             `json:"health,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	Model      *ObservedValueView `json:"model,omitempty"`
	Effort     *ObservedValueView `json:"effort,omitempty"`
	Version    *ObservedValueView `json:"version,omitempty"`
	Context    *ContextView       `json:"context,omitempty"`
	State      *StateView         `json:"state,omitempty"`
	Diverged   *DivergenceView    `json:"diverged,omitempty"`
	// Live says whether this view read the running launch's reports:
	// "current", or "unavailable: why"; absent when no read was attempted.
	Live string `json:"live,omitempty"`
	// PendingOutcome is an outcome the latest reports would decide. It is
	// agent-reported and pending the persisted fold at the next start.
	PendingOutcome string `json:"pending_outcome,omitempty"`
}

// DivergenceView is the launch's first agent report of a conversation other
// than the assigned one: evidence of a switch, which refuses nothing.
type DivergenceView struct {
	ID     string    `json:"id"`
	Source string    `json:"source,omitempty"`
	At     time.Time `json:"at"`
	ReadAt time.Time `json:"read_at"`
	// Pending marks a divergence seen only by this view, not yet folded.
	Pending bool `json:"pending,omitempty"`
}

// ObservedValueView is one agent-reported runtime value and the report that
// set it (hook or statusline; absent in records predating sources). Launch
// values are never shown here: observed means the agent reported it.
type ObservedValueView struct {
	Value  string    `json:"value"`
	Source string    `json:"source,omitempty"`
	At     time.Time `json:"at"`
	ReadAt time.Time `json:"read_at"`
}

// ContextView is the agent-reported context window: size and used
// percentage, and used tokens when reported. An agent that reports only the
// tokens used has no size or percentage.
type ContextView struct {
	Size    int64     `json:"size,omitempty"`
	Used    *int64    `json:"used,omitempty"`
	Percent *int      `json:"percent,omitempty"`
	Source  string    `json:"source,omitempty"`
	At      time.Time `json:"at"`
	ReadAt  time.Time `json:"read_at"`
}

// StateView is the agent's reported state: working, idle,
// waiting_permission, waiting_input, waiting_dialog or failed.
type StateView struct {
	Kind   string    `json:"kind"`
	Source string    `json:"source,omitempty"`
	At     time.Time `json:"at"`
	ReadAt time.Time `json:"read_at"`
}

// Text is the state as text views print it, such as "waiting for
// permission".
func (s *StateView) Text() string { return StateText(s.Kind) }

// StateText names a state for people.
func StateText(kind string) string {
	switch kind {
	case observe.StateWaitingPermission:
		return "waiting for permission"
	case observe.StateWaitingInput:
		return "waiting for input"
	case observe.StateWaitingDialog:
		return "waiting for a dialog"
	case observe.StateFailed:
		return "API error"
	}
	return kind
}

// PreviousView is the public view of a replaced conversation; a switched one
// names its successor and the reported start's source.
type PreviousView struct {
	ID      string    `json:"id"`
	EndedAt time.Time `json:"ended_at"`
	Why     string    `json:"why"`
	To      string    `json:"to,omitempty"`
	Source  string    `json:"source,omitempty"`
}

// Text is the replacement as text views print it, such as
// "switched to <id> (clear)".
func (p PreviousView) Text() string {
	if p.Why == WhySavedConversationMissing {
		return "saved resume target missing; new conversation selected"
	}
	if p.Why == WhySwitched && p.To != "" {
		return fmt.Sprintf("switched to %s (%s)", p.To, sourceOr(p.Source))
	}
	return p.Why
}

// LiveLaunch reports whether the running holder belongs to the recorded
// launch (its marker carries the launch token).
func (s *Status) LiveLaunch() bool { return s != nil && s.liveLaunch }

// LearnsID reports that the agent names new conversations itself, so a
// record without one learns it from the agent's first report.
func (s *Status) LearnsID() bool { return s != nil && s.learns }

// Switched reports that the last observed conversation differs from the
// assigned one, so the next start resumes it.
func (s *Status) Switched() bool { return s != nil && s.Current != nil && s.Current.ID != s.ID }

// Selection values: whether every in-app conversation switch is reported.
const (
	SelectionReported = agentprofile.SelectionReported
	SelectionUnknown  = agentprofile.SelectionUnknown
)

func launchView(l *Launch) *LaunchView {
	if l == nil {
		return nil
	}
	return &LaunchView{Generation: l.Generation, Kind: l.Kind, State: l.State, Operation: l.Operation, Incarnation: l.Incarnation, Holder: l.Holder, At: l.At, Detail: l.Detail}
}

func resumeView(r *Resume) *ResumeView {
	if r == nil {
		return nil
	}
	return &ResumeView{Requested: r.Requested, Generation: r.Generation, At: r.At, Outcome: r.Outcome, Detail: r.Detail}
}

func observedView(o *Observed) *ObservedView {
	if o == nil {
		return nil
	}
	value := func(v *ObservedValue) *ObservedValueView {
		if v == nil {
			return nil
		}
		// Every field but Session, which only scopes a resume's carry (see
		// Carried) and is not part of the view's output.
		view := ObservedValueView{Value: v.Value, Source: v.Source, At: v.At, ReadAt: v.ReadAt}
		return &view
	}
	view := &ObservedView{ID: o.ID, At: o.At, ReadAt: o.ReadAt, Source: o.Source, Generation: o.Generation,
		Health: o.Health, Detail: o.Detail, Model: value(o.Model), Effort: value(o.Effort), Version: value(o.Version)}
	if c := o.Context; c != nil {
		// A conversion, as for values.
		cv := ContextView(*c)
		if c.Used != nil {
			used := *c.Used
			cv.Used = &used
		}
		if c.Percent != nil {
			percent := *c.Percent
			cv.Percent = &percent
		}
		view.Context = &cv
	}
	if s := o.State; s != nil {
		// A conversion, as for values.
		sv := StateView(*s)
		view.State = &sv
	}
	if d := o.Diverged; d != nil {
		view.Diverged = &DivergenceView{ID: d.ID, Source: d.Source, At: d.At, ReadAt: d.ReadAt}
	}
	return view
}

func previousViews(ps []Previous) []PreviousView {
	var out []PreviousView
	for _, p := range ps {
		// A conversion, so a field added to Previous fails to compile here
		// until the view decides whether to show it.
		out = append(out, PreviousView(p))
	}
	return out
}

// Untracked is the note for a live session no record launched.
const Untracked = "untracked (the running session predates conversation tracking; restart the box to track it)"

// Describe builds a box's status. live reports a running holder and
// liveToken the launch token in its marker ("" for a tokenless or unread
// marker). A record that cannot be read is named, never shown as empty.
func Describe(cfg *agentbox.Config, profile *agentprofile.Profile, live bool, liveToken string) *Status {
	if cfg == nil || !cfg.HasAgent() || profile == nil {
		return nil
	}
	st := &Status{Provider: cfg.Agent}
	policy, err := EffectivePolicy(profile.Session, cfg)
	if err != nil {
		st.Policy, st.Note = "invalid", err.Error()
		return st
	}
	st.Policy, st.Tracked = policy.Describe(), policy.Tracked()
	if policy.Tracked() {
		st.Selection, st.learns = profile.Session.SelectionReport(), len(profile.Session.AssignArgs) == 0
	}
	switch policy.Policy {
	case PolicyUnsupported:
		st.Note = fmt.Sprintf("%s does not declare conversation identity; sessions start as configured", cfg.Agent)
		return st
	case PolicyManual:
		st.Note = "not tracked: agent_extra_args own the conversation, and no box owns it exclusively"
		return st
	}
	rec, ok, err := Load(cfg.Space, cfg.Name)
	switch {
	case err != nil:
		st.Note = "record unavailable: " + err.Error()
		return st
	case !ok:
		if live {
			st.Note = Untracked
		}
		return st
	case rec.Account != cfg.Account || rec.Provider != cfg.Agent || (rec.BoxID != "" && cfg.ID != "" && rec.BoxID != cfg.ID):
		st.Note = fmt.Sprintf("stale record of account %s and agent %s; the next start replaces it", rec.Account, rec.Provider)
		return st
	}
	fold := rec.foldForView(cfg.Name)
	if rec.Assigned != nil {
		st.ID = rec.Assigned.ID
		at := rec.Assigned.At
		st.AssignedAt = &at
		st.AssignedSource = rec.Assigned.Source
		if st.AssignedSource == "" {
			st.AssignedSource = AssignedChosen
		}
	}
	if c := rec.Current; c != nil {
		// A conversion, so a field added to Current fails to compile here
		// until the view decides whether to show it.
		cv := CurrentView(*c)
		st.Current = &cv
	}
	st.Launch, st.Resume, st.Observed, st.Previous = launchView(rec.Launch), resumeView(rec.Resume), observedView(rec.Observed), previousViews(rec.Previous)
	if st.Observed != nil {
		st.Observed.Live, st.Observed.PendingOutcome = fold.live, fold.pendingOutcome
		if st.Observed.Diverged != nil {
			st.Observed.Diverged.Pending = fold.pendingDivergence
		}
	}
	st.liveLaunch = live && rec.Launch != nil && liveToken != "" && liveToken == rec.Launch.Token
	if live && !st.liveLaunch {
		st.Note = Untracked
	}
	if st.Note == "" && rec.Assigned != nil && len(rec.Previous) > 0 {
		previous := rec.Previous[len(rec.Previous)-1]
		if previous.Why == WhySavedConversationMissing {
			st.Note = fmt.Sprintf("no saved conversation found for %s; selected new conversation %s", previous.ID, rec.Assigned.ID)
		}
	}
	return st
}

// notes are the observation's explanations shared by every text view.
func (o *ObservedView) notes() []string {
	var notes []string
	if d := o.Diverged; d != nil {
		note := "the agent switched from the assigned conversation to " + d.ID + " (" + sourceOr(d.Source) + ")"
		if d.Pending {
			note += ", not yet folded"
		}
		notes = append(notes, note)
	}
	if o.PendingOutcome != "" {
		notes = append(notes, "reported "+o.PendingOutcome+", not yet folded")
	}
	if strings.HasPrefix(o.Live, "unavailable") {
		notes = append(notes, "live reports "+o.Live)
	}
	if o.Detail != "" {
		notes = append(notes, o.Detail)
	}
	return notes
}

// Lines renders the whole observation for text views (box session): the
// evidence line, each agent-reported value with its event and read times,
// whether this view's live read succeeded, and the shared notes.
func (o *ObservedView) Lines() []string {
	if o == nil {
		return nil
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	at := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	lines := []string{fmt.Sprintf("%s · %s · generation %d · %s (agent-reported)", dash(o.ID), dash(o.Source), o.Generation, dash(o.Health))}
	reported := func(source string, t time.Time) string {
		if source == "" {
			return "agent-reported at " + at(t)
		}
		return "agent-reported by " + source + " at " + at(t)
	}
	for _, v := range []struct {
		name  string
		value *ObservedValueView
	}{{"model", o.Model}, {"effort", o.Effort}, {"version", o.Version}} {
		if v.value != nil {
			lines = append(lines, fmt.Sprintf("%s %s (%s, read %s)", v.name, v.value.Value, reported(v.value.Source, v.value.At), at(v.value.ReadAt)))
		}
	}
	if c := o.Context; c != nil {
		lines = append(lines, fmt.Sprintf("context %s (%s, read %s)", c.text(), reported(c.Source, c.At), at(c.ReadAt)))
	}
	if st := o.State; st != nil {
		lines = append(lines, fmt.Sprintf("state %s (%s, read %s)", st.Text(), reported(st.Source, st.At), at(st.ReadAt)))
	}
	if o.Live == "current" {
		lines = append(lines, "live reports read by this view")
	}
	return append(lines, o.notes()...)
}

// text is the context in tokens, such as "39763 of 1000000 tokens, 4%".
func (c *ContextView) text() string {
	switch {
	case c.Percent == nil && c.Used != nil:
		return fmt.Sprintf("%d tokens used", *c.Used)
	case c.Percent == nil:
		return "-"
	case c.Used == nil:
		return fmt.Sprintf("%d%% of %d tokens", *c.Percent, c.Size)
	}
	return fmt.Sprintf("%d of %d tokens, %d%%", *c.Used, c.Size, *c.Percent)
}

// Line is the one-line box show summary.
func (s *Status) Line() string {
	if s == nil {
		return ""
	}
	parts := []string{s.Provider}
	if s.ID != "" {
		parts = append(parts, s.ID)
	}
	if l := s.Launch; l != nil {
		state := l.State
		if l.State == StateUncertain && l.Operation != "" {
			state += " (operation " + l.Operation + ")"
		}
		parts = append(parts, fmt.Sprintf("launch %d %s %s", l.Generation, l.Kind, state))
	}
	if r := s.Resume; r != nil && s.Launch != nil && r.Generation == s.Launch.Generation {
		parts = append(parts, "resume "+r.Outcome)
	}
	notes := []string{}
	if o := s.Observed; o != nil && s.Launch != nil && o.Generation == s.Launch.Generation {
		observed := "observation " + o.Health
		var values []string
		if o.Model != nil {
			values = append(values, "model "+o.Model.Value)
		}
		if o.Effort != nil {
			values = append(values, "effort "+o.Effort.Value)
		}
		switch c := o.Context; {
		case c != nil && c.Percent != nil:
			values = append(values, fmt.Sprintf("context %d%%", *c.Percent))
		case c != nil && c.Used != nil:
			values = append(values, fmt.Sprintf("context %d tokens used", *c.Used))
		}
		if o.State != nil {
			values = append(values, "state "+o.State.Text())
		}
		if len(values) > 0 {
			observed += " (agent-reported " + strings.Join(values, ", ") + ")"
		}
		parts = append(parts, observed)
		notes = append(notes, o.notes()...)
	}
	parts = append(parts, "policy "+s.Policy)
	line := strings.Join(parts, " · ")
	if s.Note != "" {
		notes = append([]string{s.Note}, notes...)
	}
	if len(notes) > 0 {
		line += " — " + strings.Join(notes, "; ")
	}
	return line
}
