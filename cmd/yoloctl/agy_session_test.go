package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

const agyModelLabel = "Gemini 3.8 Flash (High)"

func agyBox(args ...string) *agentbox.Config {
	return &agentbox.Config{Name: "coder", Space: "work", Agent: "agy", Account: "main",
		Features: []string{"agy", "github", "yolo.agent.observe-agy"}, AgentExtraArgs: args}
}

// agyStatusLine is one main status line report naming conversation id.
func agyStatusLine(off int64, id, model string) observe.Observation {
	return observe.Observation{Offset: off, Event: observe.StatusLine, SessionID: id, Model: model, Version: "1.5.3", AgentTS: time.Unix(off, 0).UTC()}
}

func describeAgy(t *testing.T, cfg *agentbox.Config, f *observeFixture) *agentsession.Status {
	t.Helper()
	profile, err := agentprofile.Lookup("agy")
	if err != nil {
		t.Fatal(err)
	}
	return agentsession.Describe(cfg, profile, f.unit[cfg.Name], f.marker[cfg.Name].LaunchToken)
}

// An agy box learns its conversation from the status line (view, live and
// persisted folds alike), resumes it with --conversation and carries its own
// model label as one argument after the box's --model; --new-session learns
// afresh with the box's arguments only.
func TestAgyLearnsFromItsStatusLineAndResumesWithItsModel(t *testing.T) {
	const conv = "0f8fad5b-d9cb-469f-a165-70867728950e"
	box := []string{"--model", "gemini-box-default"}
	f := newObserveFixture(t)
	cfg := agyBox(box...)
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := f.starts[0]
	if rec.Launch.Kind != agentsession.KindLearn || rec.Assigned != nil || slices.Contains(first, "--conversation") ||
		!slices.Equal(first[len(first)-len(box):], box) {
		t.Fatalf("the first start learns, with the box's args: %+v %v", rec.Launch, first)
	}

	// Before the reporter names it (an empty conversation) nothing is
	// learned; then it names conv.
	f.reports[1] = []observe.Observation{agyStatusLine(10, "", agyModelLabel), agyStatusLine(20, conv, agyModelLabel)}
	before, _ := recordBytes(t)
	if st := describeAgy(t, cfg, f); st.Current == nil || st.Current.ID != conv || st.Current.Source != agentsession.SourceStatusLine || st.ID != conv {
		t.Fatalf("a view folds agy's status line identity: %+v", st)
	}
	if after, _ := recordBytes(t); string(after) != string(before) {
		t.Fatal("a view wrote the record")
	}
	foldLiveLaunch(held(t.Context()), "work", "coder")
	rec, _, _ = agentsession.Load("work", "coder")
	if rec.Assigned == nil || rec.Assigned.ID != conv || rec.Assigned.Source != agentsession.AssignedLearned ||
		rec.Observed.Model == nil || rec.Observed.Model.Value != agyModelLabel || rec.Observed.Model.Session != conv {
		t.Fatalf("a held live join learns and attributes the model: %+v %+v", rec.Assigned, rec.Observed)
	}

	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err = f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	argv := f.starts[1]
	want := []string{"env", "YOLOCTL_AGENT_GEN=2", "env", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"agy", "--conversation", conv, "--dangerously-skip-permissions", "--sandbox=false",
		"--model", "gemini-box-default", "--model", agyModelLabel}
	if !slices.Equal(argv, want) {
		t.Fatalf("resume argv\n got %q\nwant %q", argv, want)
	}
	if rec.Resume == nil || rec.Resume.Requested != conv || rec.Resume.Outcome != agentsession.OutcomeUnverified {
		t.Fatalf("a resume of the learned conversation: %+v", rec.Resume)
	}
	// The requested conversation's status line does not verify the resume.
	f.reports[2] = []observe.Observation{agyStatusLine(10, conv, agyModelLabel)}
	f.exit("coder", 0)
	f.noteExit(cfg)

	rec, err = f.planObserved(cfg, sessionStartOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if fresh := f.starts[2]; slices.Contains(fresh, "--conversation") || slices.Contains(fresh, agyModelLabel) ||
		!slices.Equal(fresh[len(fresh)-len(box):], box) || rec.Launch.Kind != agentsession.KindLearn || rec.Assigned != nil {
		t.Fatalf("--new-session learns afresh with the box's args: %v %+v", fresh, rec.Launch)
	}
	if len(rec.Previous) != 1 || rec.Previous[0].ID != conv || rec.Previous[0].Why != agentsession.WhyNewSession {
		t.Fatalf("the learned conversation is retired: %+v", rec.Previous)
	}
}

// Without a report there is no known conversation: the next start learns
// again, never continues the account's latest one.
func TestAgyWithoutAReportLearnsAgain(t *testing.T) {
	f := newObserveFixture(t)
	cfg := agyBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if argv := f.starts[1]; slices.Contains(argv, "--conversation") || slices.Contains(argv, "--continue") || slices.Contains(argv, "-c") || rec.Launch.Kind != agentsession.KindLearn {
		t.Fatalf("no report learns again: %v %+v", argv, rec.Launch)
	}
}

// agy defaults to last; never and manual stay respected, and its identity
// arguments make an unset policy manual or refuse an explicit one.
func TestAgyResumePolicies(t *testing.T) {
	newIdentityFixture(t)
	if _, policy, err := sessionTrackingFor(agyBox()); err != nil || policy.Policy != agentsession.PolicyLast || policy.Declared {
		t.Fatalf("agy defaults to last: %+v %v", policy, err)
	}
	for _, arg := range [][]string{{"--conversation", "0f8fad5b-d9cb-469f-a165-70867728950e"}, {"--conversation=x"}, {"--continue"}, {"-c"}} {
		tr, policy, err := sessionTrackingFor(agyBox(arg...))
		if err != nil || tr != nil || policy.Policy != agentsession.PolicyManual {
			t.Errorf("%v: manual, untracked: %+v %v", arg, policy, err)
		}
		explicit := agyBox(arg...)
		explicit.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeLast}
		if _, _, err := sessionTrackingFor(explicit); err == nil || !strings.Contains(err.Error(), "--agent-session-resume manual") {
			t.Errorf("%v with explicit last: %v", arg, err)
		}
	}
	never := agyBox()
	never.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
	if tr, policy, err := sessionTrackingFor(never); err != nil || tr == nil || policy.Policy != agentsession.PolicyNever {
		t.Fatalf("never stays tracked: %+v %v", policy, err)
	}
}

