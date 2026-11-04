package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// observeFixture fakes the container bind, the generation preparation and
// the reader on top of the identity fixture.
type observeFixture struct {
	*identityFixture
	bound    bool
	boundErr error
	prepared []string // "gen/previousGone"
	prepErr  error
	reports  map[uint64][]observe.Observation
}

func newObserveFixture(t *testing.T) *observeFixture {
	f := &observeFixture{identityFixture: newIdentityFixture(t), bound: true, reports: map[uint64][]observe.Observation{}}
	saved := []any{observationBoundFn, prepareObservationFn, readObservationFn, observationOwnerFn}
	t.Cleanup(func() {
		observationBoundFn = saved[0].(func(context.Context, *container.Client, *agentbox.Config) (bool, error))
		prepareObservationFn = saved[1].(func(string, uint64, int, int, bool) (bool, error))
		readObservationFn = saved[2].(func(context.Context, string, uint64, observe.Cursor, int, time.Time) (observe.Result, error))
		observationOwnerFn = saved[3].(func() (int, int, bool))
	})
	observationBoundFn = func(context.Context, *container.Client, *agentbox.Config) (bool, error) { return f.bound, f.boundErr }
	prepareObservationFn = func(_ string, gen uint64, _, _ int, previousGone bool) (bool, error) {
		f.prepared = append(f.prepared, strconv.FormatUint(gen, 10)+"/"+boolText(previousGone))
		return false, f.prepErr
	}
	readObservationFn = func(_ context.Context, _ string, gen uint64, cur observe.Cursor, _ int, readAt time.Time) (observe.Result, error) {
		res := observe.Result{Cursor: cur, Health: observe.HealthNoReport}
		for _, o := range f.reports[gen] {
			if o.Offset > cur.Offset {
				o.Generation, o.ReadAt = gen, readAt
				res.Observations = append(res.Observations, o)
				res.Cursor.Offset, res.Health = o.Offset, observe.HealthOK
			}
		}
		res.Cursor.Generation = gen
		return res, nil
	}
	observationOwnerFn = func() (int, int, bool) { return 1000, 1000, true }
	// Each test is one command: its live-read budget starts fresh.
	viewReads.Lock()
	viewReads.start = time.Time{}
	viewReads.Unlock()
	return f
}

func boolText(b bool) string {
	if b {
		return "gone"
	}
	return "unproven"
}

// planObserved runs ensureAgentSession's tracked tail: fold, plan, launch.
func (f *observeFixture) planObserved(cfg *agentbox.Config, opts sessionStartOptions) (*agentsession.Record, error) {
	f.t.Helper()
	l := f.begin(cfg)
	defer l.Release()
	if err := l.observeCurrentLaunch(f.t.Context(), cfg); err != nil {
		return nil, err
	}
	kind, err := l.plan(opts, &f.notices)
	if err != nil {
		return nil, err
	}
	if err := l.launch(f.t.Context(), container.New("work"), cfg, kind, f.start(cfg.Name)); err != nil {
		return nil, err
	}
	rec, _, err := agentsession.Load("work", cfg.Name)
	return rec, err
}

// learningTracking is cfg's tracking under a claude profile without
// assign_args, so its conversation IDs are learned.
func learningTracking(f *observeFixture, cfg *agentbox.Config) *conversationTracking {
	tr := f.tracking(cfg)
	profile := *tr.profile
	spec := *profile.Session
	spec.AssignArgs = nil
	profile.Session = &spec
	tr.profile = &profile
	return tr
}

