package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentprofile"
)

func TestNpmAgentMaintenanceUsesExplicitConfigPaths(t *testing.T) {
	id := strings.Repeat("f", 64)
	for _, tc := range []struct {
		agent string
		pkg   string
	}{
		{agent: "claude", pkg: "@anthropic-ai/claude-code"},
		{agent: "codex", pkg: "@openai/codex"},
		{agent: "gemini", pkg: "@google/gemini-cli"},
		{agent: "opencode", pkg: "opencode-ai"},
		{agent: "pi", pkg: "@earendil-works/pi-coding-agent"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			profile, err := agentprofile.Lookup(tc.agent)
			if err != nil {
				t.Fatal(err)
			}
			c, calls := maintenanceTargetFixture(t, "[]")
			if err := installAgentLatest(t.Context(), c, id, profile); err != nil {
				t.Fatal(err)
			}
			want := []string{
				"sudo", "nerdctl", "exec",
				"--user", "sandbox",
				"--env", wantAgentMaintenancePath,
				id,
				"npm", "install", "-g",
				"--userconfig=/dev/null",
				"--globalconfig=/dev/yoloctl-maintenance.npmrc",
				"--prefix=/home/sandbox/.npm-global",
				"--cache=/home/sandbox/.npm-global/.cache/npm",
				"--prefer-online",
				tc.pkg + "@latest",
			}
			if got := calls(); len(got) != 1 || !slices.Equal(got[0], want) {
				t.Fatalf("npm install calls = %q, want one call %q", got, want)
			}
		})
	}
}
