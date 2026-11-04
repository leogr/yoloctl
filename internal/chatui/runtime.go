package chatui

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// AgentRuntime is what the box agent reported of its model, effort and
// context window for its current launch. Every value is the agent's own
// report; an unknown one is empty.
type AgentRuntime struct {
	Model, Effort RuntimeValue
	// Context is nil when unknown.
	Context *RuntimeContext
	// Note explains missing or unusable observation, for the details card.
	Note string
	// Conversation is the running launch's conversation, such as
	// "0f8fad5b… · resumed"; empty when it is not certain.
	Conversation string
	// State is the agent's reported state; an empty Kind is unknown.
	State RuntimeState
}

// RuntimeState is the agent's reported state (StateWorking … StateFailed),
// the report that carried it and the agent's time of that report.
type RuntimeState struct {
	Kind, Source string
	At           time.Time
}

// Agent states, as the observation record names them.
const (
	StateWorking           = "working"
	StateIdle              = "idle"
	StateWaitingPermission = "waiting_permission"
	StateWaitingInput      = "waiting_input"
	StateWaitingDialog     = "waiting_dialog"
	StateFailed            = "failed"
)

// SourceActivity marks a state taken from the box's activity because the
// agent reported none: working or idle only, never a wait or a failure.
const SourceActivity = "activity"

// RuntimeValue is one model or effort value the agent reported and the
// report that carried it: SourceStatusLine, SourceHook, or "" when the
// record predates sources. An empty Value is unknown.
type RuntimeValue struct {
	Value  string
	Source string
}

// Report sources, as the observation record names them.
const (
	SourceHook       = "hook"
	SourceStatusLine = "statusline"
	SourceQuery      = "query"
)

// RuntimeContext is the reported context window: its size and used
// percentage, and the used tokens (Used < 0 when not reported). An agent
// that reports only the tokens used leaves Size 0 and Percent -1.
type RuntimeContext struct {
	Size, Used int64
	Percent    int
}

// runtimeFreshness is how long a successful host read stays current: two of
// the host's one-minute refreshes. A failed read makes it stale at once.
const runtimeFreshness = 2 * time.Minute

// maxRuntimeText bounds a displayed model or effort value.
const maxRuntimeText = 64

// modelNames are the friendly names of known model IDs. Any other ID is
// shown as reported, sanitized and bounded; nothing is inferred from it.
var modelNames = map[string]string{
	"claude-opus-5-5":   "Opus 5.5",
	"claude-sonnet-5-5": "Sonnet 5.5",
	"claude-haiku-5-5":  "Haiku 5.5",
	"claude-fable-5-1":  "Fable 5.1",
}

func modelName(id string) string {
	if name, ok := modelNames[id]; ok {
		return name
	}
	return runtimeText(id)
}

// runtimeText makes a reported value safe for one cell row: one line,
// printable, bounded. This fixed metadata bound is independent of terminal
// negotiation; the resulting text is laid out with the model's width method.
func runtimeText(v string) string {
	return ansi.TruncateWc(oneLine(v), maxRuntimeText, "…")
}

// agents applies a host read of the members' agents and their runtime.
// A failed read keeps the last values, which then show as stale.
func (m *Model) agents(msg AgentsMsg) {
	if msg.Err != nil {
		m.runtimeErr = oneLine(msg.Err.Error())
		return
	}
	identities := make(map[string]Identity, len(m.opts.Identities))
	for account, id := range m.opts.Identities {
		if agent, ok := msg.Agents[account]; ok && !id.Human {
			id.Agent = agent
		}
		identities[account] = id
	}
	m.opts.Identities = identities
	m.eachView(m.touch)
	if msg.Runtime == nil {
		return
	}
	// A successful read replaces every value: removed members disappear.
	m.runtime, m.runtimeAt, m.runtimeErr = maps.Clone(msg.Runtime), msg.At, ""
	if m.runtimeAt.IsZero() {
		m.runtimeAt = time.Now()
	}
}

// runtimeStale reports whether the last read is no longer current evidence.
func (m *Model) runtimeStale(now time.Time) bool {
	return m.runtimeErr != "" || now.Sub(m.runtimeAt) > runtimeFreshness
}

// runtimeOf returns a trusted box member's runtime evidence, if any.
func (m *Model) runtimeOf(w who) (AgentRuntime, bool) {
	if !w.trusted || w.human || !w.box || w.account == "" {
		return AgentRuntime{}, false
	}
	rt, ok := m.runtime[w.account]
	return rt, ok
}

// runtimeSummary is a participant's observed model and effort, such as
// "Opus 5.5 (max)". It is "" whenever that is not certain: without an
// observed model, or once the last read failed or is no longer current.
// An effort alone or an unknown value is never shown, and nothing marks
// what is left out; the details card shows the evidence, including the
// context window, which this line leaves out.
func (m *Model) runtimeSummary(w who, now time.Time) string {
	rt, ok := m.runtimeOf(w)
	if !ok || rt.Model.Value == "" || m.runtimeStale(now) {
		return ""
	}
	s := modelName(rt.Model.Value)
	if rt.Effort.Value != "" {
		s += " (" + runtimeText(rt.Effort.Value) + ")"
	}
	return s
}