func (f *identityFixture) beginWith(tr *conversationTracking, cfg *agentbox.Config) *trackedLaunch {
	f.t.Helper()
	l, err := beginTrackedSession(held(f.t.Context()), tr, "work", cfg.Name)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

// describeLearner is box session's text for cfg under tr's profile.
func describeLearner(t *testing.T, cfg *agentbox.Config, tr *conversationTracking) string {
	t.Helper()
	var out strings.Builder
	writeBoxSession(&out, &boxSessionView{Box: cfg.Name, Space: "work", Conversation: agentsession.Describe(cfg, tr.profile, false, "")})
	return out.String()
}

func observedBox() *agentbox.Config {
	cfg := claudeBox()
	cfg.Features = []string{"claude", "github", "yolo.agent.observe"}
	return cfg
}

func TestLaunchCarriesTheGenerationOnlyWhenObservable(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	argv := f.starts[0]
	if len(argv) < 2 || argv[0] != "env" || argv[1] != "YOLOCTL_AGENT_GEN=1" {
		t.Fatalf("a bound launch carries its generation: %v", argv)
	}
	if rec.Observed == nil || rec.Observed.Generation != 1 || rec.Observed.Health != string(observe.HealthNoReport) {
		t.Fatalf("a new launch starts observing with no report: %+v", rec.Observed)
	}
	if f.recordAtStart.Observed == nil || f.recordAtStart.Launch.State != agentsession.StateDispatching {
		t.Fatal("the generation is saved with the dispatching write, before the holder starts")
	}

	for name, setup := range map[string]func(*observeFixture){
		"old container":  func(f *observeFixture) { f.bound = false },
		"inspect failed": func(f *observeFixture) { f.boundErr = errors.New("vm down") },
		"root missing":   func(f *observeFixture) { f.prepErr = observe.ErrUnavailable },
	} {
		g := newObserveFixture(t)
		setup(g)
		rec, err := g.planObserved(claudeBox(), sessionStartOptions{})
		if err != nil || len(g.starts) != 1 {
			t.Fatalf("%s: observation must never block a launch: %v", name, err)
		}
		if slices.ContainsFunc(g.starts[0], func(a string) bool { return strings.HasPrefix(a, observe.EnvGeneration) }) {
			t.Errorf("%s: an unobservable launch got a generation: %v", name, g.starts[0])
		}
		if rec.Observed == nil || (rec.Observed.Health != string(observe.HealthUnsupported) && rec.Observed.Health != string(observe.HealthUnavailable)) {
			t.Errorf("%s: health not recorded: %+v", name, rec.Observed)
		}
	}
	g := newObserveFixture(t)
	g.bound = false
	rec, _ = g.planObserved(claudeBox(), sessionStartOptions{})
	want := "the container predates agent observation; it starts with the box's next yoloctl box recreate coder"
	if rec.Observed.Health != string(observe.HealthUnsupported) || rec.Observed.Detail != want {
		t.Fatalf("an old container names its recreate: %+v", rec.Observed)
	}
}

func TestPreviousGenerationIsDroppableOnlyAfterAClosedLaunch(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	// Generation 2 ends without any same-token evidence: the box runs with
	// no holder, so the launch becomes uncertain and only --new-session
	// continues, without proof that launch 2 is gone.
	delete(f.marker, "coder")
	f.unit["coder"] = false
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err == nil {
		t.Fatal("an uncertain launch was replaced silently")
	}
	if _, err := f.planObserved(cfg, sessionStartOptions{NewSession: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.prepared, []string{"1/unproven", "2/gone", "3/unproven"}) {
		t.Fatalf("previousGone comes only from a closed launch: %v", f.prepared)
	}
}

// D3 (a): a resume the agent answered with another conversation is not
// refused; the next start resumes the conversation it reported and records
// the requested one as switched.
func TestReportedMismatchResumesTheReportedConversation(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assigned := rec.Assigned.ID
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	// The resumed launch (generation 2) reports another conversation, then
	// exits; a subagent line naming the requested id changes nothing.
	f.reports[2] = []observe.Observation{
		{Offset: 10, Event: "SessionStart", SessionID: assigned, Source: "resume", Subagent: true, AgentTS: time.Unix(1, 0)},
		{Offset: 20, Event: "SessionStart", SessionID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Source: "startup", AgentTS: time.Unix(2, 0)},
		{Offset: 30, Event: "Stop", Effort: "high", AgentTS: time.Unix(3, 0)},
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	const reported = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	rec, err = f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatalf("a folded mismatch must not refuse the next start: %v", err)
	}
	if f.recordAtStart.Assigned.ID != reported || f.recordAtStart.Assigned.Source != agentsession.AssignedSwitched || !slices.Contains(f.starts[2], reported) {
		t.Fatalf("the next start resumes the reported conversation: %+v %v", f.recordAtStart.Assigned, f.starts[2])
	}
	p := rec.Previous[len(rec.Previous)-1]
	if p.ID != assigned || p.Why != agentsession.WhySwitched || p.To != reported || p.Source != "startup" || rec.Resume.Requested != reported {
		t.Fatalf("the requested conversation is kept as switched: %+v %+v", rec.Previous, rec.Resume)
	}
}

// The reporter is required (D1): every Claude box composes it, whatever its
// declared features, so an existing box gets it with its next recreate and no
// create-time default is needed.
func TestEveryClaudeBoxRequiresItsReporter(t *testing.T) {
	profile, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	newCmd := func(args ...string) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().StringSlice("features", nil, "")
		cmd.Flags().StringArray("feature", nil, "")
		cmd.Flags().String("image", "", "")
		if err := cmd.Flags().Parse(args); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	opts := agentboxCreateOptions{}
	applyAgentProfileDefaults(newCmd(), &opts, profile)
	if !slices.Equal(opts.Features, []string{"claude", "github", "yolo.agent.observe"}) {
		t.Fatalf("box create prefills the required features: %v", opts.Features)
	}
	for _, declared := range [][]string{{"claude", "github"}, nil, {"npm"}} {
		cfg := claudeBox()
		cfg.Features = declared
		effective, err := agentbox.EffectiveFeaturesStrict(cfg)
		if err != nil || !slices.Contains(effective, profile.PushFeature()) {
			t.Fatalf("declared %v: effective %v, %v", declared, effective, err)
		}
	}
}

// resumedThenReports runs an assigned launch, an exit and a resume, then
// gives the resumed launch (generation 2) these reports and an exit.
func resumedThenReports(f *observeFixture, cfg *agentbox.Config, reports func(assigned string) []observe.Observation) string {
	f.t.Helper()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	assigned := rec.Assigned.ID
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		f.t.Fatal(err)
	}
	f.reports[2] = reports(assigned)
	f.exit("coder", 0)
	f.noteExit(cfg)
	return assigned
}

// D3 (a): the next start resumes the last observed conversation, whatever
// the agent switched through, and never refuses a divergence.
func TestTheNextStartResumesTheLastObservedConversation(t *testing.T) {
	const other = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	for name, tc := range map[string]struct {
		reports func(string) []observe.Observation
		// switched: the start adopts other; otherwise it keeps the assigned.
		switched bool
	}{
		"resumed then clear": {func(a string) []observe.Observation {
			return []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: a, Source: "resume"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "clear"}}
		}, true},
		"A, B, back to A": {func(a string) []observe.Observation {
			return []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: a, Source: "resume"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "clear"},
				{Offset: 30, Event: "SessionStart", SessionID: a, Source: "resume"}}
		}, false},
		"switch, then compact": {func(a string) []observe.Observation {
			return []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: other, Source: "resume"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "compact"}}
		}, true},
		"subagent names another": {func(a string) []observe.Observation {
			return []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: a, Source: "resume"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "startup", Subagent: true}}
		}, false},
	} {
		f := newObserveFixture(t)
		cfg := observedBox()
		assigned := resumedThenReports(f, cfg, tc.reports)
		rec, err := f.planObserved(cfg, sessionStartOptions{})
		if err != nil {
			t.Errorf("%s: a reported switch must not refuse the next start: %v", name, err)
			continue
		}
		want := assigned
		if tc.switched {
			want = other
		}
		if !slices.Contains(f.starts[2], want) || rec.Assigned.ID != want || rec.Resume.Requested != want || rec.Current.ID != want {
			t.Errorf("%s: the start resumes %s: %v %+v %+v", name, want, f.starts[2], rec.Assigned, rec.Resume)
		}
		if got := len(rec.Previous) > 0 && rec.Previous[0].Why == agentsession.WhySwitched; got != tc.switched {
			t.Errorf("%s: previous %+v", name, rec.Previous)
		}
		if f.notices.Len() != 0 {
			t.Errorf("%s: an ordinary switch prints nothing: %q", name, f.notices.String())
		}
	}
	// An assigned conversation that the agent replaced with /clear, then
	// --new-session: both are retired and a fresh one is chosen.
	f := newObserveFixture(t)
	cfg := observedBox()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := rec.Assigned.ID
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: first, Source: "startup"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "clear"}}
	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err = f.planObserved(cfg, sessionStartOptions{NewSession: true})
	if err != nil || rec.Assigned.ID == first || rec.Assigned.ID == other || rec.Current != nil || slices.Contains(f.starts[1], "--resume") {
		t.Fatalf("--new-session starts a fresh conversation: %v %+v %v", err, rec, f.starts)
	}
	if len(rec.Previous) != 2 || rec.Previous[0].ID != first || rec.Previous[1].ID != other || rec.Previous[1].Why != agentsession.WhyNewSession {
		t.Fatalf("--new-session after a switch lists both: %+v", rec.Previous)
	}
}

