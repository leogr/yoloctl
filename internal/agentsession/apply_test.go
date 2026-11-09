package agentsession

import (
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

const (
	convA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	convB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// resuming is a record whose generation-3 launch requested convA.
func resuming() *Record {
	at := time.Unix(1000, 0).UTC()
	token, err := NewToken()
	if err != nil {
		panic(err)
	}
	return &Record{
		SchemaVersion: SchemaVersion, Account: "default", Provider: "claude", LastGeneration: 3,
		Assigned: &Assigned{ID: convA, At: at},
		Launch:   &Launch{Generation: 3, Kind: KindResume, State: StateLaunched, Token: token, At: at},
		Resume:   &Resume{Requested: convA, Generation: 3, At: at, Outcome: OutcomeUnverified},
	}
}

// line is one report of generation gen ending at offset off of file 0.
func line(gen uint64, off int64, event string, edit func(*observe.Observation)) observe.Observation {
	o := observe.Observation{Generation: gen, Offset: off, Event: event, AgentTS: time.Unix(2000+off, 0).UTC(), ReadAt: time.Unix(3000, 0).UTC()}
	if edit != nil {
		edit(&o)
	}
	return o
}

func result(gen uint64, health observe.Health, obs ...observe.Observation) observe.Result {
	end := int64(0)
	if len(obs) > 0 {
		end = obs[len(obs)-1].Offset
	}
	return observe.Result{Observations: obs, Cursor: observe.Cursor{Generation: gen, Offset: end}, Health: health}
}

func start(id, source string) func(*observe.Observation) {
	return func(o *observe.Observation) { o.SessionID, o.Source = id, source }
}

func TestApplyResumedOnlyFromTheRequestedMainSessionStart(t *testing.T) {
	r := resuming()
	sub := line(3, 10, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Subagent = convA, "resume", true })
	if !r.applyDefault(result(3, observe.HealthOK, sub)) || r.Resume.Outcome != OutcomeUnverified || r.Observed.ID != "" {
		t.Fatalf("a subagent report decides nothing: %+v %+v", r.Resume, r.Observed)
	}
	if !r.applyDefault(result(3, observe.HealthOK, line(3, 20, "SessionStart", start(convA, "resume")))) || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("the requested resume is reported: %+v", r.Resume)
	}
	// A later /clear names another conversation: the outcome stays, the
	// observed id follows the agent.
	r.applyDefault(result(3, observe.HealthOK, line(3, 30, "SessionStart", start(convB, "clear"))))
	if r.Resume.Outcome != OutcomeResumed || r.Observed.ID != convB || r.Observed.Source != "clear" {
		t.Fatalf("a valid outcome is never changed by a later report: %+v %+v", r.Resume, r.Observed)
	}
}

func TestApplyMismatchKeepsBothIDs(t *testing.T) {
	for _, source := range []string{"startup", "resume", "fork", "clear"} {
		r := resuming()
		r.applyDefault(result(3, observe.HealthOK, line(3, 10, "SessionStart", start(convB, source))))
		if r.Resume.Outcome != OutcomeMismatch || r.Resume.Requested != convA || r.Observed.ID != convB {
			t.Errorf("%s: %+v %+v", source, r.Resume, r.Observed)
		}
		r.applyDefault(result(3, observe.HealthOK, line(3, 20, "SessionStart", start(convA, "resume"))))
		if r.Resume.Outcome != OutcomeMismatch {
			t.Errorf("%s: a mismatch is never downgraded or upgraded later: %+v", source, r.Resume)
		}
	}
}

func TestApplyIgnoresOtherGenerationsAndStartsNewOnesEmpty(t *testing.T) {
	r := resuming()
	if r.applyDefault(result(2, observe.HealthOK, line(2, 10, "SessionStart", start(convA, "resume")))) || r.Observed != nil || r.Resume.Outcome != OutcomeUnverified {
		t.Fatalf("an earlier generation never promotes this launch: %+v", r)
	}
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Model = convA, "resume", "" }),
		line(3, 20, "Stop", func(o *observe.Observation) { o.Effort = "high" })))
	if r.Observed.Model != nil || r.Observed.Effort.Value != "high" {
		t.Fatalf("a missing model stays unknown: %+v", r.Observed)
	}
	// The next launch (generation 4) begins without generation 3's values.
	r.Launch = &Launch{Generation: 4, Kind: KindResume, State: StateLaunched, Token: "u", At: r.Launch.At}
	r.Resume = &Resume{Requested: convA, Generation: 4, At: r.Launch.At, Outcome: OutcomeUnverified}
	r.applyDefault(result(4, observe.HealthNoReport))
	if r.Observed.Generation != 4 || r.Observed.Effort != nil || r.Observed.ID != "" {
		t.Fatalf("prior-generation values are never carried as newly observed: %+v", r.Observed)
	}
}

