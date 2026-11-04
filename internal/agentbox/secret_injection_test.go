package agentbox

import (
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
)

func TestUserEnvironmentJSON_AgentsProjectOnlyDeclaredAccountSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", account.Default)
	acct.SetSecret("GEMINI_API_KEY", "synthetic-gemini")
	acct.SetSecret("ANTHROPIC_API_KEY", "synthetic-anthropic")
	acct.SetSecret("GOOGLE_API_KEY", "synthetic-vertex")
	acct.SetSecret("OPENCODE_API_KEY", "synthetic-zen")
	acct.SetSecret("OPENAI_API_KEY", "synthetic-openai")
	acct.SetSecret("GOOGLE_GENERATIVE_AI_API_KEY", "synthetic-google")
	acct.SetSecret("OPENROUTER_API_KEY", "synthetic-openrouter")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ agent, want string }{
		{"gemini", `{"GEMINI_API_KEY":"synthetic-gemini"}`},
		{"agy", `{"GEMINI_API_KEY":"synthetic-gemini"}`},
		{"claude", `{"ANTHROPIC_API_KEY":"synthetic-anthropic"}`},
		{"codex", ""},
		{"pi", `{"ANTHROPIC_API_KEY":"synthetic-anthropic","GEMINI_API_KEY":"synthetic-gemini","OPENAI_API_KEY":"synthetic-openai","OPENROUTER_API_KEY":"synthetic-openrouter"}`},
		{"opencode", `{"ANTHROPIC_API_KEY":"synthetic-anthropic","GOOGLE_GENERATIVE_AI_API_KEY":"synthetic-google","OPENAI_API_KEY":"synthetic-openai","OPENCODE_API_KEY":"synthetic-zen","OPENROUTER_API_KEY":"synthetic-openrouter"}`},
	} {
		cfg := &Config{
			SchemaVersion: 1, Name: "coder", Space: "default",
			Agent: tc.agent, Account: account.Default, Image: "base",
			Workdir: NewEphemeralWorkdir("coder"),
		}
		got, err := userEnvironmentJSON(cfg)
		if err != nil || got != tc.want {
			t.Fatalf("%s secret projection = %q, %v", tc.agent, got, err)
		}
		opts, err := buildRunOptions(cfg, "base", nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(opts.Command, " "), "synthetic-") {
			t.Fatal("secret exposed in launch command")
		}
		for _, value := range opts.Labels {
			if strings.Contains(value, "synthetic-") {
				t.Fatal("secret exposed in label")
			}
		}
	}
}

// TestUserEnvironmentJSON_InjectsAccountSecret: a box whose account holds the
// agent's declared secret_injection secret renders an OPENSHELL_USER_ENVIRONMENT
// JSON map carrying that secret as the env var the agent reads — the file-free
// delivery path (the value reaches the agent's env, not its command line).
func TestUserEnvironmentJSON_InjectsAccountSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// claude declares secret_injection ANTHROPIC_API_KEY.
	acct := account.New("default", account.Default)
	acct.SetSecret("ANTHROPIC_API_KEY", "sk-test-xyz")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		SchemaVersion: 1, Name: "coder", Space: "default",
		Agent: "claude", Account: account.Default,
	}
	got, err := userEnvironmentJSON(cfg)
	if err != nil {
		t.Fatalf("userEnvironmentJSON: %v", err)
	}
	if want := `{"ANTHROPIC_API_KEY":"sk-test-xyz"}`; got != want {
		t.Errorf("userEnvironmentJSON = %q, want %q", got, want)
	}
}

// TestUserEnvironmentJSON_FailsClosed pins the strictness of the
// build/apply-path injection read: a missing account, a corrupt account
// manifest, or an unresolvable agent profile is an ERROR — the create fails
// cleanly instead of silently launching the agent with no injected
// credentials.
func TestUserEnvironmentJSON_FailsClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Missing account manifest (e.g. destroyed between the CLI preflight and
	// container create): error, not a silent no-injection launch.
	cfg := &Config{
		SchemaVersion: 1, Name: "coder", Space: "default",
		Agent: "claude", Account: account.Default,
	}
	if got, err := userEnvironmentJSON(cfg); err == nil {
		t.Errorf("missing account: got (%q, nil), want error", got)
	}
	// Codex has no secret_injection rules, but its native auth/state still
	// belongs to the selected account. The final account read must therefore
	// fail closed for it too.
	codex := &Config{
		SchemaVersion: 1, Name: "codex-box", Space: "default",
		Agent: "codex", Account: account.Default,
	}
	if got, err := userEnvironmentJSON(codex); err == nil {
		t.Errorf("missing account for agent without injection rules: got (%q, nil), want error", got)
	}

	// Corrupt account manifest: decode failures propagate.
	acct := account.New("default", account.Default)
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(acct.Path(), []byte(":\t:not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := userEnvironmentJSON(cfg); err == nil {
		t.Errorf("corrupt account manifest: got (%q, nil), want error", got)
	}
	if got, err := userEnvironmentJSON(codex); err == nil {
		t.Errorf("corrupt account for agent without injection rules: got (%q, nil), want error", got)
	}

	// Unresolvable agent profile (deleted custom agent): error — matching the
	// EffectiveFeaturesStrict convention on apply paths.
	if err := os.WriteFile(acct.Path(), mustMarshalAccount(t, account.Default), 0o600); err != nil {
		t.Fatal(err)
	}
	ghost := &Config{
		SchemaVersion: 1, Name: "coder", Space: "default",
		Agent: "ghost-agent", Account: account.Default,
	}
	if got, err := userEnvironmentJSON(ghost); err == nil {
		t.Errorf("unresolvable agent profile: got (%q, nil), want error", got)
	}
}

// mustMarshalAccount round-trips a fresh empty account manifest through Save
// and returns its on-disk bytes, restoring a valid file after a corruption
// test step.
func mustMarshalAccount(t *testing.T, name string) []byte {
	t.Helper()
	c := account.New("default", name)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestUserEnvironmentJSON_EmptyCases: no injection when there's no agent/account
// (generic shell), or when the account holds no matching secret — so a box
// without the key gets no OPENSHELL_USER_ENVIRONMENT entry (the agent falls back
// to its own auth).
func TestUserEnvironmentJSON_EmptyCases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Generic shell (agent/account none) → nothing to inject.
	shell := &Config{SchemaVersion: 1, Name: "sh", Space: "default", Agent: "none", Account: "none"}
	if got, err := userEnvironmentJSON(shell); err != nil || got != "" {
		t.Errorf("shell box: got (%q, %v), want (\"\", nil)", got, err)
	}

	// Real agent + account but NO matching secret → empty (nothing injected).
	if err := account.New("default", account.Default).Save(); err != nil { // saved with no secrets
		t.Fatal(err)
	}
	cfg := &Config{SchemaVersion: 1, Name: "coder", Space: "default", Agent: "claude", Account: account.Default}
	if got, err := userEnvironmentJSON(cfg); err != nil || got != "" {
		t.Errorf("no matching secret: got (%q, %v), want (\"\", nil)", got, err)
	}
	// A valid account plus an agent with no injection rules remains the normal
	// empty result; the account is checked even though no environment is built.
	codex := &Config{SchemaVersion: 1, Name: "codex-box", Space: "default", Agent: "codex", Account: account.Default}
	if got, err := userEnvironmentJSON(codex); err != nil || got != "" {
		t.Errorf("agent without injection rules: got (%q, %v), want (\"\", nil)", got, err)
	}
}