// A resume that exits nonzero without any main-session report is failed; the
// next start requests the same conversation again, says so once, and never
// starts a new one. --new-session still does.
func TestAFailedResumeIsRetriedNeverReplaced(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assigned := rec.Assigned.ID
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 1)
	f.noteExit(cfg)
	rec, err = f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.starts[2], "--resume") || !slices.Contains(f.starts[2], assigned) || rec.Assigned.ID != assigned || len(rec.Previous) != 0 {
		t.Fatalf("a failed resume is retried with the same conversation: %v %+v", f.starts[2], rec)
	}
	if f.recordAtStart.Resume.Generation != 3 || rec.Resume.Outcome != agentsession.OutcomeUnverified {
		t.Fatalf("the retry is a new request: %+v", rec.Resume)
	}
	want := "warning: the previous resume of " + assigned + " failed (agent exited with status 1 at 1970-01-01T00:03:20Z); retrying — yoloctl box restart coder --new-session starts a new conversation\n"
	if f.notices.String() != want {
		t.Fatalf("one retry warning:\n%q\nwant\n%q", f.notices.String(), want)
	}
	// --new-session after a failed resume starts a new conversation.
	f.exit("coder", 1)
	f.noteExit(cfg)
	rec, err = f.planObserved(cfg, sessionStartOptions{NewSession: true})
	if err != nil || rec.Assigned.ID == assigned || rec.Previous[0].ID != assigned {
		t.Fatalf("--new-session replaces a failed conversation: %v %+v", err, rec)
	}
	// A nonzero exit after a reported resume is no failure, and a clean
	// exit without a report is none either.
	for name, tc := range map[string]struct {
		code    int
		reports []observe.Observation
	}{
		"reported then crashed": {1, []observe.Observation{{Offset: 10, Event: "SessionStart", Source: "resume"}}},
		"clean exit, no report": {0, nil},
	} {
		g := newObserveFixture(t)
		mine := resumedThenReports(g, cfg, func(string) []observe.Observation { return nil })
		for _, r := range tc.reports {
			r.SessionID = mine
			g.reports[3] = append(g.reports[3], r)
		}
		if _, err := g.planObserved(cfg, sessionStartOptions{}); err != nil {
			t.Fatal(err)
		}
		g.exit("coder", tc.code)
		g.noteExit(cfg)
		g.notices.Reset()
		if _, err := g.planObserved(cfg, sessionStartOptions{}); err != nil || g.notices.Len() != 0 {
			t.Errorf("%s: %v %q", name, err, g.notices.String())
		}
		if g.recordAtStart.Previous != nil {
			t.Errorf("%s: nothing is replaced: %+v", name, g.recordAtStart.Previous)
		}
	}
	// An unreadable generation decides nothing.
	h := newObserveFixture(t)
	resumedThenReports(h, cfg, func(string) []observe.Observation { return nil })
	if _, err := h.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	h.exit("coder", 1)
	h.noteExit(cfg)
	readObservationFn = func(context.Context, string, uint64, observe.Cursor, int, time.Time) (observe.Result, error) {
		return observe.Result{}, observe.ErrUnavailable
	}
	if _, err := h.planObserved(cfg, sessionStartOptions{}); err != nil || h.notices.Len() != 0 {
		t.Fatalf("an unread generation is not failed: %v %q", err, h.notices.String())
	}
}

