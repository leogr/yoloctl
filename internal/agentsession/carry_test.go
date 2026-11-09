package agentsession

import (
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

// Only values a report of the conversation itself set are carried.
func TestCarriedIsTheConversationsOwnReport(t *testing.T) {
	at := time.Unix(100, 0)
	own := func(session string) *Record {
		return &Record{Observed: &Observed{ID: convA, At: at,
			Effort: &ObservedValue{Value: "xhigh", At: at, Session: session},
			Model:  &ObservedValue{Value: "claude-fable-5-1", At: at, Session: session}}}
	}
	for _, c := range []struct {
		name string
		rec  *Record
		id   string
		want Carried
	}{
		{"its own report", own(convA), convA, Carried{Effort: "xhigh", Model: "claude-fable-5-1"}},
		{"another conversation's report", own(convB), convA, Carried{}},
		{"a report naming no conversation", own(""), convA, Carried{}},
		{"model only", &Record{Observed: &Observed{ID: convA, Model: &ObservedValue{Value: "opus", Session: convA}}}, convA, Carried{Model: "opus"}},
		{"no values", &Record{Observed: &Observed{ID: convA}}, convA, Carried{}},
		{"no observation", &Record{}, convA, Carried{}},
		{"no conversation", own(convA), "", Carried{}},
		{"nil record", nil, convA, Carried{}},
	} {
		if got := c.rec.Carried(c.id); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Through the real fold: a value reported by another conversation, or one
// reported just before a switch in the same second, is never carried into
// the conversation that is current (archyAstra's review cases).
func TestCarriedFollowsReportProvenanceThroughApply(t *testing.T) {
	inSession := func(id string, edit func(*observe.Observation)) func(*observe.Observation) {
		return func(o *observe.Observation) { o.SessionID = id; edit(o) }
	}
	effort := func(e string) func(*observe.Observation) { return func(o *observe.Observation) { o.Effort = e } }
	model := func(m string) func(*observe.Observation) { return func(o *observe.Observation) { o.Model = m } }

	// Started A; a later Stop and status line name B: nothing is A's.
	r := resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "Stop", inSession(convB, effort("max"))),
		line(3, 30, observe.StatusLine, inSession(convB, model("claude-opus-5-5")))))
	if got := r.Carried(convA); got != (Carried{}) {
		t.Fatalf("B's reports carried into A: %+v", got)
	}

	// A's effort, then a switch to B in the same second: B carries nothing,
	// A keeps its own value.
	r = resuming()
	same := func(edit func(*observe.Observation)) func(*observe.Observation) {
		return func(o *observe.Observation) { edit(o); o.AgentTS = time.Unix(2010, 0).UTC() }
	}
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "Stop", same(inSession(convA, effort("high")))),
		line(3, 30, "SessionStart", same(start(convB, "clear")))))
	if got := r.Carried(convB); got != (Carried{}) {
		t.Fatalf("A's effort carried into B: %+v", got)
	}
	if got := r.Carried(convA); got.Effort != "high" {
		t.Fatalf("A lost its own effort: %+v", got)
	}

	// A's own Stop and status line carry both.
	r = resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "Stop", inSession(convA, effort("xhigh"))),
		line(3, 30, observe.StatusLine, inSession(convA, model("claude-fable-5-1")))))
	if got := r.Carried(convA); got != (Carried{Effort: "xhigh", Model: "claude-fable-5-1"}) {
		t.Fatalf("A's own reports: %+v", got)
	}

	// A report without a conversation is not attributed.
	r = resuming()
	r.applyDefault(result(3, observe.HealthOK,
		line(3, 10, "SessionStart", start(convA, "resume")),
		line(3, 20, "Stop", effort("max"))))
	if got := r.Carried(convA); got != (Carried{}) {
		t.Fatalf("an unattributed report carried: %+v", got)
	}
}

func TestCarryArgsOnlyForListedEffortAndPlainModel(t *testing.T) {
	claude, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	codex, err := agentprofile.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := CarryArgs(claude, Carried{Effort: "xhigh", Model: "claude-fable-5-1[1m]"}); !slices.Equal(got, []string{"--effort", "xhigh", "--model", "claude-fable-5-1[1m]"}) {
		t.Fatalf("claude both: %v", got)
	}
	if got := CarryArgs(claude, Carried{Effort: "ultra", Model: "opus"}); !slices.Equal(got, []string{"--model", "opus"}) {
		t.Fatalf("an unlisted effort is dropped, the model kept: %v", got)
	}
	for _, model := range []string{"", "--dangerously-skip-permissions", "-x", "Opus 5.5", "a;b", "claude\nopus", string(make([]byte, 129))} {
		if got := CarryArgs(claude, Carried{Model: model}); got != nil {
			t.Errorf("model %q carried %v", model, got)
		}
	}
	for _, effort := range []string{"", "ultra", "none", "persistent"} {
		if got := CarryArgs(claude, Carried{Effort: effort}); got != nil {
			t.Errorf("effort %q carried %v", effort, got)
		}
	}
	if got := CarryArgs(codex, Carried{Effort: "high", Model: "gpt-6"}); got != nil {
		t.Errorf("codex carried %v; it declares no resume_carry", got)
	}
}
