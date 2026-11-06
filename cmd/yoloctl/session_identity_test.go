package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// identityFixture fakes the VM evidence a tracked start reads: the holder
// unit, its marker and its tombstone. Records live under a temporary config.
type identityFixture struct {
	t             *testing.T
	unit          map[string]bool
	marker        map[string]session.Marker
	tombstone     map[string]session.Tombstone
	names         []string
	unitErr       map[string]error
	markerErr     map[string]error
	starts        [][]string
	recordAtStart *agentsession.Record
	startErr      error
	// notices collects what plan prints on the start's stderr.
	notices strings.Builder
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &identityFixture{t: t, unit: map[string]bool{}, marker: map[string]session.Marker{}, tombstone: map[string]session.Tombstone{}, names: []string{"coder"}, unitErr: map[string]error{}, markerErr: map[string]error{}}
	oldActive, oldMarker, oldTomb, oldNames, oldNow := holderUnitActiveFn, readVMMarkerFn, readVMTombstoneFn, listBoxNamesFn, sessionNow
	oldState, oldProbe := holderUnitStateFn, probeVMMarkerFn
	holderUnitStateFn = func(_ context.Context, _ *container.Client, name string) (bool, error) {
		return f.unit[name], f.unitErr[name]
	}
	probeVMMarkerFn = func(_ context.Context, _ *container.Client, name string) (session.Marker, bool, error) {
		if err := f.markerErr[name]; err != nil {
			return session.Marker{}, false, err
		}
		m, ok := f.marker[name]
		return m, ok, nil
	}
	holderUnitActiveFn = func(_ context.Context, _ *container.Client, name string) bool { return f.unit[name] }
	readVMMarkerFn = func(_ context.Context, _ *container.Client, name string) (session.Marker, bool) {
		m, ok := f.marker[name]
		return m, ok
	}
	readVMTombstoneFn = func(_ context.Context, _ *container.Client, name string) (session.Tombstone, bool) {
		ts, ok := f.tombstone[name]
		return ts, ok
	}
	listBoxNamesFn = func(string) ([]string, error) { return f.names, nil }
	clock := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	sessionNow = func() time.Time { clock = clock.Add(time.Second); return clock }
	t.Cleanup(func() {
		holderUnitActiveFn, readVMMarkerFn, readVMTombstoneFn, listBoxNamesFn, sessionNow = oldActive, oldMarker, oldTomb, oldNames, oldNow
		holderUnitStateFn, probeVMMarkerFn = oldState, oldProbe
	})
	return f
}

// held is a context carrying the command wrapper's live space-lock witness.
func held(ctx context.Context) context.Context {
	return context.WithValue(ctx, spaceHeldKey{}, func() bool { return true })
}

func claudeBox(args ...string) *agentbox.Config {
	return &agentbox.Config{Name: "coder", Space: "work", Agent: "claude", Account: "main", AgentExtraArgs: args}
}

func (f *identityFixture) tracking(cfg *agentbox.Config) *conversationTracking {
	f.t.Helper()
	tr, _, err := sessionTrackingFor(cfg)
	if err != nil || tr == nil {
		f.t.Fatalf("box is not tracked: %v", err)
	}
	return tr
}

func (f *identityFixture) begin(cfg *agentbox.Config) *trackedLaunch {
	f.t.Helper()
	l, err := beginTrackedSession(held(f.t.Context()), f.tracking(cfg), "work", cfg.Name)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

// start is the fake holder launch: it records argv, captures the record as
// StartHolderUnit would find it, and writes this launch's marker.
func (f *identityFixture) start(name string) func([]string, string) error {
	return func(argv []string, token string) error {
		f.starts = append(f.starts, argv)
		rec, _, err := agentsession.Load("work", name)
		if err != nil {
			f.t.Fatal(err)
		}
		f.recordAtStart = rec
		if f.startErr != nil {
			return f.startErr
		}
		f.unit[name] = true
		f.marker[name] = session.Marker{StartedAt: time.Unix(100, 0), HolderPID: 7, LaunchToken: token}
		return nil
	}
}

// launchOnce plans and runs one tracked launch the way ensureAgentSession
// does with the box running and no holder.
func (f *identityFixture) launchOnce(cfg *agentbox.Config, opts sessionStartOptions) (*agentsession.Record, error) {
	f.t.Helper()
	l := f.begin(cfg)
	defer l.Release()
	kind, err := l.plan(opts, &f.notices)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		f.t.Fatal("a tracked claude box planned an untracked launch")
	}
	if err := l.launch(f.t.Context(), container.New("work"), cfg, kind, f.start(cfg.Name)); err != nil {
		return nil, err
	}
	rec, _, err := agentsession.Load("work", cfg.Name)
	return rec, err
}