func TestApplyRereadsAndReemissionChangeNothing(t *testing.T) {
	r := resuming()
	first := result(3, observe.HealthOK,
		line(3, 10, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Model = convA, "resume", "claude-sonnet-5-5" }),
		line(3, 20, "Stop", func(o *observe.Observation) { o.Effort = "low" }))
	r.applyDefault(first)
	stamped := *r.Observed.Effort
	if r.applyDefault(first) {
		t.Fatal("the same read applied again changes the record")
	}
	// A rewritten file is reread from its start: earlier positions with other
	// content never re-stamp or replace applied values.
	rewritten := result(3, observe.HealthGap,
		line(3, 10, "SessionStart", start(convB, "startup")),
		line(3, 20, "Stop", func(o *observe.Observation) { o.Effort, o.AgentTS = "max", time.Unix(9999, 0) }),
		line(3, 30, "PostModelSwitch", func(o *observe.Observation) { o.Model = "claude-opus-5-5" }))
	r.applyDefault(rewritten)
	if *r.Observed.Effort != stamped || r.Observed.ID != convA || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("re-emitted lines were applied again: %+v %+v", r.Observed, r.Resume)
	}
	if r.Observed.Model.Value != "claude-opus-5-5" || r.Observed.Health != string(observe.HealthGap) {
		t.Fatalf("only the new line applies, and the gap is kept: %+v", r.Observed)
	}
	r.applyDefault(result(3, observe.HealthOK))
	if r.Observed.Health != string(observe.HealthGap) {
		t.Fatal("a later clean read never erases the generation's gap")
	}
}

func TestApplyKeepsSubagentReportsOutOfBoxValues(t *testing.T) {
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "Stop", func(o *observe.Observation) { o.Effort = "medium" }),
		line(3, 20, "SubagentStop", func(o *observe.Observation) { o.Effort, o.SessionID, o.Subagent = "max", convB, true }),
		line(3, 30, "Stop", func(o *observe.Observation) { o.Effort, o.Subagent = "low", true })))
	if r.Observed.Effort.Value != "medium" || r.Observed.ID != "" || r.Resume.Outcome != OutcomeUnverified {
		t.Fatalf("subagent evidence changed main values: %+v %+v", r.Observed, r.Resume)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("an applied record stays valid: %v", err)
	}
}

func TestObservationPreparationStates(t *testing.T) {
	r := resuming()
	if !r.Observable(3) || r.ObservationCursor(3) != (observe.Cursor{Generation: 3}) {
		t.Fatal("a fresh launch is observable from the start")
	}
	r.BeginObservation(3, observe.HealthUnsupported, "no bind")
	if r.Observable(3) || !r.Observable(4) {
		t.Fatal("an unsupported launch has nothing to read")
	}
	r.BeginObservation(3, observe.HealthNoReport, "")
	r.applyDefault(result(3, observe.HealthOK, line(3, 42, "Stop", func(o *observe.Observation) { o.Effort = "low" })))
	if c := r.ObservationCursor(3); c.Offset != 42 || r.ObservationCursor(4) != (observe.Cursor{Generation: 4}) {
		t.Fatalf("the stored cursor serves only its generation: %+v", c)
	}
	bad := *r
	bad.Observed = &Observed{Generation: 3, Health: "fine"}
	if bad.Validate() == nil {
		t.Fatal("an unknown health is invalid")
	}
	bad.Observed = &Observed{Generation: 3, Effort: &ObservedValue{Value: "turbo"}}
	if bad.Validate() == nil {
		t.Fatal("an unknown effort is invalid")
	}
}

func TestStatusLineLabelsObservationOfTheCurrentLaunch(t *testing.T) {
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Model = convA, "resume", "claude-opus-5-5" }),
		line(3, 20, "Stop", func(o *observe.Observation) { o.Effort = "high" })))
	st := &Status{Provider: "claude", Policy: "last (default)", Launch: launchView(r.Launch), Resume: resumeView(r.Resume), Observed: observedView(r.Observed)}
	if got := st.Line(); !strings.Contains(got, "resume resumed · observation ok (agent-reported model claude-opus-5-5, effort high)") {
		t.Fatalf("line = %q", got)
	}
	r.BeginObservation(3, observe.HealthUnsupported, "add yolo.agent.observe")
	st.Observed, st.Note = observedView(r.Observed), "n"
	if got := st.Line(); !strings.Contains(got, "observation unsupported") || !strings.HasSuffix(got, " — n; add yolo.agent.observe") {
		t.Fatalf("unsupported guidance: %q", got)
	}
	st.Observed.Generation = 2
	if strings.Contains(st.Line(), "observation") {
		t.Fatal("another generation's observation is never shown as the current launch's")
	}
}

