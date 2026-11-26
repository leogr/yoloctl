package agentbox

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentprofile"
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
	// Codex native state and optional AWS injection both belong to the
	// selected account. The final account read must therefore
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

func TestBoxSecretInjectionUsesSelectedAccountAtCreation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const original = "fixture value # with equals= and\na second line"
	const rotated = "fixture rotated"
	const independent = "fixture other account"
	acct := account.New("default", "shared")
	acct.SetSecret("team:publisher", original)
	acct.SetSecret("AWS_ACCESS_KEY_ID", "fixture-default")
	acct.SetSecret("EMPTY", "")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	other := account.New("staging", "shared")
	other.SetSecret("team:publisher", independent)
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	publisher := &Config{Name: "publisher", Space: "default", Agent: "codex", Account: "shared", Image: "example:latest", Workdir: NewEphemeralWorkdir("publisher")}
	publisher.SecretInjection = []agentprofile.SecretInjection{
		{Secret: "team:publisher", Env: "GH_TOKEN"},
		{Secret: "EMPTY"}, {Secret: "MISSING"},
	}
	reviewer := *publisher
	reviewer.Name, reviewer.SecretInjection = "reviewer", nil
	env := func(c *Config) map[string]string {
		t.Helper()
		raw, err := userEnvironmentJSON(c)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]string{}
		if raw != "" && json.Unmarshal([]byte(raw), &result) != nil {
			t.Fatal("private environment was not a JSON map")
		}
		return result
	}
	published := env(publisher)
	if len(published) != 2 || published["GH_TOKEN"] != original || published["AWS_ACCESS_KEY_ID"] != "fixture-default" {
		t.Fatal("publisher got the wrong account projection")
	}
	if reviewed := env(&reviewer); len(reviewed) != 1 || reviewed["AWS_ACCESS_KEY_ID"] != "fixture-default" {
		t.Fatal("reviewer got more than its existing profile projection")
	}
	dest := *publisher
	dest.Space = "staging"
	if copied := env(&dest); len(copied) != 1 || copied["GH_TOKEN"] != independent {
		t.Fatal("copy did not use the independent destination account")
	}
	opts, err := buildRunOptions(publisher, publisher.Image, &EndpointBinary{SHA256: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(opts.PrivateEnv, openshellUserEnvironment+"=") || strings.Contains(strings.Join(opts.Env, "\n"), openshellUserEnvironment) {
		t.Fatal("privileged projection entered public environment arguments")
	}
	visible, err := json.Marshal([]any{opts.Env, opts.Command, opts.Labels, publisher})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(visible), "fixture value") {
		t.Fatal("secret leaked into the manifest or launch metadata")
	}
	acct.SetSecret("team:publisher", rotated)
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	if published["GH_TOKEN"] != original || env(publisher)["GH_TOKEN"] != rotated || publisher.secretInjectionChanged(opts.Labels) {
		t.Fatal("rotation altered the saved environment or caused mapping drift")
	}
}

func TestBoxSecretInjectionValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []agentprofile.SecretInjection
		want  string
	}{
		{"alias", []agentprofile.SecretInjection{{Secret: "team:token", Env: "GH_TOKEN"}}, ""},
		{"matches default", []agentprofile.SecretInjection{{Secret: "ANTHROPIC_API_KEY", Env: "ANTHROPIC_API_KEY"}}, ""},
		{"profile conflict", []agentprofile.SecretInjection{{Secret: "OTHER", Env: "ANTHROPIC_API_KEY"}}, "ANTHROPIC_API_KEY"},
		{"same target", []agentprofile.SecretInjection{{Secret: "FIRST", Env: "TOKEN"}, {Secret: "SECOND", Env: "TOKEN"}}, "maps both"},
		{"invalid target", []agentprofile.SecretInjection{{Secret: "team:token"}}, "environment variable name"},
		{"blank source", []agentprofile.SecretInjection{{Secret: " ", Env: "TOKEN"}}, "nonblank"},
		{"literal", []agentprofile.SecretInjection{{Secret: "TOKEN", Env: "DECLARED"}}, "box env literal"},
		{"launch env", []agentprofile.SecretInjection{{Secret: "TOKEN", Env: "CLAUDE_CONFIG_DIR"}}, "launch_env"},
		{"OpenShell", []agentprofile.SecretInjection{{Secret: "TOKEN", Env: "OPENSHELL_ENDPOINT"}}, "reserved"},
		{"chat", []agentprofile.SecretInjection{{Secret: "TOKEN", Env: "YOLOCTL_CHAT_CONFIG"}}, "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Agent: "claude", Account: "selected", SecretInjection: tc.rules, Env: map[string]EnvVar{"DECLARED": {Literal: "literal-fixture"}}}
			err := cfg.ValidateSecretInjection()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "literal-fixture") {
				t.Fatal("unexpected validation result", err)
			}
		})
	}
	shell := &Config{Agent: "none", Account: "none", SecretInjection: []agentprofile.SecretInjection{{Secret: "TOKEN"}}}
	if err := shell.ValidateSecretInjection(); err == nil {
		t.Fatal("agentless box accepted an account reference")
	}
}

func TestBoxSecretInjectionDriftRequiresExplicitRecreation(t *testing.T) {
	cfg := validConfig()
	cfg.Agent, cfg.Account = "codex", "shared"
	image, _, err := ResolvedImage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ComposePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	actual := ActualState{Exists: true, Image: image, ReadOnlyWD: !cfg.WorkdirWritable(), Labels: map[string]string{LabelAgent: "codex"}, PolicyContent: policy}
	if plan := Diff(cfg, actual, image, policy); plan.HasChanges() {
		t.Fatal("legacy container with no extra mappings drifted", plan)
	}
	cfg.SecretInjection = []agentprofile.SecretInjection{{Secret: "TOKEN"}, {Secret: "SECOND", Env: "ALIAS"}}
	if plan := Diff(cfg, actual, image, policy); !plan.NeedsRecreate || plan.BlocksStart() || !plan.SecretInjectionChanged {
		t.Fatal("adding a mapping failed to report explicit recreation", plan)
	}
	inspector := &fakeInspector{state: actual}
	applier := &fakeApplier{}
	var pending *SecretInjectionChange
	if result, err := Reconcile(t.Context(), cfg, inspector, applier); !errors.As(err, &pending) || result.HasChanges() || len(applier.recreateCalls) != 0 || len(applier.restartCalls) != 0 {
		t.Fatal("reconcile changed a container with pending secret mappings", result, err)
	}
	if err := CheckStart(t.Context(), cfg, inspector); err != nil {
		t.Fatal("pending secret mappings blocked reuse of the original environment", err)
	}
	if err := CheckSecretInjectionRecreate(WithRuntimeModeChange(t.Context()), cfg, actual.Labels); err != nil {
		t.Fatal("explicit recreation refused", err)
	}
	actual.Labels[LabelSecretInjection] = cfg.secretInjectionSnapshot()
	cfg.SecretInjection = []agentprofile.SecretInjection{{Secret: "SECOND", Env: "ALIAS"}, {Secret: "TOKEN", Env: "TOKEN"}, {Secret: "TOKEN"}}
	if plan := Diff(cfg, actual, image, policy); plan.HasChanges() {
		t.Fatal("ordering or repeats caused mapping drift", plan)
	}
	cfg.SecretInjection = nil
	if plan := Diff(cfg, actual, image, policy); !plan.SecretInjectionChanged {
		t.Fatal("removing the last mapping failed to report drift", plan)
	}
	inspector.state = actual
	if _, err := Reconcile(t.Context(), cfg, inspector, applier); !errors.As(err, &pending) {
		t.Fatal("ordinary reconcile could replace an existing secret recipient", err)
	}
}