// exit models the holder exit with its tombstone, as the holder writes it.
func (f *identityFixture) exit(name string, code int) {
	m := f.marker[name]
	delete(f.marker, name)
	f.unit[name] = false
	f.tombstone[name] = session.Tombstone{ExitCode: code, ExitedAt: time.Unix(200, 0), LaunchToken: m.LaunchToken}
}

func (f *identityFixture) noteExit(cfg *agentbox.Config) {
	f.t.Helper()
	l := f.begin(cfg)
	defer l.Release()
	if ts, ok := f.tombstone[cfg.Name]; ok {
		if err := l.noteExit(ts); err != nil {
			f.t.Fatal(err)
		}
		delete(f.tombstone, cfg.Name)
	}
}

// F1, then an exit and a resume: a new conversation is assigned by ID, the
// dispatching write precedes the holder launch, the holder binds by token,
// and the next start resumes that same conversation.
func TestTrackedLaunchAssignsThenResumes(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox("--model", "opus")
	rec, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := rec.Assigned.ID
	if f.recordAtStart == nil || f.recordAtStart.Launch.State != agentsession.StateDispatching || f.recordAtStart.Assigned.ID != id {
		t.Fatalf("holder launched before dispatching was saved: %+v", f.recordAtStart)
	}
	argv := f.starts[0]
	if !slices.Contains(argv, "--session-id") || !slices.Contains(argv, id) || slices.Contains(argv, "--resume") || argv[len(argv)-1] != "opus" {
		t.Fatalf("assign argv = %v", argv)
	}
	if !strings.Contains(strings.Join(argv, " "), "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("launch env prefix lost: %v", argv)
	}
	if !slices.Contains(argv, "ANTHROPIC_CONFIG_DIR=/home/sandbox/.claude/anthropic") {
		t.Fatal("initial launch lost native profile relocation", argv)
	}
	if rec.Launch.State != agentsession.StateLaunched || rec.Launch.Holder != "7@1970-01-01T00:01:40Z" {
		t.Fatalf("launch not bound to its holder: %+v", rec.Launch)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err = f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	argv = f.starts[1]
	if !slices.Contains(argv, "ANTHROPIC_CONFIG_DIR=/home/sandbox/.claude/anthropic") {
		t.Fatal("resumed launch lost native profile relocation", argv)
	}
	if i := slices.Index(argv, "--resume"); i < 0 || argv[i+1] != id || slices.Contains(argv, "--session-id") {
		t.Fatalf("resume argv = %v", argv)
	}
	if rec.Assigned.ID != id || rec.Launch.Generation != 2 || rec.Resume == nil || rec.Resume.Outcome != agentsession.OutcomeUnverified {
		t.Fatalf("resume record = %+v %+v", rec.Launch, rec.Resume)
	}
}

// F2: a launch allocated but never dispatched (crash before the dispatching
// write) launches once with the same reservation.
func TestAllocatedLaunchIsReusedOnce(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	l := f.begin(cfg)
	if _, err := l.plan(sessionStartOptions{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := l.rec.Allocate(agentsession.KindAssign, sessionNow()); err != nil {
		t.Fatal(err)
	}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	reserved := *l.rec.Launch
	id := l.rec.Assigned.ID
	l.Release()
	rec, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Launch.Generation != reserved.Generation || rec.Launch.Token != reserved.Token || rec.Assigned.ID != id || len(f.starts) != 1 {
		t.Fatalf("allocated reservation not reused once: %+v vs %+v, %d launches", rec.Launch, reserved, len(f.starts))
	}
}

// R1/R2/F4: a dispatched launch with no holder, marker or exit record is
// uncertain whatever the receipts say; a start refuses and names it, and only
// --new-session retires it. Same-token evidence that appears later resolves it.
func TestDispatchedLaunchWithoutEvidenceIsUncertain(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	f.startErr = errors.New("lost reply")
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err == nil {
		t.Fatal("a lost holder reply succeeded")
	}
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Launch.State != agentsession.StateUncertain || !strings.Contains(rec.Launch.Detail, "lost reply") {
		t.Fatalf("lost launch = %+v", rec.Launch)
	}
	f.startErr = nil
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "may have launched") || !strings.Contains(err.Error(), "--new-session") {
		t.Fatalf("uncertain launch not refused: %v", err)
	}
	if len(f.starts) != 1 {
		t.Fatal("an uncertain launch was replayed")
	}
	again, _, _ := agentsession.Load("work", "coder")
	if again.Launch.Generation != rec.Launch.Generation || again.Launch.State != agentsession.StateUncertain {
		t.Fatalf("refusal changed the record: %+v", again.Launch)
	}
	// A tombstone of that same launch closes it after all.
	l := f.begin(cfg)
	if err := l.noteExit(session.Tombstone{ExitCode: 0, ExitedAt: time.Unix(5, 0), LaunchToken: rec.Launch.Token}); err != nil {
		t.Fatal(err)
	}
	l.Release()
	closed, _, _ := agentsession.Load("work", "coder")
	if closed.Launch.State != agentsession.StateClosed {
		t.Fatalf("same-token exit did not close the uncertain launch: %+v", closed.Launch)
	}
}

func TestNewSessionRetiresAnUncertainLaunch(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	f.startErr = errors.New("lost reply")
	_, _ = f.launchOnce(cfg, sessionStartOptions{})
	old, _, _ := agentsession.Load("work", "coder")
	f.startErr = nil
	rec, err := f.launchOnce(cfg, sessionStartOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Assigned.ID == old.Assigned.ID || len(rec.Previous) != 1 || rec.Previous[0].ID != old.Assigned.ID || rec.Previous[0].Why != agentsession.WhyLaunchUncertain {
		t.Fatalf("new session after uncertainty = %+v", rec)
	}
}

// A dispatched launch whose marker never appeared, then the box restarted:
// only an interrupted resume is resumed; a first assignment stays uncertain.
func TestFreshSandboxProofResumesOnlyKnownConversations(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	tr := f.tracking(cfg)
	lock, err := agentsession.LockBox("work", "coder")
	if err != nil {
		t.Fatal(err)
	}
	if err := applySessionStart(lock, tr, true, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Launch.State != agentsession.StateClosed {
		t.Fatalf("a launched conversation was not closed by the restart proof: %+v", rec.Launch)
	}
	delete(f.marker, "coder")
	f.unit["coder"] = false
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil || !slices.Contains(f.starts[1], "--resume") {
		t.Fatalf("the restarted box did not resume: %v %v", err, f.starts)
	}

	// Without the proof, an assignment whose holder vanished stays uncertain.
	g := newIdentityFixture(t)
	g.startErr = errors.New("lost reply")
	_, _ = g.launchOnce(cfg, sessionStartOptions{})
	lock, _ = agentsession.LockBox("work", "coder")
	if err := applySessionStart(lock, tr, true, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	g.startErr = nil
	if _, err := g.launchOnce(cfg, sessionStartOptions{}); err == nil {
		t.Fatal("an interrupted first assignment was resumed or reassigned")
	}
}

// F11: restart --new-session --no-attach retires after the teardown and
// launches nothing.
func TestNewSessionWithoutAttachOnlyRetires(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	first, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	lock, _ := agentsession.LockBox("work", "coder")
	if err := applySessionStart(lock, f.tracking(cfg), true, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Assigned != nil || rec.Launch != nil || len(rec.Previous) != 1 || rec.Previous[0].ID != first.Assigned.ID || rec.Previous[0].Why != agentsession.WhyNewSession {
		t.Fatalf("retired record = %+v", rec)
	}
	if len(f.starts) != 1 {
		t.Fatal("retirement launched a session")
	}
	next, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil || next.Assigned.ID == first.Assigned.ID || !slices.Contains(f.starts[1], "--session-id") {
		t.Fatalf("next session did not start a new conversation: %v %+v", err, next)
	}
}

// R6/F8: another box of the same account and agent that runs, dispatched or
// left uncertain the same conversation stops a resume here; a closed one
// does not.
func TestResumeRefusesAConversationOwnedElsewhere(t *testing.T) {
	f := newIdentityFixture(t)
	f.names = []string{"coder", "other"}
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	mine, _, _ := agentsession.Load("work", "coder")
	for _, tc := range []struct {
		state string
		live  bool
		want  string
	}{
		{agentsession.StateLaunched, true, "is running in box other"},
		{agentsession.StateDispatching, false, "is dispatching in box other"},
		{agentsession.StateUncertain, false, "is uncertain in box other"},
		{agentsession.StateLaunched, false, ""},
		{agentsession.StateClosed, false, ""},
	} {
		lock, _ := agentsession.LockBox("work", "other")
		other := agentsession.NewRecord(agentsession.Scope{Account: "main", Provider: "claude"})
		other.LastGeneration = 1
		other.Assigned = &agentsession.Assigned{ID: mine.Assigned.ID, At: time.Unix(1, 0)}
		other.Launch = &agentsession.Launch{Generation: 1, Kind: agentsession.KindResume, State: tc.state, Token: strings.Repeat("c", 32), At: time.Unix(1, 0)}
		if err := agentsession.Save(lock, other); err != nil {
			t.Fatal(err)
		}
		lock.Release()
		f.unit["other"] = tc.live
		l := f.begin(cfg)
		err := l.checkOwner(t.Context(), container.New("work"), mine.Assigned.ID)
		l.Release()
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s live=%v: %v", tc.state, tc.live, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s live=%v: err = %v, want %q", tc.state, tc.live, err, tc.want)
		}
	}
	// Another box whose agent last reported this conversation owns it too.
	lock, _ := agentsession.LockBox("work", "other")
	other := agentsession.NewRecord(agentsession.Scope{Account: "main", Provider: "claude"})
	other.LastGeneration = 1
	other.Assigned = &agentsession.Assigned{ID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", At: time.Unix(1, 0)}
	other.Current = &agentsession.Current{ID: mine.Assigned.ID, Source: "resume", At: time.Unix(1, 0), ReadAt: time.Unix(1, 0), Generation: 1}
	other.Launch = &agentsession.Launch{Generation: 1, Kind: agentsession.KindResume, State: agentsession.StateLaunched, Token: strings.Repeat("c", 32), At: time.Unix(1, 0)}
	if err := agentsession.Save(lock, other); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	f.unit["other"] = true
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "conversation "+mine.Assigned.ID+" is running in box other") {
		t.Fatalf("a conversation another box last reported is refused: %v", err)
	}
	// The resume target, not the assignment, is checked.
	f.unit["other"] = false
	lock, _ = agentsession.LockBox("work", "coder")
	mine, _, _ = agentsession.Load("work", "coder")
	mine.Current = &agentsession.Current{ID: other.Assigned.ID, Source: "clear", At: time.Unix(1, 0), ReadAt: time.Unix(1, 0), Generation: mine.LastGeneration}
	_ = agentsession.Save(lock, mine)
	lock.Release()
	f.unit["other"] = true
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "conversation "+other.Assigned.ID+" is running in box other") {
		t.Fatalf("the adopted target is checked: %v", err)
	}
	if rec, _, _ := agentsession.Load("work", "coder"); rec.Assigned.ID != mine.Assigned.ID || rec.Launch.State != agentsession.StateClosed {
		t.Fatalf("a refused start changes nothing: %+v", rec)
	}
}

// Policy never starts a new conversation every time and keeps the old IDs.
func TestNeverPolicyReplacesEachConversation(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	cfg.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
	first, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	second, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Assigned.ID == first.Assigned.ID || slices.Contains(f.starts[1], "--resume") || second.Previous[0].Why != agentsession.WhyNever {
		t.Fatalf("never policy resumed: %v %+v", f.starts[1], second)
	}
}

// A mismatch outcome keeps the record and refuses nothing (D3 (a)): without
// a reported conversation the next start requests the same one again, and
// --new-session still replaces it.
func TestMismatchOutcomeKeepsTheRecordAndResumes(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	lock, _ := agentsession.LockBox("work", "coder")
	rec, _, _ := agentsession.Load("work", "coder")
	rec.Resume.Outcome = agentsession.OutcomeMismatch
	_ = agentsession.Save(lock, rec)
	lock.Release()
	f.exit("coder", 0)
	f.noteExit(cfg)
	again, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil || again.Assigned.ID != rec.Assigned.ID || !slices.Contains(f.starts[2], rec.Assigned.ID) || len(again.Previous) != 0 {
		t.Fatalf("a mismatch outcome must neither refuse nor replace the conversation: %v %+v %v", err, again, f.starts)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	fresh, err := f.launchOnce(cfg, sessionStartOptions{NewSession: true})
	if err != nil || fresh.Assigned.ID == rec.Assigned.ID || fresh.Previous[0].Why != agentsession.WhyNewSession {
		t.Fatalf("--new-session replaces it: %v %+v", err, fresh)
	}
}

// The wrapper's space lock is the witness for a tracked start: missing or
// already released, the start is refused before any lock or record.
func TestTrackedStartNeedsTheHeldSpaceLock(t *testing.T) {
	f := newIdentityFixture(t)
	tr := f.tracking(claudeBox())
	if _, err := beginTrackedSession(t.Context(), tr, "work", "coder"); err == nil {
		t.Fatal("tracked start without the space lock")
	}
	released := context.WithValue(t.Context(), spaceHeldKey{}, func() bool { return false })
	if _, err := beginTrackedSession(released, tr, "work", "coder"); err == nil {
		t.Fatal("tracked start after the space lock was released")
	}
	if rec, ok, _ := agentsession.Load("work", "coder"); ok || rec != nil {
		t.Fatal("a refused start wrote a record")
	}
}

// Agentless, manual and unsupported boxes are never tracked and so never
// take a box lock (the chat server path may already hold it); Claude and
// Codex boxes are.
func TestOnlyDeclaredSessionBoxesAreTracked(t *testing.T) {
	newIdentityFixture(t)
	for name, cfg := range map[string]*agentbox.Config{
		"agentless": {Name: "chat", Space: "work", Agent: "none", Account: "none"},
		"manual":    {Name: "m", Space: "work", Agent: "claude", Account: "main", AgentSession: &agentbox.AgentSession{Resume: "manual"}},
		"legacy":    claudeBox("--continue"),
		"gemini":    {Name: "gm", Space: "work", Agent: "gemini", Account: "main"},
		"pinned":    {Name: "astra", Space: "work", Agent: "codex", Account: "main", AgentExtraArgs: []string{"resume", "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"}},
	} {
		if tr, _, err := sessionTrackingFor(cfg); err != nil || tr != nil {
			t.Errorf("%s: tracked=%v err=%v", name, tr != nil, err)
		}
	}
	if tr, _, err := sessionTrackingFor(&agentbox.Config{Name: "cx", Space: "work", Agent: "codex", Account: "main"}); err != nil || tr == nil {
		t.Errorf("a default Codex box is tracked: tracked=%v err=%v", tr != nil, err)
	}
	explicit := claudeBox("--resume", "x")
	explicit.AgentSession = &agentbox.AgentSession{Resume: "last"}
	if _, _, err := sessionTrackingFor(explicit); err == nil || !strings.Contains(err.Error(), "--agent-session-resume manual") {
		t.Fatalf("explicit last with --resume = %v", err)
	}
}

// An attach that finds its own holder live settles a launch left
// dispatching; a holder without the token (one that predates tracking)
// mints and changes nothing.
func TestLiveHolderBindsOnlyItsOwnLaunch(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	f.unit["coder"] = true
	f.marker["coder"] = session.Marker{StartedAt: time.Unix(9, 0), HolderPID: 3}
	bindLiveLaunch(held(t.Context()), container.New("work"), "work", "coder")
	if _, ok, _ := agentsession.Load("work", "coder"); ok {
		t.Fatal("a session that predates tracking minted a record")
	}
	lock, _ := agentsession.LockBox("work", "coder")
	rec := agentsession.NewRecord(f.tracking(cfg).scope)
	_ = rec.Allocate(agentsession.KindAssign, sessionNow())
	_ = rec.Dispatch(1, rec.Launch.Token, "req", "inc", sessionNow())
	_ = agentsession.Save(lock, rec)
	lock.Release()
	bindLiveLaunch(held(t.Context()), container.New("work"), "work", "coder")
	if got, _, _ := agentsession.Load("work", "coder"); got.Launch.State != agentsession.StateDispatching {
		t.Fatalf("another holder bound the launch: %+v", got.Launch)
	}
	f.marker["coder"] = session.Marker{StartedAt: time.Unix(9, 0), HolderPID: 3, LaunchToken: rec.Launch.Token}
	bindLiveLaunch(t.Context(), container.New("work"), "work", "coder")
	if got, _, _ := agentsession.Load("work", "coder"); got.Launch.State != agentsession.StateDispatching {
		t.Fatal("bound without the space lock")
	}
	bindLiveLaunch(held(t.Context()), container.New("work"), "work", "coder")
	if got, _, _ := agentsession.Load("work", "coder"); got.Launch.State != agentsession.StateLaunched || got.Launch.Holder != "3@1970-01-01T00:00:09Z" {
		t.Fatalf("own live holder did not bind: %+v", got.Launch)
	}
}

func TestRestartOfferDescribesConversations(t *testing.T) {
	for _, tc := range []struct {
		status *agentsession.Status
		want   string
	}{
		{nil, "resume it yourself"},
		{&agentsession.Status{Tracked: true, ID: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", Policy: "last (default)"}, "requests conversation 0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d again"},
		{&agentsession.Status{Tracked: true, ID: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", Policy: "last (default)",
			Current: &agentsession.CurrentView{ID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Source: "clear"}}, "requests conversation 7c9e6679-7425-40de-944b-e07fc1f90ae7, the one the agent last reported"},
		{&agentsession.Status{Tracked: true, Note: agentsession.Untracked}, "started before tracking"},
		{&agentsession.Status{Note: "not tracked: manual"}, "not resumed automatically"},
	} {
		if got := restartConversationNote(tc.status); !strings.Contains(got, tc.want) || strings.Contains(got, "resumed successfully") {
			t.Errorf("%+v: %q, want %q", tc.status, got, tc.want)
		}
	}
}

// The admin attach fast path joins a live holder without the ensure;
// --new-session must never take it.
func TestNewSessionBypassesTheAttachFastPath(t *testing.T) {
	if !attachFastPathEligible(true, true, "box attach", []string{"coder"}, false, false) {
		t.Fatal("the ordinary admin attach lost its fast path")
	}
	for _, path := range []string{"box attach", "attach"} {
		if attachFastPathEligible(true, true, path, []string{"coder"}, false, true) {
			t.Fatalf("%s --new-session took the fast path", path)
		}
	}
}

// The witness holds exactly while the command wrapper's space lock does: it
// is absent before the wrapper and false once the operation is released.
func TestSpaceOperationWitnessEndsAtRelease(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if spaceOperationHeld(t.Context()) {
		t.Fatal("a bare context claimed the space lock")
	}
	var during, after bool
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	attach := &cobra.Command{Use: "attach", RunE: func(cmd *cobra.Command, _ []string) error {
		during = spaceOperationHeld(cmd.Context())
		if err := releaseSpaceOperation(cmd.Context()); err != nil {
			return err
		}
		after = spaceOperationHeld(cmd.Context())
		return nil
	}}
	attach.Flags().String("space", "work", "")
	attach.Flags().Bool(newSessionFlag, false, "")
	box.AddCommand(attach)
	root.AddCommand(box)
	registerSpaceOperations(root)
	root.SetArgs([]string{"box", "attach", "coder", "--space", "work", "--new-session"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !during || after {
		t.Fatalf("witness during=%v after release=%v", during, after)
	}
}

// B1: an undecidable holder or marker probe of another box that recorded the
// same conversation as launched refuses the resume; only a definite absence
// allows it. Nothing launches and the record stays as it was.
func TestOwnershipInventoryRefusesUnknownLiveness(t *testing.T) {
	f := newIdentityFixture(t)
	f.names = []string{"coder", "other"}
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	mine, _, _ := agentsession.Load("work", "coder")
	lock, _ := agentsession.LockBox("work", "other")
	other := agentsession.NewRecord(agentsession.Scope{Account: "main", Provider: "claude"})
	other.LastGeneration = 1
	other.Assigned = &agentsession.Assigned{ID: mine.Assigned.ID, At: time.Unix(1, 0)}
	other.Launch = &agentsession.Launch{Generation: 1, Kind: agentsession.KindResume, State: agentsession.StateLaunched, Token: strings.Repeat("c", 32), At: time.Unix(1, 0)}
	if err := agentsession.Save(lock, other); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	reset := func() {
		f.unitErr, f.markerErr = map[string]error{}, map[string]error{}
		delete(f.marker, "other")
	}
	for _, tc := range []struct {
		name, want string
		set        func()
	}{
		{"unit probe error", "ownership inventory uncertain: box other", func() { f.unitErr["other"] = errors.New("ssh timed out") }},
		{"malformed marker", "ownership inventory uncertain: box other", func() { f.markerErr["other"] = errors.New("session marker of other is unreadable or malformed") }},
		{"marker without a unit", "is running in box other", func() { f.marker["other"] = session.Marker{StartedAt: time.Unix(1, 0), HolderPID: 9} }},
	} {
		reset()
		tc.set()
		launches := len(f.starts)
		_, err := f.launchOnce(cfg, sessionStartOptions{})
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(f.starts) != launches {
			t.Errorf("%s: err = %v, launches %d→%d", tc.name, err, launches, len(f.starts))
		}
		if rec, _, _ := agentsession.Load("work", "coder"); rec.Launch.State != agentsession.StateClosed || rec.Launch.Generation != mine.Launch.Generation {
			t.Errorf("%s: the refusal changed the record: %+v", tc.name, rec.Launch)
		}
	}
	reset()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatalf("definite absence elsewhere still refused: %v", err)
	}
}

// B1: --new-session on a running box refuses with the record unchanged while
// the holder or its marker cannot be ruled out.
func TestNewSessionRefusesUnknownLiveness(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	before, _, _ := agentsession.Load("work", "coder")
	reset := func() {
		f.unitErr, f.markerErr, f.unit = map[string]error{}, map[string]error{}, map[string]bool{}
		delete(f.marker, "coder")
	}
	for name, set := range map[string]func(){
		"unit probe error":   func() { f.unitErr["coder"] = errors.New("service manager unavailable") },
		"marker probe error": func() { f.markerErr["coder"] = errors.New("probe failed") },
		"live holder":        func() { f.unit["coder"] = true },
		"leftover marker":    func() { f.marker["coder"] = session.Marker{StartedAt: time.Unix(1, 0), HolderPID: 9} },
	} {
		reset()
		set()
		if err := liveHolderError(t.Context(), container.New("work"), "coder"); err == nil {
			t.Errorf("%s: --new-session allowed", name)
		}
		if rec, _, _ := agentsession.Load("work", "coder"); !reflect.DeepEqual(rec, before) {
			t.Errorf("%s: record changed", name)
		}
	}
	reset()
	if err := liveHolderError(t.Context(), container.New("work"), "coder"); err != nil {
		t.Fatalf("definite absence refused: %v", err)
	}
}

// B2: retirement never resets the launch generation, so a later launch never
// takes an earlier conversation's generation (and its observation directory).
func TestGenerationsStayMonotonicAcrossRetirement(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	for i, opts := range []sessionStartOptions{{}, {NewSession: true}, {NewSession: true}, {}} {
		rec, err := f.launchOnce(cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Launch.Generation != uint64(i+1) || rec.LastGeneration != rec.Launch.Generation {
			t.Fatalf("launch %d reused or skipped a generation: %+v (last %d)", i, rec.Launch, rec.LastGeneration)
		}
		f.exit("coder", 0)
		f.noteExit(cfg)
	}
	never := claudeBox()
	never.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
	rec, err := f.launchOnce(never, sessionStartOptions{})
	if err != nil || rec.Launch.Generation != 5 || len(rec.Previous) != 3 {
		t.Fatalf("never policy restarted the generations: %+v %v", rec, err)
	}
}

// B3: a record that cannot be removed stops destroy before the declaration
// goes, so a later box of the same name never inherits the conversation; a
// repeated destroy finishes, and the next box of that name starts untracked.
func TestDestroyKeepsTheDeclarationWhenTheRecordStays(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	cfg.SchemaVersion = agentbox.SchemaVersion
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := layout.BoxPath("work", "coder")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("declared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := removeSessionRecordFn
	t.Cleanup(func() { removeSessionRecordFn = old })
	removeSessionRecordFn = func(string, string) error { return errors.New("disk busy") }
	if err := removeAgentboxConfig("work", "coder", io.Discard); err == nil || !strings.Contains(err.Error(), "its declaration is kept. Inspect the cause, then run yoloctl box destroy coder --space work again") {
		t.Fatalf("record removal failure did not stop destroy: %v", err)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("the declaration was deleted although the record stayed")
	}
	if _, ok, _ := agentsession.Load("work", "coder"); !ok {
		t.Fatal("the record vanished")
	}
	removeSessionRecordFn = old
	if err := removeAgentboxConfig("work", "coder", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := agentsession.Load("work", "coder"); ok {
		t.Fatal("the repeated destroy left the record")
	}
	// A new box of the same name starts with a new conversation.
	rec, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil || len(rec.Previous) != 0 || rec.Launch.Generation != 1 || !slices.Contains(f.starts[len(f.starts)-1], "--session-id") {
		t.Fatalf("same-name box inherited state: %+v %v", rec, err)
	}
}

// B1 at the attach entry: for a tracked box an undecidable holder unit or
// marker probe refuses before any record change, launch or container work,
// instead of reading as "no session".
func TestTrackedEnsureRefusesUndecidableProbes(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	cfg.SchemaVersion = agentbox.SchemaVersion
	cfg.Image = defaultAgentboxImage
	cfg.Workdir = agentbox.NewEphemeralWorkdir(cfg.Name)
	manifest, _ := layout.BoxPath("work", "coder")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: "coder", Status: "Up"}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionNone}
			},
		}
	}
	for name, set := range map[string]func(){
		"unit probe error":   func() { f.unitErr["coder"] = errors.New("service manager unavailable") },
		"marker probe error": func() { f.markerErr["coder"] = errors.New("probe failed") },
	} {
		f.unitErr, f.markerErr = map[string]error{}, map[string]error{}
		set()
		_, err := ensureAgentSession(held(t.Context()), container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{})
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s: err = %v", name, err)
		}
		if _, ok, _ := agentsession.Load("work", "coder"); ok {
			t.Errorf("%s: a record was written", name)
		}
	}
}

// observeLast records, between launches, the last observation as the
// reporter would: conversation id started at start, and effort and model
// reported by id itself at effortAt (none when empty).
func (f *identityFixture) observeLast(cfg *agentbox.Config, id string, start, effortAt time.Time, effort, model string) {
	f.t.Helper()
	l := f.begin(cfg)
	defer l.Release()
	o := &agentsession.Observed{ID: id, At: start, Generation: l.rec.Launch.Generation}
	if effort != "" {
		o.Effort = &agentsession.ObservedValue{Value: effort, At: effortAt, Session: id}
	}
	if model != "" {
		o.Model = &agentsession.ObservedValue{Value: model, At: effortAt, Session: id}
	}
	l.rec.Observed = o
	if err := l.save(); err != nil {
		f.t.Fatal(err)
	}
}

// A resume carries the effort and model its own conversation last reported,
// after the box's own args, so they win over the box's --effort/--model;
// anything else (no report, an unlisted effort, a model that is not a plain
// id, another conversation's reports, a new conversation) leaves the argv as
// today.
func TestResumeCarriesTheConversationsLastEffortAndModel(t *testing.T) {
	start := time.Unix(150, 0)
	box := []string{"--effort", "high", "--model", "opus"}
	for _, c := range []struct {
		name, effort, model string
		other               bool
		want                []string // tail after the resume's default args
	}{
		{name: "observed xhigh and fable", effort: "xhigh", model: "claude-fable-5-1", want: append(slices.Clone(box), "--effort", "xhigh", "--model", "claude-fable-5-1")},
		{name: "effort only", effort: "max", want: append(slices.Clone(box), "--effort", "max")},
		{name: "no report", want: box},
		{name: "unlisted effort, option-like model", effort: "ultra", model: "--append-system-prompt=x", want: box},
		{name: "another conversation", effort: "xhigh", model: "claude-fable-5-1", other: true, want: box},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			cfg := claudeBox(box...)
			rec, err := f.launchOnce(cfg, sessionStartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if first := f.starts[0]; !slices.Equal(first[len(first)-len(box):], box) {
				t.Fatalf("a new conversation changed: %v", first)
			}
			id := rec.Assigned.ID
			f.exit("coder", 0)
			f.noteExit(cfg)
			observed := id
			if c.other {
				observed = "0f3c2a4e-9b1d-4c8e-a7f6-1234567890ab"
			}
			f.observeLast(cfg, observed, start, start.Add(time.Second), c.effort, c.model)
			if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
				t.Fatal(err)
			}
			argv := f.starts[1]
			if i := slices.Index(argv, "--resume"); i < 0 || argv[i+1] != id {
				t.Fatalf("not a resume of %s: %v", id, argv)
			}
			if !slices.Equal(argv[len(argv)-len(c.want):], c.want) || slices.Contains(argv, "--append-system-prompt=x") {
				t.Fatalf("argv %v, want it to end with %v", argv, c.want)
			}
		})
	}
}
