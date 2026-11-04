package agentsession

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

var t0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func useConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func lockFor(t *testing.T, box string) *BoxLock {
	t.Helper()
	l, err := LockBox("work", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}

var scope = Scope{Account: "main", Provider: "claude"}

func claude() *agentprofile.Profile {
	return &agentprofile.Profile{Name: "claude", Command: "claude", DefaultArgs: []string{"--dangerously-skip-permissions"}, Session: &agentprofile.SessionSpec{
		IDFormat:     agentprofile.SessionIDFormatUUID,
		AssignArgs:   []string{"--session-id", "{{session_id}}"},
		ResumeArgv:   []string{"{{command}}", "--resume", "{{session_id}}"},
		ReservedArgs: []string{"--session-id", "--resume", "-r", "--continue", "-c", "--fork-session", "--bg"},
	}}
}

// The record is replaced whole, decoded strictly and never read as empty
// when it is damaged; a missing record is absent, not an error.
func TestRecordStorageIsStrict(t *testing.T) {
	useConfig(t)
	if rec, ok, err := Load("work", "coder"); rec != nil || ok || err != nil {
		t.Fatalf("missing record = %v %v %v", rec, ok, err)
	}
	l := lockFor(t, "coder")
	rec := NewRecord(scope)
	if err := rec.Allocate(KindAssign, t0); err != nil {
		t.Fatal(err)
	}
	if err := Save(l, rec); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load("work", "coder")
	if err != nil || !ok || !reflect.DeepEqual(got, rec) {
		t.Fatalf("round trip = %+v %v %v, want %+v", got, ok, err, rec)
	}
	path, _ := layout.BoxSessionPath("work", "coder")
	if !strings.Contains(path, filepath.Join("state", "boxes", "coder.session.yaml")) {
		t.Fatalf("record path %s is not outside the declarations", path)
	}
	raw, _ := os.ReadFile(path)
	for name, damaged := range map[string]string{
		"unknown field": string(raw) + "extra: 1\n",
		"bad state":     strings.Replace(string(raw), "state: allocated", "state: running", 1),
		"bad id":        strings.Replace(string(raw), rec.Assigned.ID, "not-a-uuid", 1),
		"bad schema":    strings.Replace(string(raw), "schema_version: 1", "schema_version: 2", 1),
		"bad token":     strings.Replace(string(raw), rec.Launch.Token, "short", 1),
	} {
		if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _, err := Load("work", "coder"); err == nil || got != nil {
			t.Fatalf("%s: damaged record loaded as %+v", name, got)
		}
	}
	if err := Remove("work", "coder"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("work", "coder"); err != nil {
		t.Fatal("a second removal must be harmless:", err)
	}
}

// Record writes need the box's own live lock.
func TestSaveRequiresTheBoxLock(t *testing.T) {
	useConfig(t)
	rec := NewRecord(scope)
	if err := Save(nil, rec); err == nil {
		t.Fatal("saved without a lock")
	}
	l := lockFor(t, "coder")
	l.Release()
	if err := Save(l, rec); err == nil {
		t.Fatal("saved under a released lock")
	}
}

// An allocated launch was never dispatched: it is reused once, with the same
// generation, token and conversation. Every other launch mints a new
// generation and token; a resume requests the current conversation.
func TestAllocateReusesOnlyAnUndispatchedLaunch(t *testing.T) {
	rec := NewRecord(scope)
	if err := rec.Allocate(KindAssign, t0); err != nil {
		t.Fatal(err)
	}
	first := *rec.Launch
	id := rec.Assigned.ID
	if first.Generation != 1 || first.State != StateAllocated || !ValidID(id) {
		t.Fatalf("first allocation = %+v %s", first, id)
	}
	if err := rec.Allocate(KindAssign, t0.Add(time.Minute)); err != nil || *rec.Launch != first || rec.Assigned.ID != id {
		t.Fatalf("an allocated launch was not reused: %+v (%v)", rec.Launch, err)
	}
	if err := rec.Allocate(KindResume, t0); err == nil {
		t.Fatal("an allocated assign was reused as a resume")
	}
	if err := rec.Dispatch(first.Generation, first.Token, "req-1", "inc-1", t0); err != nil {
		t.Fatal(err)
	}
	if err := rec.Allocate(KindResume, t0); err == nil {
		t.Fatal("a dispatched launch was replaced before it settled")
	}
	if err := rec.Close(first.Token, "agent exited", t0); err != nil {
		t.Fatal(err)
	}
	if err := rec.Allocate(KindResume, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rec.Launch.Generation != 2 || rec.Launch.Token == first.Token || rec.Assigned.ID != id || rec.Resume == nil ||
		rec.Resume.Requested != id || rec.Resume.Generation != 2 || rec.Resume.Outcome != OutcomeUnverified {
		t.Fatalf("resume allocation = %+v %+v %+v", rec.Launch, rec.Assigned, rec.Resume)
	}
}

// Stale callbacks and unrelated holder evidence never move a launch:
// dispatch needs the current generation and token, binding and closing need
// the launch's own token.
func TestOnlyCurrentLaunchEvidenceMovesTheRecord(t *testing.T) {
	rec := NewRecord(scope)
	_ = rec.Allocate(KindAssign, t0)
	gen, token := rec.Launch.Generation, rec.Launch.Token
	if err := rec.Dispatch(gen+1, token, "", "", t0); err == nil {
		t.Fatal("a newer generation dispatched the current launch")
	}
	if err := rec.Dispatch(gen, strings.Repeat("0", 32), "", "", t0); err == nil {
		t.Fatal("another token dispatched the current launch")
	}
	if err := rec.Dispatch(gen, token, "req", "inc", t0); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"", strings.Repeat("a", 32)} {
		if err := rec.Bind(other, "7@x", t0); err == nil {
			t.Fatalf("token %q bound the launch", other)
		}
		if err := rec.Close(other, "x", t0); err == nil {
			t.Fatalf("token %q closed the launch", other)
		}
	}
	if rec.Launch.State != StateDispatching {
		t.Fatalf("unrelated evidence moved the launch to %s", rec.Launch.State)
	}
	if err := rec.Uncertain(gen-1, token, "x", t0); err == nil {
		t.Fatal("an older generation marked the launch uncertain")
	}
	if err := rec.Bind(token, "7@x", t0); err != nil || rec.Launch.State != StateLaunched || rec.Launch.Holder != "7@x" {
		t.Fatalf("own marker did not bind: %+v %v", rec.Launch, err)
	}
	id := rec.Assigned.ID
	if err := rec.Close(token, "agent exited with status 0", t0); err != nil || rec.Launch.State != StateClosed {
		t.Fatalf("own tombstone did not close: %+v %v", rec.Launch, err)
	}
	if rec.Assigned == nil || rec.Assigned.ID != id || len(rec.Previous) != 0 {
		t.Fatal("closing a launch ended its conversation")
	}
}

// Fresh-sandbox proof (F10): launched launches and interrupted resumes close
// and stay resumable; an interrupted first assignment stays uncertain; an
// allocated reservation and a closed launch are untouched.
func TestSandboxReplacedResumesOnlyKnownConversations(t *testing.T) {
	for _, tc := range []struct {
		kind, state, want string
		changed           bool
	}{
		{KindAssign, StateLaunched, StateClosed, true},
		{KindResume, StateLaunched, StateClosed, true},
		{KindResume, StateDispatching, StateClosed, true},
		{KindResume, StateUncertain, StateClosed, true},
		{KindAssign, StateDispatching, StateUncertain, true},
		{KindAssign, StateUncertain, StateUncertain, false},
		{KindAssign, StateAllocated, StateAllocated, false},
		{KindResume, StateClosed, StateClosed, false},
	} {
		rec := NewRecord(scope)
		_ = rec.Allocate(KindAssign, t0)
		rec.Launch.Kind, rec.Launch.State = tc.kind, tc.state
		id := rec.Assigned.ID
		if changed := rec.SandboxReplaced("restarted", t0); changed != tc.changed || rec.Launch.State != tc.want {
			t.Errorf("%s %s: changed=%v state=%s, want %v %s", tc.kind, tc.state, changed, rec.Launch.State, tc.changed, tc.want)
		}
		if rec.Assigned.ID != id || len(rec.Previous) != 0 {
			t.Errorf("%s %s: the sandbox proof replaced the conversation", tc.kind, tc.state)
		}
	}
}

func TestRetireKeepsTheNewestPreviousConversations(t *testing.T) {
	rec := NewRecord(scope)
	var ids []string
	for i := 0; i < MaxPrevious+3; i++ {
		_ = rec.Allocate(KindAssign, t0)
		ids = append(ids, rec.Assigned.ID)
		rec.Retire(WhyNewSession, t0)
	}
	if len(rec.Previous) != MaxPrevious || rec.Previous[0].ID != ids[3] || rec.Previous[MaxPrevious-1].ID != ids[len(ids)-1] {
		t.Fatalf("previous = %+v", rec.Previous)
	}
	if rec.Assigned != nil || rec.Launch != nil || rec.Resume != nil {
		t.Fatal("retire left the replaced conversation current")
	}
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
	if rec.LastGeneration != MaxPrevious+3 {
		t.Fatalf("retirement reset the generation counter to %d", rec.LastGeneration)
	}
	_ = rec.Allocate(KindAssign, t0)
	if rec.Launch.Generation != MaxPrevious+4 {
		t.Fatalf("a launch after retirement reused generation %d", rec.Launch.Generation)
	}
	rec.LastGeneration = rec.Launch.Generation - 1
	if err := rec.Validate(); err == nil {
		t.Fatal("a launch newer than the generation counter validated")
	}
}

// A record of another account or agent never selects argv: loading it under
// the lock retires its conversation for the box's current scope.
func TestLoadLockedRetiresAStaleScope(t *testing.T) {
	useConfig(t)
	l := lockFor(t, "coder")
	rec := NewRecord(Scope{Account: "old", Provider: "claude"})
	_ = rec.Allocate(KindAssign, t0)
	old := rec.Assigned.ID
	if err := Save(l, rec); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLocked(l, scope, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account != "main" || got.Assigned != nil || len(got.Previous) != 1 || got.Previous[0].ID != old || got.Previous[0].Why != WhyAccountChanged || got.LastGeneration != 1 {
		t.Fatalf("stale scope = %+v", got)
	}
}

func TestEffectivePolicyKeepsExistingLaunches(t *testing.T) {
	spec := claude().Session
	// An observe-only profile tracks every launch as a new conversation.
	observeOnly := &agentprofile.SessionSpec{IDFormat: "uuid", Observe: &agentprofile.ObserveSpec{Push: "yolo.agent.observe-agy"}}
	box := func(policy string, args ...string) *agentbox.Config {
		c := &agentbox.Config{Name: "coder", Agent: "claude", Account: "main", AgentExtraArgs: args}
		if policy != "" {
			c.AgentSession = &agentbox.AgentSession{Resume: policy}
		}
		return c
	}
	for _, tc := range []struct {
		name string
		cfg  *agentbox.Config
		spec *agentprofile.SessionSpec
		want string
		err  string
	}{
		{"unset", box(""), spec, "last (default)", ""},
		{"unset legacy resume", box("", "--resume", "x"), spec, "manual (legacy: agent_extra_args carries --resume)", ""},
		{"unset legacy assignment form", box("", "--session-id=x"), spec, "manual (legacy: agent_extra_args carries --session-id)", ""},
		{"unset unrelated flag", box("", "--resumes"), spec, "last (default)", ""},
		{"explicit manual", box("manual", "-c"), spec, "manual", ""},
		{"explicit never", box("never"), spec, "never", ""},
		{"explicit last with resume", box("last", "-r"), spec, "", "agent_extra_args uses -r"},
		{"explicit never with continue", box("never", "--continue"), spec, "", "set --agent-session-resume manual or drop --continue"},
		{"unsupported profile", box("last"), nil, "unsupported", ""},
		{"observe-only unset", box(""), observeOnly, "never (default)", ""},
		{"observe-only never", box("never"), observeOnly, "never", ""},
		{"observe-only manual", box("manual"), observeOnly, "manual", ""},
		{"observe-only last", box("last"), observeOnly, "", "cannot resume a conversation"},
	} {
		got, err := EffectivePolicy(tc.spec, tc.cfg)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || got.Describe() != tc.want {
			t.Errorf("%s: %q (%v), want %q", tc.name, got.Describe(), err, tc.want)
		}
	}
}

func TestArgvAssignsOrResumesByID(t *testing.T) {
	p := claude()
	id := "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d"
	assign, err := Argv(p, KindAssign, id, []string{"--model", "opus"})
	if err != nil || !reflect.DeepEqual(assign, []string{"claude", "--dangerously-skip-permissions", "--session-id", id, "--model", "opus"}) {
		t.Fatalf("assign argv = %v (%v)", assign, err)
	}
	resume, err := Argv(p, KindResume, id, []string{"--model", "opus"})
	if err != nil || !reflect.DeepEqual(resume, []string{"claude", "--resume", id, "--dangerously-skip-permissions", "--model", "opus"}) {
		t.Fatalf("resume argv = %v (%v)", resume, err)
	}
	if _, err := Argv(p, KindResume, "not-an-id", nil); err == nil {
		t.Fatal("an invalid conversation id reached argv")
	}
	p.Session.AssignArgs = nil
	if _, err := Argv(p, KindAssign, id, nil); err == nil {
		t.Fatal("a profile without assign args assigned an id")
	}
}

// Views name every state without a usable record instead of showing nothing.
func TestDescribeNamesUntrackedAndUnreadableState(t *testing.T) {
	useConfig(t)
	cfg := &agentbox.Config{Name: "coder", Space: "work", Agent: "claude", Account: "main"}
	if st := Describe(cfg, claude(), true, ""); st.Note != Untracked || st.ID != "" || !st.Tracked {
		t.Fatalf("legacy live session = %+v", st)
	}
	if st := Describe(cfg, claude(), false, ""); st.Note != "" || st.Policy != "last (default)" {
		t.Fatalf("untouched box = %+v", st)
	}
	l := lockFor(t, "coder")
	rec := NewRecord(scope)
	_ = rec.Allocate(KindAssign, t0)
	_ = rec.Dispatch(1, rec.Launch.Token, "req", "inc", t0)
	_ = rec.Bind(rec.Launch.Token, "7@x", t0)
	if err := Save(l, rec); err != nil {
		t.Fatal(err)
	}
	st := Describe(cfg, claude(), true, rec.Launch.Token)
	if st.ID != rec.Assigned.ID || st.Note != "" || !strings.Contains(st.Line(), "launch 1 assign launched") {
		t.Fatalf("tracked session = %+v %q", st, st.Line())
	}
	if st := Describe(cfg, claude(), true, strings.Repeat("b", 32)); st.Note != Untracked {
		t.Fatalf("a holder of another launch looked tracked: %+v", st)
	}
	manual := *cfg
	manual.AgentExtraArgs = []string{"--continue"}
	if st := Describe(&manual, claude(), true, ""); st.Tracked || !strings.Contains(st.Note, "not tracked") {
		t.Fatalf("manual box = %+v", st)
	}
	codex := &agentprofile.Profile{Name: "codex", Command: "codex"}
	if st := Describe(&agentbox.Config{Name: "cx", Space: "work", Agent: "codex", Account: "main"}, codex, true, ""); st.Policy != "unsupported" || st.Tracked {
		t.Fatalf("unsupported agent = %+v", st)
	}
	path, _ := layout.BoxSessionPath("work", "coder")
	if err := os.WriteFile(path, []byte("schema_version: 1\naccount: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := Describe(cfg, claude(), false, ""); !strings.HasPrefix(st.Note, "record unavailable:") {
		t.Fatalf("unreadable record = %+v", st)
	}
}

// A resume requests the last observed conversation and adopts it first, so
// the request and the assignment agree and the replaced one is kept as
// switched; retiring keeps both IDs.
func TestAllocateAdoptsTheLastObservedConversation(t *testing.T) {
	const other = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	rec := NewRecord(scope)
	if err := rec.Allocate(KindAssign, t0); err != nil {
		t.Fatal(err)
	}
	first := rec.Assigned.ID
	if rec.Assigned.Source != AssignedChosen || rec.Target() != first {
		t.Fatalf("an assigned conversation is chosen: %+v", rec.Assigned)
	}
	rec.Launch.State = StateClosed
	rec.Current = &Current{ID: other, Source: "clear", At: t0, ReadAt: t0, Generation: 1}
	if rec.Target() != other || rec.Validate() != nil {
		t.Fatalf("target = %s (%v)", rec.Target(), rec.Validate())
	}
	if err := rec.Allocate(KindResume, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	want := Previous{ID: first, EndedAt: t0.Add(time.Minute), Why: WhySwitched, To: other, Source: "clear"}
	if rec.Assigned.ID != other || rec.Assigned.Source != AssignedSwitched || rec.Resume.Requested != other || !reflect.DeepEqual(rec.Previous, []Previous{want}) || rec.Validate() != nil {
		t.Fatalf("adopted: %+v %+v %+v (%v)", rec.Assigned, rec.Resume, rec.Previous, rec.Validate())
	}
	if (PreviousView(want)).Text() != "switched to "+other+" (clear)" {
		t.Fatalf("text = %q", PreviousView(want).Text())
	}
	// A later switch back is retired with the assignment.
	rec.Launch.State = StateClosed
	rec.Current = &Current{ID: first, Source: "resume", At: t0, ReadAt: t0, Generation: 2}
	rec.Retire(WhyNewSession, t0)
	if rec.Assigned != nil || rec.Current != nil || len(rec.Previous) != 3 || rec.Previous[1].ID != other || rec.Previous[2].ID != first || rec.Previous[2].Why != WhyNewSession {
		t.Fatalf("retire keeps both: %+v", rec.Previous)
	}
	for name, bad := range map[string]func(*Record){
		"current id":         func(r *Record) { r.Current = &Current{ID: "x", Generation: 1} },
		"current generation": func(r *Record) { r.Current = &Current{ID: other, Generation: 9} },
		"current source":     func(r *Record) { r.Current = &Current{ID: other, Source: "compact", Generation: 1} },
		"assigned source":    func(r *Record) { r.Assigned = &Assigned{ID: other, Source: "guessed"} },
		"switched to":        func(r *Record) { r.Previous = []Previous{{ID: other, Why: WhySwitched, To: "x"}} },
		"to without switch":  func(r *Record) { r.Previous = []Previous{{ID: other, Why: WhyNever, To: other}} },
		"resume outcome":     func(r *Record) { r.Resume = &Resume{Requested: other, Outcome: "lost"} },
	} {
		r := NewRecord(scope)
		r.LastGeneration = 2
		bad(r)
		if r.Validate() == nil {
			t.Errorf("%s: an invalid record validated", name)
		}
	}
}

// A closed resume launch that exited nonzero with no main-session report is
// failed; a reported start, a clean exit, an unread or damaged generation, or
// another kind is not.
func TestSettleResumeDecidesOnlyAReadSilentFailure(t *testing.T) {
	failing := func() *Record {
		r := NewRecord(scope)
		_ = r.Allocate(KindAssign, t0)
		r.Launch.State = StateClosed
		_ = r.Allocate(KindResume, t0)
		r.Launch.State = StateLaunched
		r.BeginObservation(2, "no-report", "")
		if err := r.CloseExit(r.Launch.Token, 1, t0, t0); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := failing()
	if !r.SettleResume() || r.Resume.Outcome != OutcomeFailed || !strings.Contains(r.Resume.Detail, "status 1") || r.SettleResume() || r.Validate() != nil {
		t.Fatalf("failed once: %+v (%v)", r.Resume, r.Validate())
	}
	for name, edit := range map[string]func(*Record){
		"clean exit":  func(r *Record) { *r.Launch.ExitCode = 0 },
		"no status":   func(r *Record) { r.Launch.ExitCode = nil },
		"reported":    func(r *Record) { r.Resume.Outcome = OutcomeResumed },
		"gap":         func(r *Record) { r.Observed.Health = "gap" },
		"unsupported": func(r *Record) { r.Observed.Health = "unsupported" },
		"other gen":   func(r *Record) { r.Observed.Generation = 1 },
		"assign":      func(r *Record) { r.Launch.Kind = KindAssign },
	} {
		r := failing()
		edit(r)
		if r.SettleResume() {
			t.Errorf("%s: settled as failed", name)
		}
	}
}

// Codex learns its thread (no assign arguments) and resumes it with
// `codex resume <id>` ahead of its ordinary options; a box already pinning
// `resume` in its extra arguments stays manual (archyAstra today).
func TestCodexLearnsAndResumesByThread(t *testing.T) {
	p, err := agentprofile.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	id := "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"
	resume, err := Argv(p, KindResume, id, []string{"-c", "model_reasoning_effort=high"})
	want := []string{"codex", "resume", id, "--ask-for-approval", "never", "--sandbox", "danger-full-access", "-c", "model_reasoning_effort=high"}
	if err != nil || !reflect.DeepEqual(resume, want) {
		t.Fatalf("resume argv = %v (%v), want %v", resume, err, want)
	}
	learn, err := Argv(p, KindLearn, "", nil)
	if err != nil || !reflect.DeepEqual(learn, []string{"codex", "--ask-for-approval", "never", "--sandbox", "danger-full-access"}) {
		t.Fatalf("learn argv = %v (%v)", learn, err)
	}
	if _, err := Argv(p, KindAssign, id, nil); err == nil {
		t.Fatal("Codex cannot be assigned an id")
	}
	box := func(policy string, args ...string) *agentbox.Config {
		c := &agentbox.Config{Name: "astra", Agent: "codex", Account: "main", AgentExtraArgs: args}
		if policy != "" {
			c.AgentSession = &agentbox.AgentSession{Resume: policy}
		}
		return c
	}
	if got, err := EffectivePolicy(p.Session, box("", "resume", id)); err != nil || got.Describe() != "manual (legacy: agent_extra_args carries resume)" {
		t.Fatalf("a pinned resume = %q (%v), want manual", got.Describe(), err)
	}
	if got, err := EffectivePolicy(p.Session, box("")); err != nil || got.Describe() != "last (default)" {
		t.Fatalf("unset = %q (%v)", got.Describe(), err)
	}
	if _, err := EffectivePolicy(p.Session, box("last", "resume", id)); err == nil || !strings.Contains(err.Error(), "agent_extra_args uses resume") {
		t.Fatalf("declared last with a pinned resume: %v", err)
	}
}

// A learn launch runs today's argv and takes no ID.
func TestArgvLearnsWithoutAnID(t *testing.T) {
	p := claude()
	p.Session.AssignArgs = nil
	learn, err := Argv(p, KindLearn, "", []string{"--model", "opus"})
	if err != nil || !reflect.DeepEqual(learn, []string{"claude", "--dangerously-skip-permissions", "--model", "opus"}) {
		t.Fatalf("learn argv = %v (%v)", learn, err)
	}
	if _, err := Argv(p, KindLearn, "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", nil); err == nil {
		t.Fatal("a learn launch passed an id")
	}
}
