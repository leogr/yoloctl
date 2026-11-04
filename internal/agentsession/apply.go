package agentsession

import (
	"fmt"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

// Observation of the current launch. Every value here is agent-reported:
// a sandbox process can write any line into its generation directory, so a
// report decides only display and the resume outcome label, and a hostile
// writer can at most make the user choose --new-session.

// ReadCurrentReports reads a box's unread reports of generation gen for
// read-only views. The controller sets it; elsewhere it is nil and views show
// only the persisted evidence.
var ReadCurrentReports func(box string, gen uint64, cur observe.Cursor) (observe.Result, error)

// viewFold is what a read-time fold adds to a view.
type viewFold struct {
	// live is "current" after a successful read, or "unavailable: why";
	// empty when no read was attempted (no reader, or nothing observable).
	live string
	// pendingOutcome is an outcome the unfolded reports would decide; the
	// view shows it beside the persisted one and never decides it.
	pendingOutcome string
	// pendingDivergence marks a divergence seen only at view time.
	pendingDivergence bool
}

// foldForView applies the current launch's unread reports to this freshly
// loaded record, which a view never saves: show, list and session see what
// the running agent reported without writes or locks, under the same rules
// and the persisted high-water mark. Values keep the agent's event time;
// read_at is this read's sampling time. The persisted outcome stands: a
// report that would change it is returned as pending, and a divergence seen
// only now is marked pending. The next start's persisted fold decides both.
func (r *Record) foldForView(box string) viewFold {
	if ReadCurrentReports == nil || r.Launch == nil || !r.Observable(r.Launch.Generation) {
		return viewFold{}
	}
	gen := r.Launch.Generation
	res, err := ReadCurrentReports(box, gen, r.ObservationCursor(gen))
	if err != nil {
		return viewFold{live: "unavailable: " + err.Error()}
	}
	var persisted *Resume
	if r.Resume != nil {
		saved := *r.Resume
		persisted = &saved
	}
	diverged := r.Divergence() != nil
	r.Apply(res)
	r.SettleResume()
	fold := viewFold{live: "current", pendingDivergence: !diverged && r.Divergence() != nil}
	if persisted != nil && r.Resume.Outcome != persisted.Outcome {
		fold.pendingOutcome = r.Resume.Outcome
		*r.Resume = *persisted
	}
	return fold
}

// BeginObservation starts the record's observation of launch generation gen
// with the health its preparation reached (no-report once the directory is
// ready, unsupported or unavailable otherwise). Values of an earlier
// generation are never carried into a new launch.
func (r *Record) BeginObservation(gen uint64, health observe.Health, detail string) {
	r.Observed = &Observed{Generation: gen, Health: string(health), Detail: detail}
}

// ObservationCursor is where the next read of generation gen starts.
func (r *Record) ObservationCursor(gen uint64) observe.Cursor {
	var cur observe.Cursor
	if o := r.Observed; o != nil && o.Generation == gen && len(o.Cursor) > 0 && cur.UnmarshalText(o.Cursor) == nil && cur.Generation == gen {
		return cur
	}
	return observe.Cursor{Generation: gen}
}

// Observable reports whether generation gen's directory can hold reports:
// a launch whose preparation found the container unsupported or the root
// unavailable has none to read.
func (r *Record) Observable(gen uint64) bool {
	o := r.Observed
	return o == nil || o.Generation != gen || (o.Health != string(observe.HealthUnsupported) && o.Health != string(observe.HealthUnavailable))
}

// Apply folds one read of the current launch's reports into the record and
// reports whether the record changed.
//
//   - Only the current launch's generation counts; reports of any other
//     generation never promote or change it.
//   - Subagent-tagged lines are kept out of identity, outcome, model and
//     effort: their session ID can name a later conversation.
//   - Lines at or before the highest one already applied are ignored, so a
//     reread or a rewritten file never re-stamps or changes applied values.
//   - The resume outcome is decided once per generation, by the first main
//     SessionStart naming a conversation: resumed only for source resume with
//     the requested ID, mismatch otherwise. A later report never changes it.
//   - Every main SessionStart naming a conversation with a source other than
//     compact sets Current, the last observed conversation that the next
//     resume requests; the same conversation again only refreshes its times
//     and generation. A learn launch's first one also becomes the assigned
//     conversation (learned).
//   - Separately, the first main report of a conversation other than the
//     assigned one is kept as the generation's divergence, even if a later
//     report returns to the assigned one. It is evidence of a switch shown
//     to the user; it refuses nothing.
//   - Model and effort change only when a report carries them; an absent
//     field leaves the value unknown or as last reported in this generation.
//     Hooks report the model on a startup SessionStart or a PostModelSwitch
//     and the effort on a Stop; every status line reports model, effort,
//     version and context, and is the only source of the last two. Status
//     lines never change conversation identity or the resume outcome.
//   - Health keeps the worst read of the generation and never changes the
//     outcome.
func (r *Record) Apply(res observe.Result) bool {
	if r.Launch == nil || res.Cursor.Generation != r.Launch.Generation {
		return false
	}
	gen := r.Launch.Generation
	changed := false
	o := r.Observed
	if o == nil || o.Generation != gen {
		r.BeginObservation(gen, observe.HealthNoReport, "")
		o, changed = r.Observed, true
	}
	applied := observe.Cursor{Generation: gen}
	if len(o.Applied) > 0 {
		var c observe.Cursor
		if c.UnmarshalText(o.Applied) == nil && c.Generation == gen {
			applied = c
		}
	}
	high := applied
	for _, ob := range res.Observations {
		if ob.Generation != gen || !after(ob, applied) {
			continue
		}
		if after(ob, high) {
			high = observe.Cursor{Generation: gen, K: ob.K, Offset: ob.Offset}
		}
		if ob.Subagent {
			continue
		}
		changed = r.applyLine(o, ob) || changed
	}
	if high != applied {
		o.Applied, _ = high.MarshalText()
		changed = true
	}
	if h := string(observe.Worse(observe.Health(o.Health), res.Health)); h != o.Health {
		o.Health, changed = h, true
	}
	if cursor, _ := res.Cursor.MarshalText(); string(cursor) != string(o.Cursor) {
		o.Cursor, changed = cursor, true
	}
	return changed
}

// Divergence reports the current launch's recorded divergence: the agent
// named another conversation than the assigned one at some point of this
// launch. Views show it as a switch. A new launch, or retiring the
// conversation, clears it; a report returning to the assigned ID never does.
func (r *Record) Divergence() *Divergence {
	if o := r.Observed; o != nil && r.Launch != nil && o.Generation == r.Launch.Generation {
		return o.Diverged
	}
	return nil
}

// currentConversation is the conversation folded so far: the current one,
// else the assigned one, else none.
func (r *Record) currentConversation() string {
	switch {
	case r.Current != nil:
		return r.Current.ID
	case r.Assigned != nil:
		return r.Assigned.ID
	}
	return ""
}

func after(ob observe.Observation, c observe.Cursor) bool {
	return ob.K > c.K || (ob.K == c.K && ob.Offset > c.Offset)
}

// applyLine applies one main-session report of the current generation.
func (r *Record) applyLine(o *Observed, ob observe.Observation) bool {
	if ob.Event == observe.Query && (ob.SessionID == "" || ob.SessionID != r.currentConversation()) {
		// A read of another thread (a switch since, or before it was
		// folded) is not this conversation's evidence.
		return false
	}
	changed := false
	source := SourceHook
	switch ob.Event {
	case observe.StatusLine:
		source = SourceStatusLine
	case observe.Query:
		source = SourceQuery
	}
	value := func(v string) *ObservedValue {
		return &ObservedValue{Value: v, Source: source, At: ob.AgentTS, ReadAt: ob.ReadAt}
	}
	switch ob.Event {
	case "SessionStart":
		if ob.SessionID != "" {
			o.ID, o.Source, o.At, o.ReadAt = ob.SessionID, ob.Source, ob.AgentTS, ob.ReadAt
			changed = true
			if r.Assigned == nil && r.Launch.Kind == KindLearn {
				r.Assigned = &Assigned{ID: ob.SessionID, At: ob.AgentTS, Source: AssignedLearned}
			}
			if CurrentSource(ob.Source) {
				if c := r.Current; c != nil && c.ID == ob.SessionID {
					c.At, c.ReadAt, c.Generation = ob.AgentTS, ob.ReadAt, o.Generation
				} else {
					r.Current = &Current{ID: ob.SessionID, Source: ob.Source, At: ob.AgentTS, ReadAt: ob.ReadAt, Generation: o.Generation}
				}
			}
			if r.Assigned != nil && ob.SessionID != r.Assigned.ID && o.Diverged == nil {
				o.Diverged = &Divergence{ID: ob.SessionID, Source: ob.Source, At: ob.AgentTS, ReadAt: ob.ReadAt}
			}
			if s := r.Resume; s != nil && s.Generation == o.Generation && s.Outcome == OutcomeUnverified {
				if ob.Source == "resume" && ob.SessionID == s.Requested {
					s.Outcome = OutcomeResumed
					s.Detail = fmt.Sprintf("the agent reported resuming %s at %s", ob.SessionID, ob.AgentTS.UTC().Format("2006-01-02T15:04:05Z"))
				} else {
					s.Outcome = OutcomeMismatch
					s.Detail = fmt.Sprintf("the agent reported a %s start of conversation %s instead of %s", sourceOr(ob.Source), ob.SessionID, s.Requested)
				}
			}
		}
		if ob.Model != "" {
			o.Model, changed = value(ob.Model), true
		}
	case "PostModelSwitch":
		if ob.Model != "" {
			o.Model, changed = value(ob.Model), true
		}
	case "Stop":
		// Claude reports the effort here; Codex hooks report the model.
		if ob.Model != "" {
			o.Model, changed = value(ob.Model), true
		}
		if ob.Effort != "" {
			o.Effort, changed = value(ob.Effort), true
		}
	case observe.Query:
		o.Model, o.Effort, changed = value(ob.Model), value(ob.Effort), true
	case observe.StatusLine:
		if ob.Model != "" {
			o.Model, changed = value(ob.Model), true
		}
		if ob.Effort != "" {
			o.Effort, changed = value(ob.Effort), true
		}
		if ob.Version != "" {
			o.Version, changed = value(ob.Version), true
		}
	}
	if c := ob.Context; c != nil {
		o.Context = &ObservedContext{Size: c.Size, Source: source, At: ob.AgentTS, ReadAt: ob.ReadAt}
		if c.Percent >= 0 {
			percent := c.Percent
			o.Context.Percent = &percent
		}
		if c.Used >= 0 {
			used := c.Used
			o.Context.Used = &used
		}
		changed = true
	}
	switch {
	case ob.Event == "PostModelRequest" && ob.State == observe.StateWorking:
		// A completed model request only clears a failed one of this
		// conversation: Codex, the only writer of these lines, retries a
		// failed request itself. An async completion landing after a Stop or
		// a wait changes nothing.
		if o.State != nil && o.State.Kind == observe.StateFailed && ob.SessionID != "" && ob.SessionID == r.currentConversation() {
			o.State, changed = &ObservedState{Kind: ob.State, Source: source, At: ob.AgentTS, ReadAt: ob.ReadAt}, true
		}
	case ob.State != "":
		o.State, changed = &ObservedState{Kind: ob.State, Source: source, At: ob.AgentTS, ReadAt: ob.ReadAt}, true
	case (ob.Event == observe.StatusLine || ob.Event == observe.Query) && o.State != nil && observe.Waiting(o.State.Kind):
		// The agent is running again: Claude's status line hides while a
		// prompt is open and runs on the next assistant message.
		o.State, changed = &ObservedState{Kind: observe.StateWorking, Source: source, At: ob.AgentTS, ReadAt: ob.ReadAt}, true
	}
	return changed
}

func sourceOr(source string) string {
	if source == "" {
		return "unlabelled"
	}
	return source
}