func TestApplyKeepsTheFirstDivergenceForTheLaunch(t *testing.T) {
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Subagent = convB, "startup", true })))
	if r.Divergence() != nil || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("a subagent report never diverges: %+v", r.Observed)
	}
	// A→B→A: the return to A never clears the recorded divergence, and the
	// launch verdict stays separate from it.
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 30, "SessionStart", start(convB, "clear")),
		line(3, 40, "SessionStart", start(convA, "resume"))))
	d := r.Divergence()
	if d == nil || d.ID != convB || d.Source != "clear" || r.Observed.ID != convA || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("divergence must persist for the generation: %+v %+v", d, r.Resume)
	}
	// An assigned launch diverges the same way, without any resume request.
	a := resuming()
	a.Launch.Kind, a.Resume = KindAssign, nil
	a.applyDefault(result(3, observe.HealthOK, line(3, 10, "SessionStart", start(convA, "startup")), line(3, 20, "SessionStart", start(convB, "clear"))))
	if a.Divergence() == nil || a.Divergence().ID != convB {
		t.Fatalf("an assigned conversation's /clear diverges: %+v", a.Observed)
	}
	// It belongs to its launch only.
	a.Launch = &Launch{Generation: 4, Kind: KindResume, State: StateLaunched, Token: a.Launch.Token, At: a.Launch.At}
	a.LastGeneration = 4
	if a.Divergence() != nil {
		t.Fatal("another launch's divergence is not this launch's")
	}
}

// statusLine is a status line report as the S0 capture of Claude 2.1.311
// shaped it: model, effort, version and context, no identity decision.
func statusLine(model, effort string, ctx *observe.Context) func(*observe.Observation) {
	return func(o *observe.Observation) {
		o.SessionID, o.Model, o.Effort, o.Version, o.Context = convB, model, effort, "2.1.311", ctx
	}
}

func TestApplyTakesValuesFromTheStatusLine(t *testing.T) {
	r := resuming()
	// A resumed start carries no model: hooks alone leave the box empty.
	r.applyDefault(result(3, observe.HealthOK, line(3, 10, "SessionStart", start(convA, "resume"))))
	if r.Observed.Model != nil || r.Observed.Effort != nil || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("resume: %+v %+v", r.Observed, r.Resume)
	}
	ctx := &observe.Context{Size: 200000, Used: 84000, Percent: 42}
	r.applyDefault(result(3, observe.HealthOK, line(3, 20, observe.StatusLine, statusLine("claude-opus-5-5", "high", ctx))))
	o := r.Observed
	if o.Model.Value != "claude-opus-5-5" || o.Model.Source != SourceStatusLine || o.Effort.Value != "high" ||
		o.Effort.Source != SourceStatusLine || o.Version.Value != "2.1.311" || o.Context == nil ||
		o.Context.Size != 200000 || o.Context.Used == nil || *o.Context.Used != 84000 || o.Context.Percent == nil || *o.Context.Percent != 42 ||
		o.Context.Source != SourceStatusLine || !o.Context.At.Equal(time.Unix(2020, 0)) {
		t.Fatalf("status line values: %+v %+v", o, o.Context)
	}
	if o.ID != convA || o.Diverged != nil || r.Resume.Outcome != OutcomeResumed {
		t.Fatalf("a status line never changes identity, divergence or outcome: %+v %+v", o, r.Resume)
	}
	// A later hook Stop updates effort only; a status line without context
	// or version keeps the last ones; unknown used tokens stay unknown.
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 30, "Stop", func(o *observe.Observation) { o.Effort = "max" }),
		line(3, 40, observe.StatusLine, func(o *observe.Observation) { o.Model = "claude-fable-5-1" })))
	if o.Effort.Value != "max" || o.Effort.Source != SourceHook || o.Model.Value != "claude-fable-5-1" ||
		o.Version.Value != "2.1.311" || *o.Context.Percent != 42 {
		t.Fatalf("partial reports: %+v", o)
	}
	r.applyDefault(result(3, observe.HealthOK, line(3, 50, observe.StatusLine, statusLine("", "", &observe.Context{Size: 1000000, Used: -1, Percent: 0}))))
	if o.Context.Used != nil || o.Context.Size != 1000000 || o.Context.Percent == nil || *o.Context.Percent != 0 {
		t.Fatalf("a new context replaces the old one whole: %+v", o.Context)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	st := &Status{Provider: "claude", Policy: "last (default)", Launch: launchView(r.Launch), Resume: resumeView(r.Resume), Observed: observedView(r.Observed)}
	if got := st.Line(); !strings.Contains(got, "(agent-reported model claude-fable-5-1, effort max, context 0%)") {
		t.Fatalf("line = %q", got)
	}
	lines := strings.Join(st.Observed.Lines(), "\n")
	for _, want := range []string{
		"model claude-fable-5-1 (agent-reported by statusline at 1970-01-01T00:34:00Z",
		"effort max (agent-reported by hook at",
		"version 2.1.311 (agent-reported by statusline at",
		"context 0% of 1000000 tokens (agent-reported by statusline at",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("lines lack %q:\n%s", want, lines)
		}
	}
}