// never starts a learn launch every time, even after a learned conversation.
func TestAgyNeverLearnsEachStart(t *testing.T) {
	const conv = "0f8fad5b-d9cb-469f-a165-70867728950e"
	f := newObserveFixture(t)
	cfg := agyBox()
	cfg.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{agyStatusLine(10, conv, agyModelLabel)}
	f.exit("coder", 0)
	f.noteExit(cfg)
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if argv := f.starts[1]; slices.Contains(argv, "--conversation") || slices.Contains(argv, agyModelLabel) || rec.Launch.Kind != agentsession.KindLearn ||
		len(rec.Previous) != 1 || rec.Previous[0].ID != conv || rec.Previous[0].Why != agentsession.WhyNever {
		t.Fatalf("never learns afresh: %v %+v %+v", argv, rec.Launch, rec.Previous)
	}
}

// agyBoxSession is box session's text for an agy box.
func agyBoxSession(t *testing.T, cfg *agentbox.Config, f *observeFixture) string {
	t.Helper()
	var out strings.Builder
	writeBoxSession(&out, &boxSessionView{Box: cfg.Name, Space: "work", Conversation: describeAgy(t, cfg, f)})
	return out.String()
}

// learnedAgy runs a learn launch that reports conv, then exits.
func learnedAgy(t *testing.T, f *observeFixture, cfg *agentbox.Config, conv string) {
	t.Helper()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[1] = []observe.Observation{agyStatusLine(10, conv, agyModelLabel)}
	f.exit("coder", 0)
	f.noteExit(cfg)
}