func TestAnUnverifiableRootFailsClosedBeforeDispatch(t *testing.T) {
	f := newObserveFixture(t)
	f.prepErr = fmt.Errorf("%w: %w: not the capped tmpfs", observe.ErrUnavailable, observe.ErrRootUnverified)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("an unverified root must refuse the launch: %v", err)
	}
	rec, _, _ := agentsession.Load("work", "coder")
	if len(f.starts) != 0 || rec.Launch == nil || rec.Launch.State != agentsession.StateAllocated {
		t.Fatalf("no holder may start and nothing is dispatched: starts=%d %+v", len(f.starts), rec.Launch)
	}
	// Once the root verifies, the same allocated launch is used once; no
	// launch is replayed.
	f.prepErr = nil
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil || len(f.starts) != 1 || rec.Launch.Generation != 1 || rec.LastGeneration != 1 {
		t.Fatalf("the allocated launch proceeds once: %v starts=%d %+v", err, len(f.starts), rec.Launch)
	}
	// A generation-level failure is an optional report: the launch proceeds.
	g := newObserveFixture(t)
	g.prepErr = fmt.Errorf("%w: gen-1 was already used by an earlier launch attempt", observe.ErrUnavailable)
	rec, err = g.planObserved(cfg, sessionStartOptions{})
	if err != nil || len(g.starts) != 1 || rec.Observed.Health != string(observe.HealthUnavailable) {
		t.Fatalf("an unavailable report never blocks the launch: %v %+v", err, rec.Observed)
	}
}

