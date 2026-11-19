package chatui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// leogr's blink: a refresh that fails, or that cannot read one box's
// reports, must not blank that participant's model line. The last good
// values stay until they are older than runtimeFreshness.
func TestRefreshKeepsLastGoodValues(t *testing.T) {
	m := presenceModel(t)
	good := map[string]AgentRuntime{"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, Effort: RuntimeValue{"max", SourceHook}}}
	summary := func() string { return m.runtimeSummary(m.identify("", "mopus"), time.Now()) }
	m.Update(AgentsMsg{At: time.Now(), Runtime: good})
	if summary() != "Opus 5.5 (max)" {
		t.Fatal("fixture: a good read shows the model", summary())
	}

	m.Update(AgentsMsg{Err: errors.New("deadline exceeded")})
	if got := summary(); got != "Opus 5.5 (max)" {
		t.Fatalf("a failed refresh blanked the line: %q", got)
	}

	// A read that reached the controller but missed this box's reports.
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {Note: "the current reports could not be read", Unread: true}}})
	if got := summary(); got != "Opus 5.5 (max)" {
		t.Fatalf("a missed box read blanked the line: %q", got)
	}
	m.openCard("opus", "mopus")
	if card := view(m); !strings.Contains(card, "the last refresh could not read its reports") || strings.Contains(card, "stale:") {
		t.Fatalf("the card says the latest read missed its reports:\n%s", card)
	}
	m.overlay = nil

	// The next good read takes over.
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {Model: RuntimeValue{"claude-fable-5-1", SourceStatusLine}}}})
	if got := summary(); got != "Fable 5.1" {
		t.Fatalf("a newer good read replaces the kept values: %q", got)
	}

	// Kept values still age out: read over runtimeFreshness ago, then missed.
	m.Update(AgentsMsg{At: time.Now().Add(-runtimeFreshness - time.Minute), Runtime: good})
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {Unread: true}}})
	if got := summary(); got != "" {
		t.Fatalf("old kept values are stale: %q", got)
	}

	// With nothing kept, a missed read shows nothing; a real change (not
	// a transient miss) replaces the values.
	m.Update(AgentsMsg{At: time.Now(), Runtime: good})
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {Note: "the agent session is not running"}}})
	if got := summary(); got != "" {
		t.Fatalf("a stopped session is not kept: %q", got)
	}
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {Unread: true}}})
	if got := summary(); got != "" {
		t.Fatalf("a missed read with nothing kept shows nothing: %q", got)
	}
}
