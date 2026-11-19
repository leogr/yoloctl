package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/features"
)

// A standalone image build has no selected agent: the internal managed-npm
// marker is not a feature there, and the Codex feature keeps the user's
// prefix (archyAstra r1-3).
func TestImageBuildRejectsTheAgentNpmMarker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run := func(features string) (string, error) {
		cmd := findByPath(newRootCmd(), "image", "build")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(t.Context())
		if err := cmd.ParseFlags([]string{"--tag", "local/x:1", "--features", features, "--dry-run", "--space", "default"}); err != nil {
			t.Fatal(err)
		}
		err := cmd.RunE(cmd, nil)
		return out.String(), err
	}
	if _, err := run("codex," + features.AgentNpmRuntime("@openai/codex")); err == nil || !strings.Contains(err.Error(), "unknown feature") {
		t.Fatalf("image build accepted the internal marker: %v", err)
	}
	out, err := run("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "npm install -g @openai/codex@latest --prefix=/home/sandbox/.npm-global") || strings.Contains(out, "/opt/yoloctl/npm") {
		t.Fatalf("standalone Codex build left the user's prefix:\n%s", out)
	}
}