// describe is box show's conversation view of the fixture box.
func describe(t *testing.T, cfg *agentbox.Config, f *observeFixture) *agentsession.Status {
	t.Helper()
	profile, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	m := f.marker[cfg.Name]
	return agentsession.Describe(cfg, profile, f.unit[cfg.Name], m.LaunchToken)
}

func recordBytes(t *testing.T) ([]byte, time.Time) {
	t.Helper()
	path, err := layout.BoxSessionPath("work", "coder")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil {
		t.Fatal(err, statErr)
	}
	return data, info.ModTime()
}

func TestViewsShowTheLiveLaunchsReportsWithoutWriting(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{
		{Offset: 10, Event: "SessionStart", SessionID: rec.Assigned.ID, Source: "startup", Model: "claude-sonnet-5-5", AgentTS: time.Unix(10, 0).UTC()},
		{Offset: 20, Event: "Stop", Effort: "high", AgentTS: time.Unix(20, 0).UTC()},
		{Offset: 30, Event: "Stop", Effort: "max", Subagent: true, AgentTS: time.Unix(30, 0).UTC()},
	}
	before, mtime := recordBytes(t)
	st := describe(t, cfg, f)
	o := st.Observed
	if o == nil || o.Live != "current" || o.Model.Value != "claude-sonnet-5-5" || o.Effort.Value != "high" || !o.Effort.At.Equal(time.Unix(20, 0)) {
		t.Fatalf("the live holder's startup model and Stop effort are shown: %+v", o)
	}
	if !strings.Contains(st.Line(), "agent-reported model claude-sonnet-5-5, effort high") {
		t.Fatalf("line: %q", st.Line())
	}
	again := describe(t, cfg, f)
	if !again.Observed.Effort.At.Equal(o.Effort.At) {
		t.Fatal("a reread is never a new agent event")
	}
	after, mtime2 := recordBytes(t)
	if string(after) != string(before) || !mtime2.Equal(mtime) {
		t.Fatal("a view wrote the record")
	}

	// A divergence or outcome seen only by a view stays pending.
	f.reports[1] = append(f.reports[1], observe.Observation{Offset: 40, Event: "SessionStart", SessionID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Source: "clear", AgentTS: time.Unix(40, 0)})
	st = describe(t, cfg, f)
	if st.Observed.Diverged == nil || !st.Observed.Diverged.Pending || !strings.Contains(st.Line(), "(clear), not yet folded") ||
		!st.Switched() || st.Current.ID != "7c9e6679-7425-40de-944b-e07fc1f90ae7" {
		t.Fatalf("a view-time divergence is agent-reported and pending: %+v %q", st.Observed.Diverged, st.Line())
	}
	if after, _ := recordBytes(t); string(after) != string(before) {
		t.Fatal("a view persisted a divergence")
	}

	// An unavailable live read is explicit, never absent values.
	readObservationFn = func(context.Context, string, uint64, observe.Cursor, int, time.Time) (observe.Result, error) {
		return observe.Result{}, fmt.Errorf("%w: not mounted", observe.ErrUnavailable)
	}
	st = describe(t, cfg, f)
	if !strings.HasPrefix(st.Observed.Live, "unavailable") || st.Observed.Model != nil || !strings.Contains(st.Line(), "live reports unavailable") {
		t.Fatalf("unavailable live values: %+v %q", st.Observed, st.Line())
	}
}

