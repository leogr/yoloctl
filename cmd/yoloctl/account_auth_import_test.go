package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// writeHostClaudeJSON writes a fake ~/.claude.json holding an API key under
// primaryApiKey (claude's documented field), in a temp HOME.
func writeHostClaudeJSON(t *testing.T, home, key string) {
	t.Helper()
	body := `{"primaryApiKey":"` + key + `","hasCompletedOnboarding":true}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeAuthTarget builds the resolved NAME AGENT target for the claude agent
// against a temp config tree.
func claudeAuthTarget(t *testing.T, name string) accountAuthTarget {
	t.Helper()
	profile, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Auth == nil || profile.Auth.ImportSecret == nil {
		t.Fatal("claude profile must declare auth.import_secret")
	}
	stateDir, err := account.StatePath(name, "claude")
	if err != nil {
		t.Fatal(err)
	}
	return accountAuthTarget{space: "default", name: name, profile: profile, stateDir: stateDir}
}

// TestImportSecretIntoAccount_ExtractsToSecret: `account auth import` with no
// native login file extracts the key from ~/.claude.json's primaryApiKey field
// into the account's ANTHROPIC_API_KEY secret (the env-injection path), instead
// of copying a file claude wouldn't read in-box.
func TestImportSecretIntoAccount_ExtractsToSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeHostClaudeJSON(t, home, "sk-extracted-123")

	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	tgt := claudeAuthTarget(t, "personal")

	if err := importSecretIntoAccount(tgt, *tgt.profile.Auth.ImportSecret, io.Discard, io.Discard); err != nil {
		t.Fatalf("importSecretIntoAccount: %v", err)
	}
	acct, err := account.Load("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if got := acct.Secrets["ANTHROPIC_API_KEY"]; got != "sk-extracted-123" {
		t.Errorf("extracted ANTHROPIC_API_KEY = %q, want sk-extracted-123", got)
	}
}

// Import requires an existing account, including an ordinary name "default".
func TestImportSecretIntoAccount_RejectsMissingAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeHostClaudeJSON(t, home, "sk-auto-456")

	tgt := claudeAuthTarget(t, account.Default)
	if err := importSecretIntoAccount(tgt, *tgt.profile.Auth.ImportSecret, io.Discard, io.Discard); err == nil {
		t.Fatal("missing default should fail")
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Fatal("default was auto-created")
	}

	named := claudeAuthTarget(t, "personal")
	if err := importSecretIntoAccount(named, *named.profile.Auth.ImportSecret, io.Discard, io.Discard); err == nil {
		t.Fatal("import into a missing named account must fail with the create hint")
	}
	if ok, _ := account.Exists("default", "personal"); ok {
		t.Error("failed import left a side-effect account behind")
	}
}

// TestImportSecretIntoAccount_NeverLogsValue: the extracted key value must never
// appear in any emitted output (only the field/secret names + path).
func TestImportSecretIntoAccount_NeverLogsValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const secret = "sk-must-not-leak-987"
	writeHostClaudeJSON(t, home, secret)

	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	tgt := claudeAuthTarget(t, "personal")

	var out, errw strings.Builder
	if err := importSecretIntoAccount(tgt, *tgt.profile.Auth.ImportSecret, &out, &errw); err != nil {
		t.Fatalf("import: %v", err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errw.String(), secret) {
		t.Fatalf("secret value leaked into output:\nout=%q\nerr=%q", out.String(), errw.String())
	}
}

// The TUI passes argv verbatim rather than asking a shell to expand '~'. An
// explicitly completed --file path must therefore be expanded by the command
// itself before it is read.
func TestAccountAuthImportExpandsExplicitTildeFile(t *testing.T) {
	native := fakeNativeAccounts(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const body = `{"tokens":{"access_token":"test"}}`
	if err := os.WriteFile(filepath.Join(home, "login.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	mustCreate(t, account.Default)
	if _, _, err := execAccount(t, newAccountAuthImportCmd(), "", account.Default, "codex", "--file", "~/login.json"); err != nil {
		t.Fatalf("auth import: %v", err)
	}
	stateDir, err := native(account.Default, "codex"), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(stateDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("imported auth = %q, want %q", got, body)
	}
}
