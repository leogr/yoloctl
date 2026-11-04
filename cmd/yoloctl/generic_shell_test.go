package main

import (
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
)

// shellOpts builds an in-process create request for a GENERIC SHELL box: no
// --agent, no features, no explicit capabilities — the case that must be valid
// with no fake feature. The agent value is parameterized to cover both an
// omitted --agent ("") and an explicit --agent none.
func shellOpts(name, agent string) agentboxCreateOptions {
	return agentboxCreateOptions{
		Space:   "default",
		Name:    name,
		Agent:   agent,
		Image:   "img:1",
		workdir: agentbox.NewEphemeralWorkdir(name),
	}
}

// A generic-shell box (omitted --agent or --agent none) with NO features and NO
// explicit capabilities must build successfully: it is a valid, maximally
// locked-down sandbox, not a misconfiguration. It must NOT require a fake
// feature, and it gets the default writable /workdir.
func TestBuildAgentboxConfig_GenericShellNoPolicy(t *testing.T) {
	for _, agent := range []string{"", "none"} {
		t.Run("agent="+agent, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			cfg, _, err := buildAgentboxConfig(t.Context(), shellOpts("shell", agent))
			if err != nil {
				t.Fatalf("generic shell (agent=%q, no features/capabilities) must build, got error: %v", agent, err)
			}
			if cfg.HasAgent() {
				t.Errorf("agent=%q must yield a generic shell (no agent), got Agent=%q", agent, cfg.Agent)
			}
			if len(cfg.Features) != 0 {
				t.Errorf("a shell box must require no fake feature, got features %v", cfg.Features)
			}
			if !cfg.WorkdirWritable() {
				t.Errorf("a generic shell box defaults to a writable workdir (fs.workdir.write)")
			}
			// The zero-feature box must COMPOSE a valid locked-down policy — the
			// case the removed "no policy specified" guard rejected.
			if _, err := agentbox.ComposePolicy(cfg); err != nil {
				t.Errorf("generic shell must compose a locked-down policy, got: %v", err)
			}
		})
	}
}

// A generic-shell box with --read-only and NO features/capabilities must also
// build: it has the EMPTY capability set (no fs.workdir.write), which composes
// to a read-only, default-deny policy. This is the case both removed guards
// ("no policy specified" / "no policy declared") rejected.
func TestBuildAgentboxConfig_GenericShellReadOnly(t *testing.T) {
	for _, agent := range []string{"", "none"} {
		t.Run("agent="+agent, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			opts := shellOpts("shell-ro", agent)
			opts.ReadOnly = true
			cfg, _, err := buildAgentboxConfig(t.Context(), opts)
			if err != nil {
				t.Fatalf("read-only generic shell (agent=%q, no features) must build, got error: %v", agent, err)
			}
			if cfg.WorkdirWritable() {
				t.Errorf("--read-only shell must NOT have fs.workdir.write")
			}
			if slices.Contains(cfg.Capabilities, capabilities.WorkdirWriteCapability) {
				t.Errorf("read-only shell still carries %s: %v", capabilities.WorkdirWriteCapability, cfg.Capabilities)
			}
			if len(cfg.Capabilities) != 0 {
				t.Errorf("read-only shell should have an EMPTY capability set, got %v", cfg.Capabilities)
			}
			// The empty capability set must still compose a valid locked-down policy.
			if _, err := agentbox.ComposePolicy(cfg); err != nil {
				t.Errorf("read-only shell (empty capability set) must compose a locked-down policy, got: %v", err)
			}
		})
	}
}