// The requested conversation on the status line leaves the resume
// unverified and says why; another one is a mismatch, and the next start
// follows it, as for any reported switch.
func TestAgyResumeOutcomeFromTheStatusLine(t *testing.T) {
	const conv, other = "0f8fad5b-d9cb-469f-a165-70867728950e", "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	f := newObserveFixture(t)
	cfg := agyBox()
	learnedAgy(t, f, cfg, conv)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reports[2] = []observe.Observation{agyStatusLine(10, conv, agyModelLabel)}
	text := agyBoxSession(t, cfg, f)
	for _, want := range []string{
		"Conversation: " + conv + "  assigned 1970-01-01T00:00:10Z (learned)\n",
		"Resume:       requested " + conv + " · generation 2 · unverified; the status line reports it selected, which does not prove its history was restored\n",
		"Observed:     " + conv + " · statusline · generation 2 · ok (agent-reported)\n",
		"model " + agyModelLabel + " (agent-reported by statusline at",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("box session lacks %q:\n%s", want, text)
		}
	}

	f.reports[2] = append(f.reports[2], agyStatusLine(20, other, "Gemini 3.8 Pro"))
	f.exit("coder", 0)
	f.noteExit(cfg)
	l := f.begin(cfg)
	if err := l.observeCurrentLaunch(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	l.Release()
	rec, _, _ := agentsession.Load("work", "coder")
	if rec.Resume.Outcome != agentsession.OutcomeMismatch || !strings.Contains(rec.Resume.Detail, "status line reported conversation "+other+" active instead of the requested "+conv) {
		t.Fatalf("another conversation is a mismatch: %+v", rec.Resume)
	}
	text = agyBoxSession(t, cfg, f)
	if !strings.Contains(text, "Current:      "+other+" · last observed status line report at") || !strings.Contains(text, "· mismatch (the agent's status line reported") {
		t.Fatalf("box session names the switch and the mismatch:\n%s", text)
	}
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if argv := f.starts[2]; !slices.Contains(argv, other) || argv[len(argv)-1] != "Gemini 3.8 Pro" {
		t.Fatalf("the next start resumes the reported conversation with its own model: %v", argv)
	}
}

// A closed learn launch that named no conversation says so; with readable
// but empty reports it names the explicit reporter opt-in and its conditions.
func TestAgyUnlearnedLaunchNamesTheReporterOptIn(t *testing.T) {
	f := newObserveFixture(t)
	cfg := agyBox()
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if text := agyBoxSession(t, cfg, f); strings.Contains(text, "none was learned") {
		t.Fatalf("a running learn launch has no note yet:\n%s", text)
	}
	f.exit("coder", 0)
	f.noteExit(cfg)
	text := agyBoxSession(t, cfg, f)
	want := "Note:         launch 1 reported no conversation, so none was learned; the next start begins a new one. " +
		"The agy status line reporter may be off for account main: stop every box that uses it, " +
		"run yoloctl account observe main agy --enable --space work (it needs the account's existing settings), then start again\n"
	if !strings.Contains(text, want) {
		t.Fatalf("box session:\n%s\nwant %q", text, want)
	}
	// An unavailable root keeps its own health; no reporter hint.
	g := newObserveFixture(t)
	g.prepErr = errors.New("tmpfs full")
	if _, err := g.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	g.exit("coder", 0)
	g.noteExit(cfg)
	if text := agyBoxSession(t, cfg, g); !strings.Contains(text, "none was learned; the next start begins a new one\n") || strings.Contains(text, "account observe") {
		t.Fatalf("an unavailable observation names no reporter fix:\n%s", text)
	}
}

// A resume that exits nonzero before any report is failed and retried with
// the same conversation. Carry is launch-scoped: that launch reported no
// model, so the retry carries none and the box's own arguments apply.
// Nothing selects a new conversation.
func TestAgyFailedResumeRetriesWithTheBoxArguments(t *testing.T) {
	const conv = "0f8fad5b-d9cb-469f-a165-70867728950e"
	f := newObserveFixture(t)
	cfg := agyBox("--model", "gemini-box-default")
	learnedAgy(t, f, cfg, conv)
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if argv := f.starts[1]; argv[len(argv)-1] != agyModelLabel {
		t.Fatalf("the resume carries the label: %v", argv)
	}
	f.exit("coder", 1)
	f.noteExit(cfg)
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "warning: the previous resume of " + conv + " failed (agent exited with status 1 at 1970-01-01T00:03:20Z); retrying — yoloctl box restart coder --new-session starts a new conversation\n"
	if f.notices.String() != want {
		t.Fatalf("retry warning:\n%q\nwant\n%q", f.notices.String(), want)
	}
	argv := f.starts[2]
	if rec.Resume.Requested != conv || !slices.Contains(argv, conv) || slices.Contains(argv, agyModelLabel) || argv[len(argv)-1] != "gemini-box-default" || len(rec.Previous) != 0 {
		t.Fatalf("the retry keeps the conversation with the box's model: %+v %v", rec.Resume, argv)
	}
}