// reportedBy names a value's report for the details card.
func reportedBy(source string) string {
	switch source {
	case SourceStatusLine:
		return "reported by the agent's status line for the current launch"
	case SourceHook:
		return "reported by an agent hook for the current launch"
	case SourceQuery:
		return "read from the agent's own server for the current launch"
	case SourceActivity:
		return "from the box's activity; the agent reported no state"
	}
	return "agent-reported for the current launch"
}

// tokens is a token count in short form: 812, 84k, 1M, 1.5M.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_500:
		return strconv.FormatInt((n+500)/1000, 10) + "k"
	}
	return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
}

// text is the context row's value, such as "84k of 200k, 42%", or
// "42% of 200k" without reported used tokens.
func (c RuntimeContext) text() string {
	if c.Size == 0 {
		return tokens(c.Used) + " used"
	}
	if c.Used < 0 {
		return fmt.Sprintf("%d%% of %s", c.Percent, tokens(c.Size))
	}
	return fmt.Sprintf("%s of %s, %d%%", tokens(c.Used), tokens(c.Size), c.Percent)
}

// agentBox reports a box whose agent is known and real (not "none"): only
// those have a model to show.
func agentBox(agent string) bool { return agent != "" && agent != "none" }

// runtimeRows are the details card's model, effort, context and conversation
// lines: each observed raw value with the report that carried it, then how
// current the read is and why a value is missing. Unknown values have no row,
// context only has one beside an observed model, and with none observed there
// are no rows at all.
func (m *Model) runtimeRows(w who, now time.Time) [][2]string {
	if !agentBox(w.agent) {
		return nil
	}
	rt, _ := m.runtimeOf(w)
	var rows [][2]string
	for _, f := range []struct {
		name string
		v    RuntimeValue
	}{{"Model", rt.Model}, {"Effort", rt.Effort}} {
		if f.v.Value == "" {
			continue
		}
		value := runtimeText(f.v.Value) + " (" + reportedBy(f.v.Source) + ")"
		if name, ok := modelNames[f.v.Value]; ok && f.name == "Model" {
			value = name + " · " + value
		}
		rows = append(rows, [2]string{f.name, value})
	}
	if rt.Context != nil && rt.Model.Value != "" {
		rows = append(rows, [2]string{"Context", rt.Context.text()})
	}
	if s, ok := m.stateOf(w, now); ok {
		rows = append(rows, [2]string{"State", stateText(s) + " (" + reportedBy(s.Source) + ")"})
	}
	if rt.Conversation != "" {
		rows = append(rows, [2]string{"Conversation", runtimeText(rt.Conversation)})
	}
	if len(rows) == 0 {
		return nil
	}
	switch {
	case m.runtimeErr != "":
		rows = append(rows, [2]string{"Evidence", "stale: the last refresh failed: " + m.runtimeErr})
	case m.runtimeStale(now):
		rows = append(rows, [2]string{"Evidence", "stale: last read " + m.runtimeAt.Local().Format("15:04")})
	default:
		rows = append(rows, [2]string{"Evidence", "read " + m.runtimeAt.Local().Format("15:04:05")})
	}
	if rt.Note != "" {
		// Keep the metadata safety bound stable across terminal reports.
		rows = append(rows, [2]string{"Note", ansi.TruncateWc(oneLine(rt.Note), 4*maxRuntimeText, "…")})
	}
	return rows
}

// stateOf is a participant's state: the agent's own report while the last
// read is current, else, for a box that reported none, its activity
// (working or idle; unknown activity is no state). A failed or stale read
// shows no reported state, and activity never stands in for a wait.
func (m *Model) stateOf(w who, now time.Time) (RuntimeState, bool) {
	rt, ok := m.runtimeOf(w)
	if !ok || !agentBox(w.agent) {
		return RuntimeState{}, false
	}
	if rt.State.Kind != "" {
		if m.runtimeStale(now) {
			return RuntimeState{}, false
		}
		return rt.State, true
	}
	switch m.boxActivity(w.account) {
	case spacetelemetry.ActivityActive:
		return RuntimeState{Kind: StateWorking, Source: SourceActivity}, true
	case spacetelemetry.ActivityIdle:
		return RuntimeState{Kind: StateIdle, Source: SourceActivity}, true
	}
	return RuntimeState{}, false
}

// stateText is the State row's value, such as "waiting for permission
// since 14:02".
func stateText(s RuntimeState) string {
	since := ""
	if !s.At.IsZero() {
		since = " since " + s.At.Local().Format("15:04")
	}
	switch s.Kind {
	case StateWaitingPermission:
		return "waiting for permission" + since
	case StateWaitingInput:
		return "waiting for input" + since
	case StateWaitingDialog:
		return "waiting for a dialog" + since
	case StateFailed:
		if !s.At.IsZero() {
			return "API error at " + s.At.Local().Format("15:04")
		}
		return "API error"
	}
	return runtimeText(s.Kind)
}

// stateTag is the Participants panel's tag beside a box's name: only for a
// reported wait or failure; working and idle get no words.
func (m *Model) stateTag(w who, now time.Time) (string, bool) {
	s, ok := m.stateOf(w, now)
	if !ok {
		return "", false
	}
	switch s.Kind {
	case StateWaitingPermission:
		return "⏳ needs permission", true
	case StateWaitingInput:
		return "⏳ needs input", true
	case StateWaitingDialog:
		return "⏳ dialog", true
	case StateFailed:
		return "⚠ API error", true
	}
	return "", false
}