func TestViewsNeverShowAnotherGenerationsReports(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "Stop", Effort: "low", AgentTS: time.Unix(1, 0)}}
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := describe(t, cfg, f); st.Observed == nil || st.Observed.Generation != 2 || st.Observed.Effort != nil {
		t.Fatalf("launch 2 shows none of launch 1's values: %+v", st.Observed)
	}
}

func TestLiveJoinsFoldOnlyUnderTheSpaceOperation(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "Stop", Effort: "medium", AgentTS: time.Unix(1, 0)}}
	before, _ := recordBytes(t)
	foldLiveLaunch(t.Context(), "work", "coder")
	if after, _ := recordBytes(t); string(after) != string(before) {
		t.Fatal("a fast-path attach without the space operation wrote the record")
	}
	foldLiveLaunch(held(t.Context()), "work", "coder")
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Observed.Effort == nil || rec.Observed.Effort.Value != "medium" {
		t.Fatalf("a held live join persists the fold: %+v", rec.Observed)
	}
}

// never retires the finished conversation before the divergence check, so it
// starts fresh after a divergent launch; an uncertain launch still refuses.
func TestNeverStartsFreshAfterADivergentLaunchButRefusesUncertainty(t *testing.T) {
	const other = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	f := newObserveFixture(t)
	cfg := observedBox()
	cfg.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := rec.Assigned.ID
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: first, Source: "startup"}, {Offset: 20, Event: "SessionStart", SessionID: other, Source: "clear"}}
	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err = f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatalf("never must start fresh after a divergent launch: %v", err)
	}
	if rec.Assigned.ID == first || rec.Assigned.ID == other || rec.Previous[0].ID != first || rec.Previous[0].Why != agentsession.WhyNever || rec.Observed.Diverged != nil {
		t.Fatalf("a fresh conversation, the old one retired by never, no divergence carried: %+v", rec)
	}
	// Launch 2 leaves no holder evidence: uncertain stays refused first.
	delete(f.marker, "coder")
	f.unit["coder"] = false
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err == nil {
		t.Fatal("never must not replace an uncertain launch silently")
	}
}

