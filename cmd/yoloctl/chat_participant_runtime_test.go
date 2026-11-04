package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// currentObservation is a running box whose current launch reported values.
func currentObservation(cfg *agentbox.Config, model, effort string) controllerBoxView {
	value := func(v string) *agentsession.ObservedValueView {
		if v == "" {
			return nil
		}
		return &agentsession.ObservedValueView{Value: v, Source: agentsession.SourceStatusLine, At: time.Now().Add(-time.Hour), ReadAt: time.Now()}
	}
	used, percent := int64(84000), 42
	st := &agentsession.Status{Provider: cfg.Agent, Tracked: true,
		Launch: &agentsession.LaunchView{Generation: 3, Kind: "resume", State: agentsession.StateLaunched},
		Observed: &agentsession.ObservedView{Generation: 3, Health: "ok", Live: "current", Model: value(model), Effort: value(effort),
			Context: &agentsession.ContextView{Size: 200000, Used: &used, Percent: &percent, Source: agentsession.SourceStatusLine}}}
	return controllerBoxView{Config: cfg, State: boxstate.Box{Name: cfg.Name, Runtime: boxstate.RuntimeRunning,
		Session: boxstate.SessionInfo{State: boxstate.SessionRunning}, Conversation: st}}
}

// values is a box's displayed model, effort and note.
func values(b controllerBoxView) (model, effort, note string) {
	rt := participantRuntime(b)
	return rt.Model.Value, rt.Effort.Value, rt.Note
}

// Observations count only for the running, recorded, current launch with a
// usable read; a model event from an hour ago is still current evidence.
func TestObservedRuntimeIsCurrentLaunchOnly(t *testing.T) {
	cfg := &agentbox.Config{Name: "opus", ID: "box-opus", Agent: "claude"}
	model, effort, note := values(currentObservation(cfg, "claude-opus-5-5", "max"))
	if model != "claude-opus-5-5" || effort != "max" || note != "" {
		t.Fatalf("current launch: %q %q %q", model, effort, note)
	}
	if model, effort, note := values(currentObservation(cfg, "claude-opus-5-5", "")); model == "" || effort != "" || note == "" {
		t.Fatalf("model only: %q %q %q", model, effort, note)
	}
	for name, mutate := range map[string]func(*controllerBoxView){
		"no record":        func(b *controllerBoxView) { b.State.Conversation = nil },
		"box stopped":      func(b *controllerBoxView) { b.State.Runtime = boxstate.RuntimeStopped },
		"session ended":    func(b *controllerBoxView) { b.State.Session.State = boxstate.SessionExited },
		"untracked holder": func(b *controllerBoxView) { b.State.Conversation.Note = agentsession.Untracked },
		"earlier launch":   func(b *controllerBoxView) { b.State.Conversation.Observed.Generation = 2 },
		"launch closed":    func(b *controllerBoxView) { b.State.Conversation.Launch.State = "closed" },
		"read unavailable": func(b *controllerBoxView) {
			b.State.Conversation.Observed.Live = "unavailable: skipped: this command's live-read budget is spent"
		},
		"not read":    func(b *controllerBoxView) { b.State.Conversation.Observed.Live = "" },
		"reports gap": func(b *controllerBoxView) { b.State.Conversation.Observed.Health = "gap" },
	} {
		b := currentObservation(cfg, "claude-opus-5-5", "max")
		mutate(&b)
		if model, effort, note := values(b); model != "" || effort != "" || note == "" || participantRuntime(b).Context != nil {
			t.Errorf("%s: stale evidence shown as current: %q %q %q", name, model, effort, note)
		}
	}
}

