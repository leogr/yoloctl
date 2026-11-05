package main

import (
	"fmt"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// Model, effort, context window and state for the chat view's Participants panel:
// only the agent's own reports for its current launch, as one finite
// box.inventory read exposes them. Nothing is inferred from an agent name, a
// provider default or the box's launch arguments.

// chatParticipantRuntime maps each trusted, non-revoked box member, by
// account, to its runtime evidence. A declaration counts only while its ID
// equals the member's immutable box ID, as for agent glyphs.
func chatParticipantRuntime(header chatstream.Header, view []controllerBoxView) map[string]chatui.AgentRuntime {
	byID := map[string]controllerBoxView{}
	for _, b := range view {
		if b.Config != nil && b.Config.ID != "" {
			byID[b.Config.ID] = b
		}
	}
	out := map[string]chatui.AgentRuntime{}
	for _, id := range header.Identities {
		b, ok := byID[id.BoxID]
		if id.Revoked || id.BoxID == "" || !ok || !b.Config.HasAgent() {
			continue
		}
		out[id.Account] = participantRuntime(b)
	}
	return out
}

// participantRuntime is a box's observed values; a missing one stays
// unknown, with a note saying why.
func participantRuntime(b controllerBoxView) chatui.AgentRuntime {
	o, note := observedRuntime(b)
	rt := chatui.AgentRuntime{Note: note}
	if o == nil {
		rt.Hint = reporterHint(b, note)
		return rt
	}
	value := func(v *agentsession.ObservedValueView) chatui.RuntimeValue {
		if v == nil {
			return chatui.RuntimeValue{}
		}
		return chatui.RuntimeValue{Value: v.Value, Source: v.Source}
	}
	rt.Model, rt.Effort, rt.Version = value(o.Model), value(o.Effort), value(o.Version)
	if c := o.Context; c != nil {
		rt.Context = &chatui.RuntimeContext{Size: c.Size, Used: -1, Percent: -1}
		if c.Used != nil {
			rt.Context.Used = *c.Used
		}
		if c.Percent != nil {
			rt.Context.Percent = *c.Percent
		}
	}
	if s := o.State; s != nil {
		rt.State = chatui.RuntimeState{Kind: s.Kind, Source: s.Source, At: s.At}
	}
	if rt.Model.Value == "" || rt.Effort.Value == "" {
		rt.Note = "the agent has not reported every value in this launch"
	}
	rt.Conversation = participantConversation(b.State.Conversation)
	return rt
}

// participantConversation is the details card's conversation of the running
// verified launch, such as "0f8fad5b… · resumed": fresh for a startup of the
// assigned ID in this launch, resumed for a resume the agent reported,
// learned for an ID the agent named in this learn launch, and switched when
// this launch's agent last reported another conversation than the assigned
// one. Anything less certain has no row. Its caller has established, through
// observedRuntime, that st describes the running launch.
func participantConversation(st *agentsession.Status) string {
	l, cur := st.Launch, st.Current
	if st.ID == "" || cur == nil || cur.Generation != l.Generation {
		return ""
	}
	label := ""
	switch {
	case st.Switched():
		label = "switched"
	case l.Kind == agentsession.KindLearn:
		label = "learned"
	case l.Kind == agentsession.KindResume && resumeOutcome(st) == agentsession.OutcomeResumed:
		label = "resumed"
	case l.Kind == agentsession.KindAssign && cur.Source == "startup":
		label = "fresh"
	default:
		return ""
	}
	return cur.ID[:8] + "… · " + label
}

// resumeOutcome is the current launch's resume outcome, including one that
// only this view's read decided.
func resumeOutcome(st *agentsession.Status) string {
	r := st.Resume
	if r == nil || r.Generation != st.Launch.Generation {
		return ""
	}
	if p := st.Observed.PendingOutcome; p != "" {
		return p
	}
	return r.Outcome
}

// observedRuntime returns what the agent reported for the launch that is
// running now, or nil with the reason. A stored earlier launch, a launch this
// read could not reach, or reports of doubtful health are not current
// evidence.
func observedRuntime(b controllerBoxView) (*agentsession.ObservedView, string) {
	st := b.State.Conversation
	switch {
	case st == nil || st.Observed == nil:
		return nil, "no agent reports for this box"
	case b.State.Runtime != boxstate.RuntimeRunning || b.State.Session.State != boxstate.SessionRunning:
		return nil, "the agent session is not running"
	case st.Note != "":
		// Set with an observation only when the running holder is not the
		// recorded launch (agentsession.Untracked).
		return nil, "the running session is not the recorded launch"
	case st.Launch == nil || st.Launch.State != agentsession.StateLaunched || st.Observed.Generation != st.Launch.Generation:
		return nil, "the agent reports are from an earlier launch"
	case st.Observed.Live != "current":
		return nil, "the current reports could not be read"
	case st.Observed.Health != string(observe.HealthOK):
		return nil, "agent reports are " + st.Observed.Health
	}
	return st.Observed, ""
}

// reporterHint is the next step for a current launch whose reports were all
// readable but empty (health no-report), when the agent's reporter is the
// status line of its account's own settings (agy): it may be off for that
// account. Turning it on is the explicit `account observe` command, which
// refuses while any running box uses the account; nothing is changed here.
// Every other case keeps its plain note.
func reporterHint(b controllerBoxView, note string) string {
	if note != "agent reports are "+string(observe.HealthNoReport) || b.Config == nil {
		return ""
	}
	profile, err := agentprofile.Lookup(b.Config.Agent)
	if err != nil || profile.Session == nil || profile.Session.Observe == nil || profile.Session.Observe.AccountStatusLine == nil {
		return ""
	}
	account, space, agent := b.Config.Account, b.Config.Space, b.Config.Agent
	return fmt.Sprintf("no usable report has been observed for this launch; the %s status line reporter may be off for account %s. "+
		"To turn it on: stop every box that uses account %s, run yoloctl account observe %s %s --enable --space %s, then start the boxes again.",
		agent, account, account, account, agent, space)
}