// The real held ensure entry folds a live launch on its earliest return; a
// fast-path entry without the space operation writes nothing.
func TestHeldEnsureEntryFoldsTheLiveLaunch(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "Stop", Effort: "medium", AgentTS: time.Unix(1, 0)}}
	before, _ := recordBytes(t)
	if _, err := ensureAgentSession(t.Context(), container.New("work"), "work", "coder", io.Discard, false, nil, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if after, _ := recordBytes(t); string(after) != string(before) {
		t.Fatal("an entry without the space operation wrote the record")
	}
	if _, err := ensureAgentSession(held(t.Context()), container.New("work"), "work", "coder", io.Discard, false, nil, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Observed.Effort == nil || rec.Observed.Effort.Value != "medium" {
		t.Fatalf("the held live join persists the fold: %+v", rec.Observed)
	}
}

func TestBoxSessionTextShowsTheWholeObservation(t *testing.T) {
	var out strings.Builder
	writeBoxSession(&out, &boxSessionView{Box: "coder", Space: "work", Conversation: statusFixture()})
	text := out.String()
	for _, want := range []string{
		"Observed:     0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d · resume · generation 3 · ok (agent-reported)",
		"model claude-opus-5-5 (agent-reported by statusline at 2026-10-22T09:00:00Z, read 2026-10-22T09:00:00Z)",
		"effort high (agent-reported by hook at",
		"version 2.1.311 (agent-reported by statusline at",
		"context 84000 of 200000 tokens, 42% (agent-reported by statusline at",
		"live reports read by this view",
		"the agent switched from the assigned conversation to 5d1c0000-0000-4000-8000-000000000001 (clear), not yet folded",
		"reported mismatch, not yet folded",
		"Conversation: 0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d  assigned 2026-10-22T09:00:00Z (switched)\n",
		"Current:      5d1c0000-0000-4000-8000-000000000001 · last observed clear at 2026-10-22T09:00:00Z · next start resumes it\n",
		"Selection:    reported by the agent\n",
		"Previous:     5d1c0000-0000-4000-8000-000000000000  2026-10-22T09:00:00Z  new-session\n",
		"              5d1c0000-0000-4000-8000-000000000002  2026-10-22T09:00:00Z  switched to 0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d (resume)\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("box session text lacks %q:\n%s", want, text)
		}
	}
	st := statusFixture()
	st.Observed.Live = "unavailable: agent observation unavailable: context deadline exceeded"
	out.Reset()
	writeBoxSession(&out, &boxSessionView{Box: "coder", Space: "work", Conversation: st})
	if !strings.Contains(out.String(), "live reports unavailable: agent observation unavailable: context deadline exceeded") {
		t.Fatalf("an unavailable live read is explicit:\n%s", out.String())
	}
	// No switch, an agent that does not report its selection, and one that
	// learns its conversation IDs.
	st = statusFixture()
	st.Current.ID, st.Selection = st.ID, agentsession.SelectionUnknown
	out.Reset()
	writeBoxSession(&out, &boxSessionView{Box: "coder", Space: "work", Conversation: st})
	if strings.Contains(out.String(), "Current:") || !strings.Contains(out.String(), "Selection:    not reported by claude: an in-app switch to an already loaded conversation is not visible to yoloctl\n") {
		t.Fatalf("current only when it differs; the selection limit is explicit:\n%s", out.String())
	}
}