func TestValidateBoundsObservedValues(t *testing.T) {
	used, pct1, pct101 := int64(-2), 1, 101
	for name, edit := range map[string]func(*Observed){
		"source":              func(o *Observed) { o.Model = &ObservedValue{Value: "m", Source: "configured"} },
		"version":             func(o *Observed) { o.Version = &ObservedValue{Value: strings.Repeat("9", 33)} },
		"context size":        func(o *Observed) { o.Context = &ObservedContext{Size: 0, Percent: &pct1} },
		"percent":             func(o *Observed) { o.Context = &ObservedContext{Size: 10, Percent: &pct101} },
		"used":                func(o *Observed) { o.Context = &ObservedContext{Size: 10, Percent: &pct1, Used: &used} },
		"used only, none":     func(o *Observed) { o.Context = &ObservedContext{} },
		"used only, negative": func(o *Observed) { o.Context = &ObservedContext{Used: &used} },
		"state kind":          func(o *Observed) { o.State = &ObservedState{Kind: "busy", Source: SourceHook} },
		"state source":        func(o *Observed) { o.State = &ObservedState{Kind: observe.StateIdle} },
		"ctx source":          func(o *Observed) { o.Context = &ObservedContext{Size: 10, Percent: &pct1, Source: "configured"} },
	} {
		r := resuming()
		r.BeginObservation(3, observe.HealthOK, "")
		edit(r.Observed)
		if r.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	r := resuming()
	r.BeginObservation(3, observe.HealthOK, "")
	r.Observed.Model = &ObservedValue{Value: "m"}
	if err := r.Validate(); err != nil {
		t.Fatal("a value stored before sources existed stays valid:", err)
	}
}

// Current is the last observed conversation: every main start but a compact
// sets it, the same ID only refreshes its times and generation, and subagent
// or other-generation lines never touch it.
func TestApplyCurrentFollowsTheLastMainStart(t *testing.T) {
	for _, source := range []string{"startup", "resume", "clear", "fork", ""} {
		r := resuming()
		r.applyDefault(result(3, observe.HealthOK, line(3, 10, "SessionStart", start(convB, source))))
		if c := r.Current; c == nil || c.ID != convB || c.Source != source || c.Generation != 3 || !c.At.Equal(time.Unix(2010, 0)) {
			t.Errorf("%q: %+v", source, r.Current)
		}
	}
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "SessionStart", start(convB, "compact")),
		line(3, 30, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Subagent = convB, "startup", true }),
	))
	if c := r.Current; c == nil || c.ID != convA || c.Source != "resume" || !c.At.Equal(time.Unix(2010, 0)) {
		t.Fatalf("compact and subagent lines keep the current conversation: %+v", r.Current)
	}
	r.applyDefault(result(4, observe.HealthOK, line(4, 40, "SessionStart", start(convB, "clear"))))
	if r.Current.ID != convA {
		t.Fatalf("another generation's report set the current conversation: %+v", r.Current)
	}
	r.applyDefault(result(3, observe.HealthOK, line(3, 50, "SessionStart", start(convA, "clear"))))
	if c := r.Current; c.ID != convA || c.Source != "resume" || !c.At.Equal(time.Unix(2050, 0)) || c.Generation != 3 {
		t.Fatalf("the same conversation only refreshes its times: %+v", c)
	}
	r.applyDefault(result(3, observe.HealthOK, line(3, 60, "SessionStart", start(convB, "clear"))))
	if r.Current.ID != convB || r.Current.Source != "clear" || r.Assigned.ID != convA {
		t.Fatalf("a switch sets current and leaves the assignment: %+v %+v", r.Current, r.Assigned)
	}
}

// A learn launch's first main report names the conversation; without one the
// record stays without an assignment.
func TestApplyLearnsTheFirstReportedConversation(t *testing.T) {
	learning := func() *Record {
		r := NewRecord(scope)
		if err := r.Allocate(KindLearn, t0); err != nil {
			t.Fatal(err)
		}
		r.Launch.State = StateLaunched
		return r
	}
	r := learning()
	r.applyDefault(result(1, observe.HealthNoReport))
	if r.Assigned != nil || r.Current != nil || r.Validate() != nil {
		t.Fatalf("no report learns nothing: %+v %v", r.Assigned, r.Validate())
	}
	r.applyDefault(result(1, observe.HealthOK,
		line(1, 10, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Subagent = convB, "startup", true }),
		line(1, 20, "SessionStart", start(convA, "startup")),
		line(1, 30, "SessionStart", start(convB, "clear")),
	))
	if a := r.Assigned; a == nil || a.ID != convA || a.Source != AssignedLearned || !a.At.Equal(time.Unix(2020, 0)) {
		t.Fatalf("the first main report is learned: %+v", r.Assigned)
	}
	if r.Current.ID != convB || r.Divergence() == nil || r.Divergence().ID != convB || r.Validate() != nil {
		t.Fatalf("a later switch is current and a divergence: %+v %+v", r.Current, r.Divergence())
	}
	if err := r.Allocate(KindLearn, t0); err == nil {
		t.Fatal("a learn launch replaced a learned conversation")
	}
}

