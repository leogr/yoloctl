package agentsession

import (
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

// applyDefault folds res under the default rule: only a start report names
// the conversation (Claude, Codex).
func (r *Record) applyDefault(res observe.Result) bool { return r.Apply(res, Fold{}) }

// statusLineRule is agy's rule: its status line names the conversation.
var statusLineRule = Fold{StatusLineIdentity: true}

const agyLabel = "Gemini 3.8 Flash (High)"

func agyLine(id, model string, edit func(*observe.Observation)) func(*observe.Observation) {
	return func(o *observe.Observation) {
		o.SessionID, o.Model, o.Version = id, model, "1.5.3"
		if edit != nil {
			edit(o)
		}
	}
}

func agyLearning(t *testing.T) *Record {
	t.Helper()
	r := NewRecord(Scope{Account: "main", Provider: "agy"})
	if err := r.Allocate(KindLearn, t0); err != nil {
		t.Fatal(err)
	}
	r.Launch.State = StateLaunched
	return r
}

func TestFoldForTheProfilesIdentityRule(t *testing.T) {
	for name, want := range map[string]bool{"agy": true, "claude": false, "codex": false} {
		p, err := agentprofile.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := FoldFor(p).StatusLineIdentity; got != want {
			t.Errorf("%s: status line identity %v, want %v", name, got, want)
		}
	}
	if FoldFor(nil) != (Fold{}) {
		t.Error("no profile is the default rule")
	}
}

// agy: the first main status line naming a valid conversation is learned;
// an empty, invalid, subagent or other-generation one is not, and a later
// conversation is current and a divergence, all labelled statusline.
func TestApplyLearnsTheConversationFromAStatusLine(t *testing.T) {
	r := agyLearning(t)
	r.Apply(result(2, observe.HealthOK, line(2, 5, observe.StatusLine, agyLine(convA, agyLabel, nil))), statusLineRule)
	r.Apply(result(1, observe.HealthOK,
		line(1, 10, observe.StatusLine, agyLine("", agyLabel, nil)),
		line(1, 20, observe.StatusLine, agyLine("not-a-uuid", agyLabel, nil)),
		line(1, 30, observe.StatusLine, agyLine(convB, agyLabel, func(o *observe.Observation) { o.Subagent = true })),
	), statusLineRule)
	if r.Assigned != nil || r.Current != nil || r.Observed.ID != "" {
		t.Fatalf("no qualifying report learns nothing: %+v %+v %+v", r.Assigned, r.Current, r.Observed)
	}
	if r.Observed.Model == nil || r.Observed.Model.Value != agyLabel || r.Observed.Model.Session != "" {
		t.Fatalf("an unnamed status line still reports an unattributed model: %+v", r.Observed.Model)
	}
	r.Apply(result(1, observe.HealthOK, line(1, 40, observe.StatusLine, agyLine(convA, agyLabel, nil))), statusLineRule)
	a, c := r.Assigned, r.Current
	if a == nil || a.ID != convA || a.Source != AssignedLearned || !a.At.Equal(time.Unix(2040, 0)) {
		t.Fatalf("the first main status line is learned: %+v", a)
	}
	if c == nil || c.ID != convA || c.Source != SourceStatusLine || c.Generation != 1 || r.Observed.ID != convA || r.Observed.Source != SourceStatusLine {
		t.Fatalf("current and observed name it, labelled statusline: %+v %+v", c, r.Observed)
	}
	if r.Divergence() != nil || r.Resume != nil {
		t.Fatalf("learning is neither a switch nor a resume: %+v %+v", r.Divergence(), r.Resume)
	}
	// The same conversation again (a status line runs on every state
	// change) changes no identity time.
	r.Apply(result(1, observe.HealthOK, line(1, 50, observe.StatusLine, agyLine(convA, agyLabel, nil))), statusLineRule)
	if !r.Current.At.Equal(time.Unix(2040, 0)) || !r.Observed.At.Equal(time.Unix(2040, 0)) {
		t.Fatalf("a repeated status line restamped identity: %+v %+v", r.Current, r.Observed)
	}
	r.Apply(result(1, observe.HealthOK, line(1, 60, observe.StatusLine, agyLine(convB, "Gemini 3.8 Pro", nil))), statusLineRule)
	if r.Assigned.ID != convA || r.Current.ID != convB || r.Current.Source != SourceStatusLine {
		t.Fatalf("a later conversation is current, the learned one stays: %+v %+v", r.Assigned, r.Current)
	}
	if d := r.Divergence(); d == nil || d.ID != convB || d.Source != SourceStatusLine {
		t.Fatalf("a later conversation is the divergence: %+v", d)
	}
	if got := r.Carried(convB); got.Model != "Gemini 3.8 Pro" {
		t.Fatalf("B's own model: %+v", got)
	}
	if got := r.Carried(convA); got.Model != "" {
		t.Fatalf("A carries B's model: %+v", got)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := r.Allocate(KindResume, t0); err != nil {
		t.Fatal(err)
	}
	if r.Assigned.ID != convB || r.Assigned.Source != AssignedSwitched || r.Resume.Requested != convB ||
		len(r.Previous) != 1 || r.Previous[0].Source != SourceStatusLine || r.Validate() != nil {
		t.Fatalf("the next resume adopts the reported switch: %+v %+v %+v %v", r.Assigned, r.Resume, r.Previous, r.Validate())
	}
}

// The default rule (Claude, Codex) keeps status lines out of identity.
func TestDefaultRuleKeepsStatusLinesOutOfIdentity(t *testing.T) {
	r := agyLearning(t)
	r.applyDefault(result(1, observe.HealthOK, line(1, 10, observe.StatusLine, agyLine(convA, agyLabel, nil))))
	if r.Assigned != nil || r.Current != nil || r.Observed.ID != "" || r.Divergence() != nil {
		t.Fatalf("the default rule learned from a status line: %+v %+v %+v", r.Assigned, r.Current, r.Observed)
	}
}

// A status line naming the requested conversation is not proof that it was
// restored: the resume stays unverified, and a nonzero exit after it is not
// settled as failed. Another conversation (an empty one first changes
// nothing) is a mismatch and a divergence, and a later report keeps it.
func TestStatusLineVerifiesNoResumeButReportsAMismatch(t *testing.T) {
	r := resuming()
	r.Provider = "agy"
	r.Current = &Current{ID: convA, Source: SourceStatusLine, At: time.Unix(1000, 0).UTC(), Generation: 2}
	r.Apply(result(3, observe.HealthOK, line(3, 10, observe.StatusLine, agyLine(convA, agyLabel, nil))), statusLineRule)
	if r.Resume.Outcome != OutcomeUnverified || r.Observed.ID != convA {
		t.Fatalf("the requested conversation: %+v %+v", r.Resume, r.Observed)
	}
	if c := r.Current; c.Generation != 3 || !c.At.Equal(time.Unix(2010, 0)) || c.Source != SourceStatusLine {
		t.Fatalf("a known conversation in a later generation refreshes current: %+v", c)
	}
	code := 1
	r.Launch.State, r.Launch.ExitCode = StateClosed, &code
	if r.SettleResume() || r.Resume.Outcome != OutcomeUnverified {
		t.Fatalf("a launch that named its conversation was settled: %+v", r.Resume)
	}

	r = resuming()
	r.Provider = "agy"
	r.Apply(result(3, observe.HealthOK, line(3, 5, observe.StatusLine, agyLine("", agyLabel, nil))), statusLineRule)
	if r.Resume.Outcome != OutcomeUnverified {
		t.Fatalf("an empty conversation decides nothing: %+v", r.Resume)
	}
	r.Apply(result(3, observe.HealthOK, line(3, 10, observe.StatusLine, agyLine(convB, agyLabel, nil)),
		line(3, 20, observe.StatusLine, agyLine(convA, agyLabel, nil))), statusLineRule)
	want := "the agent's status line reported conversation " + convB + " active instead of the requested " + convA
	if r.Resume.Outcome != OutcomeMismatch || r.Resume.Detail != want || r.Divergence() == nil || r.Divergence().ID != convB {
		t.Fatalf("another conversation is a mismatch, kept after a return: %+v %+v", r.Resume, r.Divergence())
	}
	if r.Current.ID != convA || r.Validate() != nil {
		t.Fatalf("the next resume follows the last reported one: %+v %v", r.Current, r.Validate())
	}
	r.Launch.State, r.Launch.ExitCode = StateClosed, &code
	if r.SettleResume() || r.Resume.Outcome != OutcomeMismatch {
		t.Fatalf("a mismatch is never settled again: %+v", r.Resume)
	}

	// No report at all: a nonzero exit is still a failed resume.
	r = resuming()
	r.Provider = "agy"
	r.Apply(result(3, observe.HealthNoReport), statusLineRule)
	r.Launch.State, r.Launch.ExitCode = StateClosed, &code
	if !r.SettleResume() || r.Resume.Outcome != OutcomeFailed {
		t.Fatalf("an unreported nonzero resume exit: %+v", r.Resume)
	}
}

// agy carries the model label its conversation reported as one argument.
func TestCarryArgsCarryAgysModelLabel(t *testing.T) {
	agy, err := agentprofile.Lookup("agy")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{agyLabel, "gemini-3.8-flash-high", "Gemini 3.8 Pro"} {
		if got := CarryArgs(agy, Carried{Model: model, Effort: "high"}); !slices.Equal(got, []string{"--model", model}) {
			t.Errorf("agy %q: %v", model, got)
		}
	}
	for _, model := range []string{"", "-x", "--model", " Gemini", "Gemini ", "Gemini  3", "Gemini\t3", "Gemini;3", "Gemini'3", "Gemini$(x)", "Gemini\n3", string(make([]byte, 129))} {
		if got := CarryArgs(agy, Carried{Model: model}); got != nil {
			t.Errorf("agy model %q carried %v", model, got)
		}
	}
	if got := CarryArgs(claude, Carried{Model: agyLabel}); got != nil {
		t.Errorf("claude carries only plain ids: %v", got)
	}
}