func TestBoxSessionTextNamesALearnedConversation(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	learner := learningTracking(f, cfg)
	if st := describeLearner(t, cfg, learner); !strings.Contains(st, "Conversation: none yet (learned from the agent's first report)\n") {
		t.Fatalf("a learning box without a report:\n%s", st)
	}
	l := f.beginWith(learner, cfg)
	kind, err := l.plan(sessionStartOptions{}, &f.notices)
	if err != nil || kind != agentsession.KindLearn {
		t.Fatalf("a profile without assign args learns: %q %v", kind, err)
	}
	if err := l.launch(t.Context(), container.New("work"), cfg, kind, f.start(cfg.Name)); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if !slices.Equal(f.starts[0], []string{"env", "YOLOCTL_AGENT_GEN=1", "env", "CLAUDE_CONFIG_DIR=/home/sandbox/.claude", "DISABLE_AUTOUPDATER=1", "FORCE_AUTOUPDATE_PLUGINS=1", "claude", "--dangerously-skip-permissions"}) {
		t.Fatalf("a learn launch is today's argv with the generation: %v", f.starts[0])
	}
	const learned = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	f.reports[1] = []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: learned, Source: "startup", AgentTS: time.Unix(10, 0).UTC()}}
	if st := describeLearner(t, cfg, learner); !strings.Contains(st, "Conversation: "+learned+"  assigned 1970-01-01T00:00:10Z (learned)\n") {
		t.Fatalf("a view shows the learned conversation:\n%s", st)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	l = f.beginWith(learner, cfg)
	defer l.Release()
	if err := l.observeCurrentLaunch(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if kind, err := l.plan(sessionStartOptions{}, &f.notices); err != nil || kind != agentsession.KindResume || l.conversation() != learned {
		t.Fatalf("the next start resumes the learned conversation: %q %v %q", kind, err, l.conversation())
	}
}

// A reader that honours its context ends at the command's deadline with an
// explicit unavailable value, and no reader outlives the call. This shows the
// cooperative budget, not syscall cancellation or a hard wall-clock bound.
func TestASlowLiveReadEndsAtTheCommandDeadline(t *testing.T) {
	f := newObserveFixture(t)
	cfg := observedBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	saved := observationViewBudget
	t.Cleanup(func() { observationViewBudget = saved })
	observationViewBudget = 100 * time.Millisecond
	readObservationFn = func(ctx context.Context, _ string, _ uint64, _ observe.Cursor, _ int, _ time.Time) (observe.Result, error) {
		select {
		case <-ctx.Done():
			return observe.Result{}, fmt.Errorf("%w: %w", observe.ErrUnavailable, ctx.Err())
		case <-time.After(10 * time.Second):
			return observe.Result{}, errors.New("the read was never bounded")
		}
	}
	started := time.Now()
	st := describe(t, cfg, f)
	if elapsed := time.Since(started); elapsed > 2*time.Second || !strings.Contains(st.Observed.Live, "deadline exceeded") {
		t.Fatalf("live read not bounded: %v %q", elapsed, st.Observed.Live)
	}
	if st = describe(t, cfg, f); !strings.Contains(st.Observed.Live, "budget is spent") {
		t.Fatalf("later reads of the same command are skipped explicitly: %q", st.Observed.Live)
	}
}

func TestAKnownBadBindFailsClosedButInspectFailureIsOptional(t *testing.T) {
	f := newObserveFixture(t)
	f.boundErr = fmt.Errorf("check: %w", agentbox.ErrObservationBind)
	if _, err := f.planObserved(observedBox(), sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "nothing was launched") {
		t.Fatalf("a foreign or read-only bind must refuse: %v", err)
	}
	rec, _, _ := agentsession.Load("work", "coder")
	if len(f.starts) != 0 || rec.Launch.State != agentsession.StateAllocated {
		t.Fatalf("nothing dispatched: starts=%d %+v", len(f.starts), rec.Launch)
	}
	g := newObserveFixture(t)
	g.boundErr = errors.New("nerdctl inspect: connection refused")
	if rec, err := g.planObserved(observedBox(), sessionStartOptions{}); err != nil || len(g.starts) != 1 || rec.Observed.Health != string(observe.HealthUnavailable) {
		t.Fatalf("an inspect that cannot run stays an optional report: %v", err)
	}
}