// A Codex launch: hooks report identity and model (also on Stop), the
// SubagentStart of a child thread is kept out of box values, a compaction
// keeps the current conversation, and a query line sets model and effort.
func TestApplyFoldsCodexReports(t *testing.T) {
	r := NewRecord(scope)
	r.Provider = "codex"
	if err := r.Allocate(KindLearn, t0); err != nil {
		t.Fatal(err)
	}
	r.Launch.State = StateLaunched
	model := func(m string, edit func(*observe.Observation)) func(*observe.Observation) {
		return func(o *observe.Observation) {
			if edit != nil {
				edit(o)
			}
			o.Model = m
		}
	}
	r.applyDefault(result(1, observe.HealthOK,
		line(1, 10, "SessionStart", model("gpt-5.6-luna", start(convA, "startup"))),
		line(1, 20, observe.Query, func(o *observe.Observation) { o.SessionID, o.Model, o.Effort = convA, "gpt-5.6-luna", "high" }),
		line(1, 30, "SubagentStart", func(o *observe.Observation) { o.SessionID, o.Model, o.Subagent = convB, "gpt-5.6-mini", true }),
		line(1, 40, "SessionStart", start(convA, "compact")),
		line(1, 50, "Stop", model("gpt-5.6-sol", func(o *observe.Observation) { o.SessionID = convA })),
	))
	if a := r.Assigned; a == nil || a.ID != convA || a.Source != AssignedLearned {
		t.Fatalf("the first main start is learned: %+v", r.Assigned)
	}
	if r.Current == nil || r.Current.ID != convA || r.Current.Source != "startup" || r.Divergence() != nil {
		t.Fatalf("a compaction keeps the conversation: %+v %+v", r.Current, r.Divergence())
	}
	o := r.Observed
	if o.Model == nil || o.Model.Value != "gpt-5.6-sol" || o.Model.Source != SourceHook {
		t.Fatalf("the Stop hook's model is the latest: %+v", o.Model)
	}
	if o.Effort == nil || o.Effort.Value != "high" || o.Effort.Source != SourceQuery {
		t.Fatalf("the daemon read's effort: %+v", o.Effort)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	st := &Status{Provider: "codex", Observed: observedView(r.Observed)}
	if lines := strings.Join(st.Observed.Lines(), "\n"); !strings.Contains(lines, "effort high (agent-reported by query at") {
		t.Fatalf("box session names the query source:\n%s", lines)
	}
}

// A query line is the daemon's read of the thread it names: it applies only
// while that thread is the conversation folded so far (current, else
// assigned), and never sets one. archyAstra's repro: a read of A, a switch to
// B, then A's result arrives and is dropped; an unlabelled line from an older
// writer is dropped too.
func TestApplyAppliesAQueryOnlyToItsThread(t *testing.T) {
	query := func(off int64, id, model, state string) observe.Observation {
		return line(3, off, observe.Query, func(o *observe.Observation) { o.SessionID, o.Model, o.Effort, o.State = id, model, "high", state })
	}
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		query(1, convA, "model-a", observe.StateWorking),
		line(3, 2, "SessionStart", func(o *observe.Observation) { o.SessionID, o.Source, o.Model = convB, "resume", "model-b" }),
		query(3, convA, "stale-a", observe.StateIdle),
		query(4, "", "unlabelled", observe.StateIdle)))
	o := r.Observed
	if r.Current == nil || r.Current.ID != convB {
		t.Fatalf("the switch is folded: %+v", r.Current)
	}
	if o.Model == nil || o.Model.Value != "model-b" || o.Effort == nil || o.Effort.Value != "high" {
		t.Fatalf("a stale or unlabelled read overwrote the new thread's values: %+v %+v", o.Model, o.Effort)
	}
	if o.State == nil || o.State.Kind != observe.StateWorking || o.State.Source != SourceQuery {
		t.Fatalf("only the matching read's state stands: %+v", o.State)
	}
	// Before any start report, the assigned conversation is the one read.
	r = resuming()
	r.applyDefault(result(3, observe.HealthOK, query(1, convB, "other", ""), query(2, convA, "assigned", "")))
	if r.Observed.Model == nil || r.Observed.Model.Value != "assigned" || r.Current != nil || r.Observed.ID != "" {
		t.Fatalf("a query applied to the wrong thread or set identity: %+v %+v", r.Observed, r.Current)
	}
}

