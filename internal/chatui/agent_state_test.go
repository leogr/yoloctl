package chatui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

func stateRow(m *Model, account string) string {
	m.openCard(account[1:], account)
	defer func() { m.overlay = nil }()
	for _, line := range strings.Split(ansi.Strip(strings.Join(m.cardRows(100), "\n")), "\n") {
		if strings.Contains(line, "State ") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func stateTagOf(m *Model, account string) string {
	tag, _ := m.stateTag(m.identify(account[1:], account), time.Now())
	return tag
}

// A reported state has a State row on the card with the report that
// carried it; only a wait or an API error is tagged beside the name, and
// a failed or old read shows neither.
func TestReportedStateRowAndTag(t *testing.T) {
	m := presenceModel(t)
	at := time.Date(2026, 10, 28, 14, 2, 0, 0, time.Local)
	report := func(kind string) {
		m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{
			"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, State: RuntimeState{Kind: kind, Source: SourceHook, At: at}},
		}})
	}
	for _, c := range []struct {
		kind, row, tag string
	}{
		{StateWaitingPermission, "State         waiting for permission since 14:02 (reported by an agent hook", "⏳ needs permission"},
		{StateWaitingInput, "State         waiting for input since 14:02 (reported by an agent hook", "⏳ needs input"},
		{StateWaitingDialog, "State         waiting for a dialog since 14:02 (reported by an agent hook", "⏳ dialog"},
		{StateFailed, "State         API error at 14:02 (reported by an agent hook", "⚠ API error"},
		{StateWorking, "State         working (reported by an agent hook", ""},
		{StateIdle, "State         idle (reported by an agent hook", ""},
	} {
		report(c.kind)
		if got := stateRow(m, "mopus"); !strings.HasPrefix(got, c.row) {
			t.Errorf("%s row: %q, want %q…", c.kind, got, c.row)
		}
		if got := stateTagOf(m, "mopus"); got != c.tag {
			t.Errorf("%s tag: %q, want %q", c.kind, got, c.tag)
		}
		panel := view(m)
		if c.tag != "" && !strings.Contains(panel, c.tag) {
			t.Errorf("%s: panel lacks %q:\n%s", c.kind, c.tag, panel)
		}
		for _, words := range []string{"working", "idle"} {
			if strings.Contains(panel, words) {
				t.Errorf("%s: panel names %q:\n%s", c.kind, words, panel)
			}
		}
	}
	report(StateWaitingPermission)
	m.Update(AgentsMsg{Err: errors.New("controller busy")})
	if row, tag := stateRow(m, "mopus"), stateTagOf(m, "mopus"); row != "" || tag != "" {
		t.Fatalf("a failed read kept the state: %q %q", row, tag)
	}
	m.Update(AgentsMsg{At: time.Now().Add(-3 * time.Minute), Runtime: map[string]AgentRuntime{
		"mopus": {State: RuntimeState{Kind: StateWaitingPermission, Source: SourceHook, At: at}},
	}})
	if row, tag := stateRow(m, "mopus"), stateTagOf(m, "mopus"); row != "" || tag != "" {
		t.Fatalf("an old read kept the state: %q %q", row, tag)
	}
	// A box without an agent has no state, whatever it reports.
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{
		"msol": {State: RuntimeState{Kind: StateFailed, Source: SourceHook}},
	}})
	if row, tag := stateRow(m, "msol"), stateTagOf(m, "msol"); row != "" || tag != "" {
		t.Fatalf("an agentless box shows a state: %q %q", row, tag)
	}
}

// Without a reported state the card falls back to the box's activity:
// active is working, idle is idle, unknown is no row, and the fallback is
// never a wait, so it is never tagged.
func TestStateFallsBackToActivity(t *testing.T) {
	m := presenceModel(t)
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{
		"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}},
	}})
	if row := stateRow(m, "mopus"); row != "" {
		t.Fatalf("unknown activity is a state: %q", row)
	}
	m.Update(inventoryFrame(presenceInventory()))
	output := func(n uint64) {
		m.Update(sampleFrame(map[string]spacetelemetry.BoxSample{"opus": {PTY: &spacetelemetry.PTYCounters{Session: "s", OutputBytes: n}}}))
	}
	output(0)
	output(5)
	const from = "(from the box's activity; the agent reported no state)"
	if row := stateRow(m, "mopus"); row != "State         working "+from {
		t.Fatalf("active box: %q", row)
	}
	for range spacetelemetry.ActivityHold + 2 {
		output(5)
	}
	if a := m.boxActivity("mopus"); a != spacetelemetry.ActivityIdle {
		t.Fatalf("activity = %v, want idle", a)
	}
	if row := stateRow(m, "mopus"); row != "State         idle "+from {
		t.Fatalf("idle box: %q", row)
	}
	if tag := stateTagOf(m, "mopus"); tag != "" {
		t.Fatalf("the fallback was tagged: %q", tag)
	}
	// A reported state wins over activity.
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{
		"mopus": {State: RuntimeState{Kind: StateWaitingInput, Source: SourceStatusLine}},
	}})
	if tag := stateTagOf(m, "mopus"); tag != "⏳ needs input" {
		t.Fatalf("report over activity: %q", tag)
	}
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{"mopus": {}}})
	m.Update(TelemetryLostMsg{})
	if row := stateRow(m, "mopus"); row != "" {
		t.Fatalf("activity outlived the stream: %q", row)
	}
}
