package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

// The devcontainer merge skips a forward whose published listener an
// explicit --port already claims — previously the duplicate tuple failed
// Config.Validate at Save.
func TestBuildAgentboxConfig_DevcontainerPortDedupe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wd, ".devcontainer", "devcontainer.json"),
		[]byte(`{"forwardPorts": [3000, 9090]}`), 0o644); err != nil {
		t.Fatalf("write devcontainer.json: %v", err)
	}
	workdir, _, err := agentbox.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	opts := agentboxCreateOptions{
		Space:   "default",
		Name:    "coder",
		Image:   "img:1",
		Ports:   []agentbox.Port{{Published: 3000, Target: 3000}},
		workdir: workdir,
	}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	want := []agentbox.Port{{Published: 3000, Target: 3000}, {Published: 9090, Target: 9090}}
	if !slices.Equal(cfg.Ports, want) {
		t.Errorf("ports = %v, want deduped %v", cfg.Ports, want)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("deduped config must validate: %v", err)
	}
}
