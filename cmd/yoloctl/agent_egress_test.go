package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/layout"
)

// composeAgentPolicy mirrors how box creation derives a box's policy: prefill
// features from the resolved profile (profileFeatures), set Agent, then
// ComposePolicy. Returns the composed policy YAML.
func composeAgentPolicy(t *testing.T, agent string) string {
	t.Helper()
	profile, err := lookupAgentProfile(agent)
	if err != nil {
		t.Fatalf("lookup %s: %v", agent, err)
	}
	cfg := &agentbox.Config{
		Name:         agent,
		Space:        "default",
		Agent:        agent,
		Features:     profileFeatures(profile),
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Workdir:      agentbox.NewEphemeralWorkdir(agent),
	}
	raw, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		t.Fatalf("compose %s policy: %v", agent, err)
	}
	return string(raw)
}

// TestAgentDefaultEgress_Fresh: an embedded built-in agent grants its own API
// egress by default (the regression we want to keep fixed).
func TestAgentDefaultEgress_Fresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if got := composeAgentPolicy(t, "claude"); !strings.Contains(got, "api.anthropic.com") {
		t.Errorf("fresh claude box is missing anthropic egress:\n%s", got)
	}
	if got := composeAgentPolicy(t, "codex"); !strings.Contains(got, "api.openai.com") {
		t.Errorf("fresh codex box is missing openai egress:\n%s", got)
	}
	if got := composeAgentPolicy(t, "gemini"); !strings.Contains(got, "generativelanguage.googleapis.com") {
		t.Errorf("fresh gemini box is missing Gemini egress:\n%s", got)
	}
}

// TestAgentEgress_FlowsOnlyThroughFeatures: egress is derived STRICTLY from the
// profile's required features (agent → features → capabilities), with no magic
// repair. A custom profile that lists no requires_features therefore grants no
// agent egress — the box is locked down by construction, exactly as declared.
// This is the inverse guard to TestAgentDefaultEgress_Fresh: it proves there is
// no hidden agent-level capability path.
func TestAgentEgress_FlowsOnlyThroughFeatures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	pdir, err := layout.AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A minimal user profile with no requires_features. It parses fine, but
	// since egress flows only through features, the composed box has none.
	minimal := "name: minimal-claude\nimage: yoloctl/agentbox:ubuntu-24.04\ncommand: claude\n"
	if err := os.WriteFile(filepath.Join(pdir, "minimal-claude.yaml"), []byte(minimal), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := composeAgentPolicy(t, "minimal-claude"); strings.Contains(got, "api.anthropic.com") {
		t.Errorf("a profile with no requires_features must not grant agent egress (no hidden agent-level capability):\n%s", got)
	}
}
