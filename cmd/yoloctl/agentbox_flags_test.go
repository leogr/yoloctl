package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
)

// flagsTestOpts builds a deterministic, in-process create request: a generic
// (agent-less) ephemeral box whose policy source is an explicit feature, so
// buildAgentboxConfig needs no account / agent-profile / VM I/O. Callers mutate
// the capability-drop / read-only / env fields under test.
func flagsTestOpts(name string) agentboxCreateOptions {
	return agentboxCreateOptions{
		Space:    "default",
		Name:     name,
		Image:    "img:1",
		Features: []string{"github"},
		CPUs:     "2",
		Memory:   "2g",
		workdir:  agentbox.NewEphemeralWorkdir(name),
	}
}

// --drop-capability fs.workdir.write removes the default workdir-write grant, so
// the resolved box is read-only (WorkdirWritable() == false). fs.workdir.write
// is never feature-required, so the drop is allowed.
func TestBuildAgentboxConfig_DropWorkdirWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("coder")
	opts.DropCapabilities = []string{capabilities.WorkdirWriteCapability}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if cfg.WorkdirWritable() {
		t.Errorf("WorkdirWritable() = true after dropping %s, want false", capabilities.WorkdirWriteCapability)
	}
	if slices.Contains(cfg.Capabilities, capabilities.WorkdirWriteCapability) {
		t.Errorf("capabilities still contain %s after drop: %v", capabilities.WorkdirWriteCapability, cfg.Capabilities)
	}
}

// --read-only is sugar for --drop-capability fs.workdir.write: it produces the
// exact same capability set as the explicit drop (read-only workdir).
func TestBuildAgentboxConfig_ReadOnlyEqualsDropWorkdirWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ro := flagsTestOpts("ro")
	ro.ReadOnly = true
	roCfg, _, err := buildAgentboxConfig(t.Context(), ro)
	if err != nil {
		t.Fatalf("buildAgentboxConfig(read-only): %v", err)
	}

	drop := flagsTestOpts("drop")
	drop.DropCapabilities = []string{capabilities.WorkdirWriteCapability}
	dropCfg, _, err := buildAgentboxConfig(t.Context(), drop)
	if err != nil {
		t.Fatalf("buildAgentboxConfig(drop): %v", err)
	}

	if roCfg.WorkdirWritable() {
		t.Errorf("--read-only left the workdir writable")
	}
	rc := append([]string(nil), roCfg.Capabilities...)
	dc := append([]string(nil), dropCfg.Capabilities...)
	slices.Sort(rc)
	slices.Sort(dc)
	if !slices.Equal(rc, dc) {
		t.Errorf("--read-only caps %v != --drop-capability fs.workdir.write caps %v", rc, dc)
	}
}

// Passing BOTH --read-only and --drop-capability fs.workdir.write is idempotent
// (the same name is dropped twice) — it must not error and yields a read-only
// workdir.
func TestBuildAgentboxConfig_ReadOnlyAndExplicitDropIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("coder")
	opts.ReadOnly = true
	opts.DropCapabilities = []string{capabilities.WorkdirWriteCapability}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if cfg.WorkdirWritable() {
		t.Errorf("WorkdirWritable() = true, want false")
	}
}

// --unconfined-network sets Config.UnconfinedNetwork and nothing else; the
// default leaves it false (confined). This is the only field the flag touches —
// capabilities/workdir are unaffected.
func TestBuildAgentboxConfig_UnconfinedNetwork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	on := flagsTestOpts("unconf")
	on.UnconfinedNetwork = true
	onCfg, _, err := buildAgentboxConfig(t.Context(), on)
	if err != nil {
		t.Fatalf("buildAgentboxConfig(unconfined): %v", err)
	}
	if !onCfg.UnconfinedNetwork {
		t.Error("--unconfined-network did not set Config.UnconfinedNetwork")
	}
	// The flag must not perturb the workdir-write default.
	if !onCfg.WorkdirWritable() {
		t.Error("--unconfined-network unexpectedly changed workdir writability")
	}

	def := flagsTestOpts("conf")
	defCfg, _, err := buildAgentboxConfig(t.Context(), def)
	if err != nil {
		t.Fatalf("buildAgentboxConfig(default): %v", err)
	}
	if defCfg.UnconfinedNetwork {
		t.Error("default Config.UnconfinedNetwork = true, want false (confined)")
	}
}

