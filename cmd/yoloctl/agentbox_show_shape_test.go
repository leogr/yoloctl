package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

// TestBoxShowJSONAccountShape freezes the current `box show --json` account
// surface: the plain account NAME plus the per-agent state-leaf bind, with no
// retired scope axis.
func TestBoxShowJSONAccountShape(t *testing.T) {
	d := &container.Details{
		Name:    "coder",
		Image:   "img:1",
		Status:  "Up 2 minutes",
		Running: true,
		Labels: map[string]string{
			agentbox.LabelAgentbox: "coder",
			agentbox.LabelSpace:    "default",
			agentbox.LabelAgent:    "claude",
			agentbox.LabelAccount:  "personal",
		},
	}
	state := agentStateMount{
		Source:      "/cfg/yoloctl/account-state/personal/claude",
		Destination: "/home/sandbox/.claude",
		Enabled:     true,
	}
	var buf bytes.Buffer
	if err := writeAgentboxShowJSON(&buf, d, "default", landlockState{}, state,
		agentboxShowMetadata{AgentName: "claude"}, sessionStatus{State: "running"}, nil); err != nil {
		t.Fatalf("writeAgentboxShowJSON: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("show JSON not parseable: %v\n%s", err, buf.String())
	}
	if string(doc["account"]) != `"personal"` {
		t.Errorf("account = %s, want the plain name \"personal\"", doc["account"])
	}
	var as struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Enabled     bool   `json:"enabled"`
	}
	if err := json.Unmarshal(doc["account_state"], &as); err != nil {
		t.Fatalf("account_state shape: %v", err)
	}
	if as.Source != state.Source || as.Destination != state.Destination || !as.Enabled {
		t.Errorf("account_state = %+v, want the per-agent leaf bind", as)
	}
	if _, ok := doc["scope"]; ok {
		t.Errorf("show JSON leaks the retired scope field:\n%s", buf.String())
	}
}