// A query's version is folded with its model and effort, only for its own
// thread: a read of another conversation sets nothing, and a query without a
// version keeps the one already known.
func TestApplyFoldsAQueryVersionOnlyForItsThread(t *testing.T) {
	query := func(off int64, id, version string) observe.Observation {
		return line(3, off, observe.Query, func(o *observe.Observation) {
			o.SessionID, o.Model, o.Effort, o.Version = id, "gpt-6-astra", "high", version
		})
	}
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK, query(1, convA, "0.166.0"), query(2, convB, "9.9.9"), query(3, convA, "")))
	v := r.Observed.Version
	if v == nil || v.Value != "0.166.0" || v.Source != SourceQuery {
		t.Fatalf("the matching read's version, from the query: %+v", v)
	}
}

// The latest state report wins, in line order; a status line or query line
// without a state resolves a waiting state to working, and nothing else
// changes a state without reporting one. Subagent lines never touch it.
func TestApplyFoldsTheAgentState(t *testing.T) {
	state := func(s string) func(*observe.Observation) { return func(o *observe.Observation) { o.State = s } }
	for _, tc := range []struct {
		name  string
		lines []observe.Observation
		want  string
		src   string
	}{
		{"prompt then status line resolves", []observe.Observation{
			line(3, 1, "Notification", state(observe.StateWaitingPermission)),
			line(3, 2, observe.StatusLine, nil)}, observe.StateWorking, SourceStatusLine},
		{"prompt then Stop", []observe.Observation{
			line(3, 1, "Notification", state(observe.StateWaitingInput)),
			line(3, 2, "Stop", state(observe.StateIdle))}, observe.StateIdle, SourceHook},
		{"failed then prompt", []observe.Observation{
			line(3, 1, "StopFailure", state(observe.StateFailed)),
			line(3, 2, "UserPromptSubmit", state(observe.StateWorking))}, observe.StateWorking, SourceHook},
		{"idle prompt", []observe.Observation{
			line(3, 1, "UserPromptSubmit", state(observe.StateWorking)),
			line(3, 2, "Notification", state(observe.StateIdle))}, observe.StateIdle, SourceHook},
		{"a query without state leaves working", []observe.Observation{
			line(3, 1, "UserPromptSubmit", state(observe.StateWorking)),
			line(3, 2, observe.Query, func(o *observe.Observation) { o.SessionID, o.Model, o.Effort = convA, "m", "high" })}, observe.StateWorking, SourceHook},
		{"a query resolves a wait", []observe.Observation{
			line(3, 1, "PermissionRequest", state(observe.StateWaitingPermission)),
			line(3, 2, observe.Query, func(o *observe.Observation) { o.SessionID, o.Model, o.Effort = convA, "m", "high" })}, observe.StateWorking, SourceQuery},
		{"a query of another thread keeps a wait", []observe.Observation{
			line(3, 1, "PermissionRequest", state(observe.StateWaitingPermission)),
			line(3, 2, observe.Query, func(o *observe.Observation) {
				o.SessionID, o.Model, o.Effort, o.State = convB, "m", "high", observe.StateIdle
			})}, observe.StateWaitingPermission, SourceHook},
		{"a hook without state keeps a wait", []observe.Observation{
			line(3, 1, "Notification", state(observe.StateWaitingDialog)),
			line(3, 2, "Stop", func(o *observe.Observation) { o.Effort = "max" })}, observe.StateWaitingDialog, SourceHook},
		{"subagent lines never touch it", []observe.Observation{
			line(3, 1, "Stop", state(observe.StateIdle)),
			line(3, 2, "SubagentStop", func(o *observe.Observation) { o.Subagent = true; o.State = observe.StateWorking })}, observe.StateIdle, SourceHook},
	} {
		r := resuming()
		r.applyDefault(result(3, observe.HealthOK, tc.lines...))
		s := r.Observed.State
		if s == nil || s.Kind != tc.want || s.Source != tc.src {
			t.Errorf("%s: %+v, want %s from %s", tc.name, s, tc.want, tc.src)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK, line(3, 1, observe.StatusLine, nil)))
	if r.Observed.State != nil {
		t.Fatalf("no state report leaves the state unknown: %+v", r.Observed.State)
	}
	// Used tokens alone are a context of unknown size, from any context line.
	r.applyDefault(result(3, observe.HealthOK, line(3, 2, "PostModelRequest", func(o *observe.Observation) {
		o.Context = &observe.Context{Used: 172000, Percent: -1}
	})))
	c := r.Observed.Context
	if c == nil || c.Size != 0 || c.Percent != nil || c.Used == nil || *c.Used != 172000 || c.Source != SourceHook || r.Validate() != nil {
		t.Fatalf("used-only context: %+v %v", c, r.Validate())
	}
	st := &Status{Provider: "codex", Policy: "last (default)", Launch: launchView(r.Launch), Resume: resumeView(r.Resume), Observed: observedView(r.Observed)}
	if got := st.Line(); !strings.Contains(got, "context 172000 tokens used") {
		t.Fatalf("line = %q", got)
	}
	// Text views name a wait for people, on the line and in the details.
	r.applyDefault(result(3, observe.HealthOK, line(3, 3, "PermissionRequest", func(o *observe.Observation) { o.State = observe.StateWaitingPermission })))
	st.Observed = observedView(r.Observed)
	if got := st.Line(); !strings.Contains(got, "state waiting for permission") {
		t.Fatalf("line = %q", got)
	}
	if got := strings.Join(st.Observed.Lines(), "\n"); !strings.Contains(got, "state waiting for permission (agent-reported by") {
		t.Fatalf("lines:\n%s", got)
	}
}