// --privileged sets only Config.Privileged; capabilities stay as declared. The
// retired capability is refused with guidance toward the flag.
func TestBuildAgentboxConfig_Privileged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	on := flagsTestOpts("dev")
	on.Privileged = true
	onCfg, _, err := buildAgentboxConfig(t.Context(), on)
	if err != nil {
		t.Fatalf("buildAgentboxConfig(privileged): %v", err)
	}
	defCfg, _, err := buildAgentboxConfig(t.Context(), flagsTestOpts("dev"))
	if err != nil {
		t.Fatalf("buildAgentboxConfig(default): %v", err)
	}
	if !onCfg.Privileged || defCfg.Privileged || !slices.Equal(onCfg.Capabilities, defCfg.Capabilities) {
		t.Fatalf("privileged=%v/%v capabilities %q vs %q", onCfg.Privileged, defCfg.Privileged, onCfg.Capabilities, defCfg.Capabilities)
	}
	retired := flagsTestOpts("dev")
	retired.Capabilities = []string{capabilities.RetiredPrivilegedCapability}
	if _, _, err := buildAgentboxConfig(t.Context(), retired); err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "--privileged") {
		t.Fatalf("retired capability = %v", err)
	}
}

func TestBuildAgentboxConfig_AgentExtraArgs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("coder")
	opts.Agent = "claude"
	opts.Account = "default"
	opts.AgentExtraArgs = []string{"--model", "opus"}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if !slices.Equal(cfg.AgentExtraArgs, opts.AgentExtraArgs) {
		t.Errorf("AgentExtraArgs = %v, want %v", cfg.AgentExtraArgs, opts.AgentExtraArgs)
	}
}

func TestBuildAgentboxConfig_AgentExtraArgsRequireAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("shell")
	opts.Agent = "none"
	opts.Account = "none"
	opts.AgentExtraArgs = []string{"--model", "opus"}
	_, _, err := buildAgentboxConfig(t.Context(), opts)
	if err == nil {
		t.Fatalf("expected generic-shell agent extra args to fail, got nil")
	}
	if !strings.Contains(err.Error(), "--agent-extra-args requires a real --agent") {
		t.Errorf("error = %v", err)
	}
}

// Dropping a capability a resolved FEATURE requires must FAIL with an
// explanation naming the feature — a drop can only remove from the
// explicit/default set, never deny a feature-required capability. The claude
// feature requires network.anthropic.
func TestBuildAgentboxConfig_DropFeatureRequiredFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("claude-box")
	opts.Features = []string{"claude"} // claude → network.anthropic
	opts.DropCapabilities = []string{"network.anthropic"}
	_, _, err := buildAgentboxConfig(t.Context(), opts)
	if err == nil {
		t.Fatalf("expected drop of a feature-required capability to fail, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "network.anthropic") || !strings.Contains(msg, "claude") {
		t.Errorf("error %q should name both the capability (network.anthropic) and the feature (claude)", msg)
	}
	if !strings.Contains(msg, "required by feature") {
		t.Errorf("error %q should explain the capability is required by a feature", msg)
	}
}