// The card's conversation row is the running verified launch's conversation
// with how it came to be; anything less certain has no row.
func TestParticipantConversationIsCertainOnly(t *testing.T) {
	const assigned, other = "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	cfg := &agentbox.Config{Name: "opus", ID: "box-opus", Agent: "claude"}
	at := time.Now()
	box := func(kind, outcome, pending, current, source string, gen uint64) controllerBoxView {
		b := currentObservation(cfg, "claude-opus-5-5", "max")
		st := b.State.Conversation
		st.ID, st.Launch.Kind, st.Observed.PendingOutcome = assigned, kind, pending
		if outcome != "" {
			st.Resume = &agentsession.ResumeView{Requested: assigned, Generation: 3, Outcome: outcome}
		}
		if current != "" {
			st.Current = &agentsession.CurrentView{ID: current, Source: source, At: at, ReadAt: at, Generation: gen}
		}
		return b
	}
	for name, tc := range map[string]struct {
		b    controllerBoxView
		want string
	}{
		"fresh":              {box("assign", "", "", assigned, "startup", 3), "0b8f6e3c… · fresh"},
		"resumed":            {box("resume", "resumed", "", assigned, "resume", 3), "0b8f6e3c… · resumed"},
		"resumed, pending":   {box("resume", "unverified", "resumed", assigned, "resume", 3), "0b8f6e3c… · resumed"},
		"learned":            {box("learn", "", "", assigned, "startup", 3), "0b8f6e3c… · learned"},
		"switched":           {box("resume", "resumed", "", other, "clear", 3), "7c9e6679… · switched"},
		"switched at start":  {box("assign", "", "", other, "startup", 3), "7c9e6679… · switched"},
		"resume unverified":  {box("resume", "unverified", "", assigned, "resume", 3), ""},
		"resume mismatch":    {box("resume", "mismatch", "", assigned, "resume", 3), ""},
		"no report":          {box("assign", "", "", "", "", 0), ""},
		"earlier launch":     {box("resume", "resumed", "", assigned, "resume", 2), ""},
		"assign, unlabelled": {box("assign", "", "", assigned, "", 3), ""},
	} {
		if got := participantRuntime(tc.b).Conversation; got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	for name, mutate := range map[string]func(*controllerBoxView){
		"box stopped":      func(b *controllerBoxView) { b.State.Runtime = boxstate.RuntimeStopped },
		"untracked holder": func(b *controllerBoxView) { b.State.Conversation.Note = agentsession.Untracked },
		"launch closed":    func(b *controllerBoxView) { b.State.Conversation.Launch.State = "closed" },
		"not read":         func(b *controllerBoxView) { b.State.Conversation.Observed.Live = "" },
	} {
		b := box("resume", "resumed", "", assigned, "resume", 3)
		mutate(&b)
		if got := participantRuntime(b).Conversation; got != "" {
			t.Errorf("%s: a row without a running verified launch: %q", name, got)
		}
	}
}

func runtimeHeader() chatstream.Header {
	return chatstream.Header{Bootstrap: managedchat.Bootstrap{RoomID: "room", Generation: 4, Identities: []managedchat.BootstrapIdentity{
		{ID: "m1", Account: "mopus", Name: "opus", BoxID: "box-opus", Role: "member"},
		{ID: "m2", Account: "mastra", Name: "astra", BoxID: "box-astra", Role: "member"},
		{ID: "m3", Account: "mgone", Name: "gone", BoxID: "box-gone", Role: "member", Revoked: true},
		{ID: "m4", Account: "mreused", Name: "reused", BoxID: "box-old", Role: "member"},
		{ID: "m5", Account: "mshell", Name: "shell", BoxID: "box-shell", Role: "member"},
		{ID: "h", Account: "mhuman", Name: "lee", Role: "human"},
	}}}
}

// Only observed values are shown, each with its own source; explicit launch
// arguments never fill a gap. Revoked, replaced and agentless boxes have
// none.
func TestParticipantRuntimeKeysByImmutableBoxID(t *testing.T) {
	astra := &agentbox.Config{Name: "astra", ID: "box-astra", Agent: "codex", AgentExtraArgs: []string{"--model", "gpt-6-astra", "-c", `model_reasoning_effort="xhigh"`}}
	astraView := currentObservation(astra, "gpt-6.1-sol", "")
	view := []controllerBoxView{
		currentObservation(&agentbox.Config{Name: "opus", ID: "box-opus", Agent: "claude"}, "claude-opus-5-5", "max"),
		astraView,
		currentObservation(&agentbox.Config{Name: "gone", ID: "box-gone", Agent: "claude"}, "claude-opus-5-5", "max"),
		currentObservation(&agentbox.Config{Name: "reused", ID: "box-new", Agent: "claude"}, "claude-opus-5-5", "max"),
		{Config: &agentbox.Config{Name: "shell", ID: "box-shell", Agent: "none"}},
	}
	got := chatParticipantRuntime(runtimeHeader(), view)
	if len(got) != 2 {
		t.Fatalf("only current members with agents: %+v", got)
	}
	opus := got["mopus"]
	if opus.Model != (chatui.RuntimeValue{Value: "claude-opus-5-5", Source: chatui.SourceStatusLine}) || opus.Effort.Source != chatui.SourceStatusLine ||
		opus.Context == nil || *opus.Context != (chatui.RuntimeContext{Size: 200000, Used: 84000, Percent: 42}) {
		t.Fatalf("observed: %+v", opus)
	}
	partial := got["mastra"]
	if partial.Model != (chatui.RuntimeValue{Value: "gpt-6.1-sol", Source: chatui.SourceStatusLine}) ||
		partial.Effort != (chatui.RuntimeValue{}) || partial.Note == "" {
		t.Fatalf("an unreported effort must stay unknown: %+v", partial)
	}
	// Without observation, --model in agent_extra_args yields nothing.
	astraView.State.Conversation = nil
	none := chatParticipantRuntime(runtimeHeader(), []controllerBoxView{astraView})["mastra"]
	if none.Model != (chatui.RuntimeValue{}) || none.Effort != (chatui.RuntimeValue{}) || none.Context != nil || none.Note != "no agent reports for this box" {
		t.Fatalf("launch arguments shown as runtime: %+v", none)
	}
}

// One read at a time: a superseded read is discarded and asked again, a
// failure reports an error for staleness, and success carries its time.
func TestLoadAgentsIsBoundedAndDiscardsSupersededReads(t *testing.T) {
	header := runtimeHeader()
	s := &chatSession{ctx: t.Context(), header: header, agentsWake: make(chan struct{}, 1)}
	s.refreshAgents()
	s.refreshAgents()
	if len(s.agentsWake) != 1 {
		t.Fatal("refresh requests must coalesce")
	}
	<-s.agentsWake
	view := []controllerBoxView{currentObservation(&agentbox.Config{Name: "opus", ID: "box-opus", Agent: "claude"}, "claude-opus-5-5", "max")}
	before := time.Now()
	msg, ok := s.loadAgents(func(context.Context) ([]controllerBoxView, error) { return view, nil }).(chatui.AgentsMsg)
	if !ok || msg.Err != nil || msg.Agents["mopus"] != "claude" || msg.Runtime["mopus"].Model.Value != "claude-opus-5-5" || msg.At.Before(before) {
		t.Fatalf("read: %+v", msg)
	}
	// The catalog moves on during the read: discard, and read again.
	if msg := s.loadAgents(func(context.Context) ([]controllerBoxView, error) {
		s.mu.Lock()
		s.header.Generation++
		s.mu.Unlock()
		return view, nil
	}); msg != nil || len(s.agentsWake) != 1 {
		t.Fatalf("a superseded read was published: %+v", msg)
	}
	<-s.agentsWake
	if msg, ok := s.loadAgents(func(context.Context) ([]controllerBoxView, error) {
		return nil, errors.New("controller busy")
	}).(chatui.AgentsMsg); !ok || msg.Err == nil || msg.Runtime != nil {
		t.Fatalf("a failed read must report an error: %+v", msg)
	}
	// A read answering at its deadline is a failure, not current evidence.
	old := agentsReadTimeout
	agentsReadTimeout = 20 * time.Millisecond
	t.Cleanup(func() { agentsReadTimeout = old })
	if msg, ok := s.loadAgents(func(ctx context.Context) ([]controllerBoxView, error) {
		<-ctx.Done()
		return view, nil
	}).(chatui.AgentsMsg); !ok || msg.Err == nil {
		t.Fatalf("a late read was published: %+v", msg)
	}
}

// A click's lookup is bounded; a deadline is distinct from a missing box, and
// an answer for a membership that changed meanwhile is refused.
func TestResolveAttachIsBoundedAndRechecksMembership(t *testing.T) {
	old := attachLookupTimeout
	attachLookupTimeout = 20 * time.Millisecond
	t.Cleanup(func() { attachLookupTimeout = old })
	s := &chatSession{ctx: t.Context(), members: map[string]string{"mopus": "m1"}, header: runtimeHeader()}
	target := managedchat.AttachTarget{RoomID: "room", MemberID: "m1", Box: "opus", BoxID: "box-opus", State: "running"}
	answer := func(t managedchat.AttachTarget, err error) func(context.Context, string) (managedchat.AttachTarget, error) {
		return func(context.Context, string) (managedchat.AttachTarget, error) { return t, err }
	}
	msg := s.resolveAttachWith("mopus", answer(target, nil)).(chatui.AttachTargetMsg)
	if msg.Err != nil || msg.Target.State != "running" || msg.Target.Account != "mopus" {
		t.Fatalf("lookup: %+v", msg)
	}
	msg = s.resolveAttachWith("mopus", func(ctx context.Context, _ string) (managedchat.AttachTarget, error) {
		<-ctx.Done()
		return target, nil // an answer at the deadline is still too late
	}).(chatui.AttachTargetMsg)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "did not answer within") {
		t.Fatalf("deadline: %+v", msg)
	}
	msg = s.resolveAttachWith("mopus", answer(managedchat.AttachTarget{}, errors.New("participant lookup deadline reached; the controller is busy, try again"))).(chatui.AttachTargetMsg)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "controller is busy") {
		t.Fatalf("controller deadline: %+v", msg)
	}
	for name, mutate := range map[string]func(){
		"member replaced": func() { s.members["mopus"] = "m9" },
		"member revoked":  func() { delete(s.members, "mopus") },
		"room replaced":   func() { s.header.RoomID = "other" },
	} {
		s.members, s.header = map[string]string{"mopus": "m1"}, runtimeHeader()
		msg := s.resolveAttachWith("mopus", func(context.Context, string) (managedchat.AttachTarget, error) {
			s.mu.Lock()
			mutate()
			s.mu.Unlock()
			return target, nil
		}).(chatui.AttachTargetMsg)
		if msg.Err == nil {
			t.Fatalf("%s: answer used across a membership change", name)
		}
	}
	s.members, s.header = map[string]string{"mopus": "m1"}, runtimeHeader()
	wrong := target
	wrong.MemberID = "m2"
	if msg := s.resolveAttachWith("mopus", answer(wrong, nil)).(chatui.AttachTargetMsg); msg.Err == nil {
		t.Fatal("an answer for another member was accepted")
	}
}
