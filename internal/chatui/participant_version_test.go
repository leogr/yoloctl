package chatui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

const agyHint = "no usable report has been observed for this launch; the agy status line reporter may be off for account leogr. " +
	"To turn it on: stop every box that uses account leogr, run yoloctl account observe leogr agy --enable --space yolospace, then start the boxes again."

func versionModel(at time.Time) *Model {
	m := New(Options{Space: "yolospace", Channel: "#space", Nick: "leo", Account: "mleo", NoMotion: true, Identities: map[string]Identity{
		"mleo": {Name: "leo", Human: true}, "mastra": {Name: "astra", Box: true}, "magy": {Name: "agy", Box: true},
		"mqa": {Name: "qa", Box: true},
	}})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.Update(MembersMsg{Channel: "#space", Members: []Member{
		{Nick: "leo", Account: "mleo"}, {Nick: "astra", Account: "mastra"}, {Nick: "agy", Account: "magy"}, {Nick: "qa", Account: "mqa"},
	}})
	m.Update(AgentsMsg{At: at, Agents: map[string]string{"mastra": "codex", "magy": "agy", "mqa": "codex"}, Runtime: map[string]AgentRuntime{
		"mastra": {Model: RuntimeValue{"gpt-6-astra", SourceQuery}, Effort: RuntimeValue{"high", SourceQuery},
			Version: RuntimeValue{"0.166.0", SourceQuery}},
		"magy": {Note: "agent reports are no-report", Hint: agyHint},
		"mqa":  {Note: "no agent reports for this box"},
	}})
	return m
}

func rowsOf(m *Model, account string, now time.Time) map[string]string {
	out := map[string]string{}
	for _, r := range m.runtimeRows(m.identify("", account), now) {
		out[r[0]] = r[1]
	}
	return out
}

// The card shows the agent CLI's version with the report that carried it;
// the one-line panel summary never does.
func TestParticipantCardShowsTheReportedVersion(t *testing.T) {
	now := time.Now()
	m := versionModel(now)
	rows := rowsOf(m, "mastra", now)
	if rows["Version"] != "0.166.0 (read from the agent's own server for the current launch)" {
		t.Fatalf("version row: %q (%v)", rows["Version"], rows)
	}
	if got := m.runtimeSummary(m.identify("", "mastra"), now); got != "gpt-6-astra (high)" || strings.Contains(got, "0.166.0") {
		t.Fatalf("panel summary: %q", got)
	}
	// Stale: the card keeps its rows with the Evidence row's stale label, as
	// for model and effort; the panel hides the summary.
	later := now.Add(3 * time.Minute)
	stale := rowsOf(m, "mastra", later)
	if stale["Version"] == "" || !strings.HasPrefix(stale["Evidence"], "stale: ") {
		t.Fatalf("stale card: %v", stale)
	}
	if got := m.runtimeSummary(m.identify("", "mastra"), later); got != "" {
		t.Fatalf("a stale panel summary shows nothing: %q", got)
	}
}

// D0's agy card has nothing observed: the hint still shows, whole, as its
// only row; an empty card without a hint stays empty, as before.
func TestAnEmptyCardShowsOnlyTheReporterHint(t *testing.T) {
	now := time.Now()
	m := versionModel(now)
	rows := m.runtimeRows(m.identify("", "magy"), now)
	if len(rows) != 1 || rows[0] != [2]string{"Note", agyHint} {
		t.Fatalf("agy card rows: %q", rows)
	}
	if got := m.runtimeSummary(m.identify("", "magy"), now); got != "" {
		t.Fatalf("agy panel line stays empty: %q", got)
	}
	if rows := m.runtimeRows(m.identify("", "mqa"), now); rows != nil {
		t.Fatalf("an empty card without a hint keeps no rows: %q", rows)
	}
}