// --env literals round-trip into Config.Env: a plain KEY=VALUE and a value that
// itself contains '=' (split on the FIRST '='). Stored as a literal EnvVar.
func TestBuildAgentboxConfig_EnvLiterals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("coder")
	opts.Env = map[string]agentbox.EnvVar{
		"FOO": {Literal: "bar"},
		"X":   {Literal: "a=b"},
	}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if got := cfg.Env["FOO"]; got.Literal != "bar" {
		t.Errorf("Env[FOO] = %+v, want literal \"bar\"", got)
	}
	if got := cfg.Env["X"]; got.Literal != "a=b" {
		t.Errorf("Env[X] = %+v, want literal \"a=b\"", got)
	}
}

// The feature-required drop guard must also fire when the requiring feature is
// pulled in by the AGENT PROFILE (RequiresFeatures), not just the box's explicit
// --features: a claude AGENT box that drops network.anthropic fails even though
// it lists no explicit features — effectiveFeatureNames unions in the profile's
// required features (which include the claude feature → network.anthropic).
func TestBuildAgentboxConfig_DropAgentProfileRequiredFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("claude-box")
	opts.Agent = "claude"
	opts.Account = "default"
	opts.Features = nil // rely on the profile's required features only
	opts.DropCapabilities = []string{"network.anthropic"}
	_, _, err := buildAgentboxConfig(t.Context(), opts)
	if err == nil {
		t.Fatalf("expected drop of an agent-profile-required capability to fail, got nil")
	}
	if !strings.Contains(err.Error(), "network.anthropic") {
		t.Errorf("error %q should name the capability network.anthropic", err.Error())
	}
}

// A --capability / --features name no catalog defines fails the BUILD, which
// every create-shaped command runs before it persists anything. Create saves
// the manifest before its reconcile composes the policy, so the name would
// otherwise be left in a declaration that blocks the corrected re-run.
func TestBuildAgentboxConfig_RejectsUnresolvableNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agentboxCreateOptions)
		want   string
	}{
		{"capability", func(o *agentboxCreateOptions) { o.Capabilities = []string{"bogus.capability"} }, `unknown capability "bogus.capability"`},
		{"feature", func(o *agentboxCreateOptions) { o.Features = []string{"bogus-feature"} }, `unknown feature "bogus-feature"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			opts := flagsTestOpts("coder")
			tc.mutate(&opts)
			_, _, err := buildAgentboxConfig(t.Context(), opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("buildAgentboxConfig error = %v, want it to contain %q", err, tc.want)
			}
			if !IsUserError(err) {
				t.Errorf("error %q must be a user error (exit 1): the caller fixes the name", err)
			}
		})
	}
}

// End-to-end through `box create --manifest-only`, the path that never
// composes a policy at all: the typo must not leave a manifest behind.
func TestBoxCreate_ManifestOnlyRejectsUnknownCapability(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubStoppedSpaceResolver(t)

	_, _, err := execCmd(t, newAgentboxCreateCmd(), "",
		"coder", "--manifest-only", "--workdir-mode", "ephemeral", "--capability", "bogus.capability")
	if err == nil || !strings.Contains(err.Error(), `unknown capability "bogus.capability"`) {
		t.Fatalf("box create error = %v, want the unknown-capability error", err)
	}
	if boxManifestExists(t, "default", "coder") {
		t.Error("rejected create left a manifest behind; it would block the corrected re-run")
	}
}

// An explicit --mount round-trips into Config.Mounts as the user's extra bind.
// A generic-shell box adds no managed agent-state slot, so the bind stands
// alone — proving --mount feeds Config.Mounts directly.
func TestBuildAgentboxConfig_MountRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	opts := flagsTestOpts("coder")
	opts.Mounts = []agentbox.Mount{{Source: "/h/src", Target: "/c/dst", ReadOnly: true}}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("got %d mounts, want 1: %+v", len(cfg.Mounts), cfg.Mounts)
	}
	if cfg.Mounts[0] != (agentbox.Mount{Source: "/h/src", Target: "/c/dst", ReadOnly: true}) {
		t.Errorf("mount = %+v, want /h/src -> /c/dst (ro)", cfg.Mounts[0])
	}
}