// A Codex turn through its hooks: a prompt submit is working, a model
// request gives the tokens used only, a permission request waits, and the
// Stop is idle.
func TestApplyFoldsCodexStateAndUsedContext(t *testing.T) {
	r := NewRecord(scope)
	r.Provider = "codex"
	if err := r.Allocate(KindLearn, t0); err != nil {
		t.Fatal(err)
	}
	r.Launch.State = StateLaunched
	state := func(s string) func(*observe.Observation) { return func(o *observe.Observation) { o.State = s } }
	steps := []struct {
		ob   observe.Observation
		want string
	}{
		{line(1, 10, "SessionStart", start(convA, "startup")), ""},
		{line(1, 20, "UserPromptSubmit", state(observe.StateWorking)), observe.StateWorking},
		{line(1, 30, "PostModelRequest", func(o *observe.Observation) { o.Context = &observe.Context{Used: 172000, Percent: -1} }), observe.StateWorking},
		{line(1, 40, "PermissionRequest", state(observe.StateWaitingPermission)), observe.StateWaitingPermission},
		{line(1, 50, "Stop", state(observe.StateIdle)), observe.StateIdle},
	}
	var obs []observe.Observation
	for _, step := range steps {
		obs = append(obs, step.ob)
		r.applyDefault(result(1, observe.HealthOK, obs...))
		got := ""
		if s := r.Observed.State; s != nil {
			got = s.Kind
		}
		if got != step.want {
			t.Fatalf("after %s: state %q, want %q", step.ob.Event, got, step.want)
		}
	}
	c := r.Observed.Context
	if c == nil || c.Used == nil || *c.Used != 172000 || c.Size != 0 || c.Percent != nil {
		t.Fatalf("the tokens used, with no window or percentage: %+v", c)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A completed model request clears only a failed request of the same
// conversation (Codex, their only writer, retries a failed request itself);
// it never turns idle, a wait or an unknown state into working, and no other
// report recovers a failure. Claude's StopFailure keeps its meaning.
func TestApplyClearsACodexFailureOnlyOnACompletedRequest(t *testing.T) {
	codex := func(t *testing.T) *Record {
		t.Helper()
		r := NewRecord(scope)
		r.Provider = "codex"
		if err := r.Allocate(KindLearn, t0); err != nil {
			t.Fatal(err)
		}
		r.Launch.State = StateLaunched
		return r
	}
	request := func(off int64, session, state string) observe.Observation {
		return line(1, off, "PostModelRequest", func(o *observe.Observation) {
			o.SessionID, o.State, o.Context = session, state, &observe.Context{Used: off, Percent: -1}
		})
	}
	kind := func(r *Record) string {
		if s := r.Observed.State; s != nil {
			return s.Kind
		}
		return ""
	}
	started := line(1, 10, "SessionStart", start(convA, "startup"))
	failed := request(20, convA, observe.StateFailed)

	// Recovery, failure again, recovery: in one batch and in separate reads.
	seq := []observe.Observation{started, failed, request(30, convA, observe.StateWorking), request(40, convA, observe.StateFailed), request(50, convA, observe.StateWorking)}
	want := []string{"", observe.StateFailed, observe.StateWorking, observe.StateFailed, observe.StateWorking}
	r := codex(t)
	for i := range seq {
		r.applyDefault(result(1, observe.HealthOK, seq[:i+1]...))
		if got := kind(r); got != want[i] {
			t.Fatalf("after report %d: %q, want %q", i, got, want[i])
		}
	}
	if s := r.Observed.State; s.At != seq[4].AgentTS || s.ReadAt != seq[4].ReadAt || s.Source != SourceHook {
		t.Fatalf("the recovery is stamped by the completed request: %+v", s)
	}
	if r.applyDefault(result(1, observe.HealthOK, seq...)) {
		t.Fatal("the same read applied again changes the record")
	}
	batch := codex(t)
	batch.applyDefault(result(1, observe.HealthOK, seq...))
	if kind(batch) != observe.StateWorking || *batch.Observed.Context.Used != 50 {
		t.Fatalf("one batch: %+v", batch.Observed)
	}

	// Nothing else recovers a failure, and a skipped recovery still takes
	// the tokens used.
	for name, ob := range map[string]observe.Observation{
		"stateless request": request(30, convA, ""),
		"query":             line(1, 30, observe.Query, start(convA, "")),
		"status line":       line(1, 30, observe.StatusLine, nil),
		"other session":     request(30, convB, observe.StateWorking),
		"no session":        request(30, "", observe.StateWorking),
		"subagent":          line(1, 30, "PostModelRequest", func(o *observe.Observation) { o.SessionID, o.State, o.Subagent = convA, observe.StateWorking, true }),
	} {
		r := codex(t)
		r.applyDefault(result(1, observe.HealthOK, started, failed, ob))
		if kind(r) != observe.StateFailed {
			t.Fatalf("%s recovered the failure: %+v", name, r.Observed.State)
		}
		if strings.HasSuffix(name, "session") && (r.Observed.Context == nil || *r.Observed.Context.Used != 30) {
			t.Fatalf("%s: the tokens used are kept: %+v", name, r.Observed.Context)
		}
	}
	old := codex(t)
	old.applyDefault(result(1, observe.HealthOK, started, failed, line(0, 30, "PostModelRequest", func(o *observe.Observation) { o.SessionID, o.State = convA, observe.StateWorking })))
	if kind(old) != observe.StateFailed {
		t.Fatal("a completed request of another generation recovered the failure")
	}

	// A completed request never turns another state into working.
	for name, before := range map[string][]observe.Observation{
		"idle":    {started, line(1, 20, "Stop", func(o *observe.Observation) { o.State = observe.StateIdle })},
		"waiting": {started, line(1, 20, "PermissionRequest", func(o *observe.Observation) { o.State = observe.StateWaitingPermission })},
		"unknown": {started},
	} {
		r := codex(t)
		r.applyDefault(result(1, observe.HealthOK, append(before, request(30, convA, observe.StateWorking))...))
		wantKind := map[string]string{"idle": observe.StateIdle, "waiting": observe.StateWaitingPermission, "unknown": ""}[name]
		if kind(r) != wantKind {
			t.Fatalf("a completed request changed %s into %q", name, kind(r))
		}
	}

	// After a switch to B, a completed request of A cannot recover B.
	switched := codex(t)
	switched.applyDefault(result(1, observe.HealthOK, started, line(1, 15, "SessionStart", start(convB, "startup")), request(20, convB, observe.StateFailed), request(30, convA, observe.StateWorking)))
	if kind(switched) != observe.StateFailed {
		t.Fatal("a completed request of the previous conversation recovered the current one")
	}

	// With no conversation known at all, nothing recovers and nothing is
	// learned; an assigned conversation alone is enough.
	unknown := codex(t)
	unknown.applyDefault(result(1, observe.HealthOK, request(20, "", observe.StateFailed), request(30, convA, observe.StateWorking)))
	if kind(unknown) != observe.StateFailed || unknown.Current != nil || unknown.Assigned != nil {
		t.Fatalf("an unknown conversation recovered or was learned: %+v %+v %+v", unknown.Observed.State, unknown.Current, unknown.Assigned)
	}
	assigned := resuming()
	assigned.Provider = "codex"
	assigned.applyDefault(result(3, observe.HealthOK,
		line(3, 20, "PostModelRequest", func(o *observe.Observation) { o.SessionID, o.State = convA, observe.StateFailed }),
		line(3, 30, "PostModelRequest", func(o *observe.Observation) { o.SessionID, o.State = convA, observe.StateWorking })))
	if kind(assigned) != observe.StateWorking || assigned.Current != nil {
		t.Fatalf("the assigned conversation's completed request: %+v", assigned.Observed.State)
	}

	// B's limit: a Codex Stop still reports idle over an unrecovered failure.
	stopped := codex(t)
	stopped.applyDefault(result(1, observe.HealthOK, started, failed, line(1, 30, "Stop", func(o *observe.Observation) { o.State = observe.StateIdle })))
	if kind(stopped) != observe.StateIdle {
		t.Fatalf("Codex failed then Stop: %q", kind(stopped))
	}

	// Claude's StopFailure ends a turn: stateless and context-only reports
	// keep it. (Its writer never records a model request; see
	// test_agent_observe.py.)
	claude := resuming()
	claude.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "StopFailure", func(o *observe.Observation) { o.State = observe.StateFailed }),
		line(3, 30, observe.StatusLine, func(o *observe.Observation) { o.Context = &observe.Context{Size: 200000, Used: 1, Percent: 1} }),
		line(3, 40, observe.Query, start(convA, ""))))
	if kind(claude) != observe.StateFailed {
		t.Fatalf("Claude StopFailure was recovered: %q", kind(claude))
	}
}
