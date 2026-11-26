package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
)

func TestBoxSecretFlagsAndManifestCopy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubStoppedSpaceResolver(t)
	const value = "fixture-account-value"
	acct := account.New("default", "shared")
	acct.SetSecret("team:publisher", value)
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	copyBox := func(args ...string) error {
		t.Helper()
		stdout, stderr, err := execCmd(t, newAgentboxCopyCmd(), "", args...)
		if strings.Contains(stdout+stderr, value) || (err != nil && strings.Contains(err.Error(), value)) {
			t.Fatal("credential exposed in CLI output")
		}
		return err
	}
	// The shared host/operator request decoder must retain each --secret
	// operand through the worker parser, including a colon in an account key.
	root := newRootCmd()
	argv, err := controllerCommandArgs(root, controller.Request{
		ID: "test-request-0123456789", Method: "box.create", Space: "default", Args: []string{"publisher"}, Dash: -1,
		Flags: map[string][]string{
			"manifest-only": {"true"}, "workdir-mode": {"ephemeral"}, "agent": {"codex"}, "account": {"shared"},
			"secret": {"team:publisher:GH_TOKEN", "PENDING", "PENDING:PENDING"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := execCmd(t, root, "", argv...); err != nil || strings.Contains(stdout+stderr, value) {
		t.Fatal("request failed or exposed a secret value", err)
	}
	for name, overrides := range map[string][]string{
		"worker": nil, "reviewer": {"--secret="}, "alternate": {"--secret", "OTHER:GH_TOKEN"},
		"shell": {"--agent", "none"},
	} {
		args := append([]string{"publisher", name, "--manifest-only"}, overrides...)
		if err := copyBox(args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyBox("publisher", "reject", "--manifest-only", "--agent", "none", "--secret", "OTHER"); err == nil {
		t.Fatal("agentless copy accepted an explicit reference")
	}
	for name, rules := range map[string][]agentprofile.SecretInjection{
		"publisher": {{Secret: "team:publisher", Env: "GH_TOKEN"}, {Secret: "PENDING"}},
		"worker":    {{Secret: "team:publisher", Env: "GH_TOKEN"}, {Secret: "PENDING"}},
		"reviewer":  nil, "alternate": {{Secret: "OTHER", Env: "GH_TOKEN"}}, "shell": nil,
	} {
		cfg, err := agentbox.Load("default", name)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.SecretInjection, rules) {
			t.Fatalf("%s has the wrong requested mappings", name)
		}
		manifest, err := os.ReadFile(cfg.Path())
		if err != nil || bytes.Contains(manifest, []byte(value)) {
			t.Fatal("manifest could not be checked or contains the credential", err)
		}
		if name != "shell" && (cfg.Account != acct.Name || cfg.Agent != "codex") {
			t.Fatal("mapping altered the selected native identity")
		}
	}
}

func TestBoxSetAndShowRequestedSecretNames(t *testing.T) {
	cfg := &agentbox.Config{Name: "publisher", Space: "default", Agent: "codex", Account: "shared"}
	rules, err := parseSecretFlags([]string{"SOURCE:TARGET", "DEFAULT"})
	if err != nil {
		t.Fatal(err)
	}
	set := setOptions{SecretInjection: rules}
	if changed, err := applySetOptions(cfg, set); err != nil || !changed {
		t.Fatal("set did not add mappings", err)
	}
	if changed, err := applySetOptions(cfg, set); err != nil || changed {
		t.Fatal("repeat set rewrote mappings", err)
	}
	for _, spec := range []string{"", "KEY:", ":ENV", "KEY:not-an-env"} {
		if _, err := parseSecretFlags([]string{spec}); err == nil {
			t.Fatal("invalid secret reference accepted")
		}
	}
	b := boxstate.Box{Name: cfg.Name, Space: cfg.Space, Config: cfg}
	details := &container.Details{Name: cfg.Name, Env: []string{"TARGET=fixture-private-value"}}
	for _, output := range []func(*bytes.Buffer) error{
		func(out *bytes.Buffer) error { return writeBoxStateJSON(out, b) },
		func(out *bytes.Buffer) error {
			return writeAgentboxShowJSON(out, details, cfg.Space, landlockState{}, agentStateMount{}, agentboxShowMetadata{}, sessionStatus{}, &b)
		},
	} {
		var raw bytes.Buffer
		if err := output(&raw); err != nil {
			t.Fatal(err)
		}
		var result struct {
			SecretInjection []agentprofile.SecretInjection `json:"secret_injection"`
		}
		if json.Unmarshal(raw.Bytes(), &result) != nil || strings.Contains(raw.String(), "fixture-private-value") ||
			!slices.Equal(result.SecretInjection, []agentprofile.SecretInjection{{Secret: "SOURCE", Env: "TARGET"}, {Secret: "DEFAULT", Env: "DEFAULT"}}) {
			t.Fatal("show JSON failed to represent requested names")
		}
	}
	var human bytes.Buffer
	if err := writeBoxStateSummary(&human, b); err != nil || !strings.Contains(human.String(), "Requested account secret mappings:") || !strings.Contains(human.String(), `"DEFAULT" → DEFAULT`) {
		t.Fatal("compact show lost requested mappings", err)
	}
}
