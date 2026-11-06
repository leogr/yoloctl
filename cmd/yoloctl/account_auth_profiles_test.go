package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
)

func TestClaudeProfileRelocationKeepsOnlyTheOriginalLeafBind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &agentbox.Config{Agent: "claude", Account: "first"}
	first, _, err := applyAgentStateMount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Source != first.Source || cfg.Mounts[0].Target != "/home/sandbox/.claude" {
		t.Fatal("Claude acquired a replaceable child bind", cfg.Mounts)
	}
	cfg.Account = "second"
	second, _, err := applyAgentStateMount(cfg)
	if err != nil || len(cfg.Mounts) != 1 || cfg.Mounts[0].Source != second.Source || second.Source == first.Source {
		t.Fatal("selected native leaf was not replaced", err, cfg.Mounts)
	}
	if got := agentboxHostMounts(cfg); len(got) != 0 {
		t.Fatal("native state became a host share", got)
	}
	cfg.Account = account.None
	if state, _, err := applyAgentStateMount(cfg); err != nil || state.Enabled || len(cfg.Mounts) != 0 {
		t.Fatal("account-none retained native state", err, cfg.Mounts)
	}
}

func anthropicHostFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "anthropic")
	files := map[string]string{
		"active_config":         "work\n",
		"configs/work.json":     `{"version":"1","authentication":{"type":"user_oauth"}}`,
		"credentials/work.json": `{"version":"1","type":"oauth_token","access_token":"synthetic-private-material","refresh_token":"synthetic-refresh"}`,
	}
	for rel, body := range files {
		name := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAnthropicImportPreservesSelectionLegacyAndRemoval(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	native := fakeNativeAccounts(t)
	mustCreate(t, "personal")
	dir := anthropicHostFixture(t)
	leaf := native("personal", "claude")
	if err := os.MkdirAll(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"claudeAiOauth":{"accessToken":"synthetic-legacy"}}`)
	if err := os.WriteFile(filepath.Join(leaf, ".credentials.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "claude", "--anthropic-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+stderr, "synthetic-") {
		t.Fatal("import printed credential material")
	}
	for _, rel := range []string{"active_config", "configs/work.json", "credentials/work.json"} {
		want, _ := os.ReadFile(filepath.Join(dir, rel))
		got, err := os.ReadFile(filepath.Join(leaf, "anthropic", rel))
		if err != nil || string(got) != string(want) {
			t.Fatalf("profile import lost %s", rel)
		}
	}
	got, _ := os.ReadFile(filepath.Join(leaf, ".credentials.json"))
	if string(got) != string(legacy) {
		t.Fatal("legacy login overwritten")
	}
	if _, _, err := execAccount(t, newAccountDestroyCmd(), "", "personal", "--force"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leaf); !os.IsNotExist(err) {
		t.Fatal("native subtree survived account removal")
	}
}

func TestAnthropicImportRejectsAmbiguityAndIncompleteMaterialBeforeWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	native := fakeNativeAccounts(t)
	mustCreate(t, "personal")
	dir := anthropicHostFixture(t)
	var writes int
	prior := accountStateOperation
	accountStateOperation = func(ctx context.Context, space string, req accountstate.Request) (accountstate.Result, error) {
		if req.Action == "write" {
			writes++
		}
		return prior(ctx, space, req)
	}
	if _, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "claude", "--file", filepath.Join(dir, "credentials/work.json")); err == nil || !strings.Contains(err.Error(), "--anthropic-dir") {
		t.Fatal("accepted new credentials as a legacy file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials/work.json"), []byte(`{"type":"oauth_token","access_token":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "claude", "--anthropic-dir", dir); err == nil {
		t.Fatal("accepted empty credential")
	}
	if writes != 0 {
		t.Fatal("wrote invalid import")
	}
	if _, err := os.Stat(native("personal", "claude")); !os.IsNotExist(err) {
		t.Fatal("invalid import allocated state")
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude/.credentials.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config/anthropic"), 0700); err != nil {
		t.Fatal(err)
	}
	// An empty SDK directory is not a competing login source.
	if _, err := prepareAuthImport([]string{"personal", "claude"}, "", "", "default"); err != nil {
		t.Fatal("empty Anthropic directory hid legacy source", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config/anthropic/credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config/anthropic/credentials/default.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAuthImport([]string{"personal", "claude"}, "", "", "default"); err == nil || !strings.Contains(err.Error(), "choose --file") {
		t.Fatal("ambiguous default import", err)
	}
}

func TestAnthropicImportRefusesActiveConsumerAndUnexpectedPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	fakeNativeAccounts(t)
	mustCreate(t, "personal")
	dir := anthropicHostFixture(t)
	accountStopUsers = func(context.Context, string, string, bool, io.Writer) error { return errors.New("active consumer") }
	if _, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "claude", "--anthropic-dir", dir); err == nil || !strings.Contains(err.Error(), "active consumer") {
		t.Fatal("active consumer import accepted", err)
	}
	target := claudeAuthTarget(t, "personal")
	doc, err := prepareAnthropicImport(target, dir)
	if err != nil {
		t.Fatal(err)
	}
	doc.NativeFiles[0].Path = "../other/auth.json"
	if _, err := nativeAuthWrites(target, doc); err == nil {
		t.Fatal("untrusted native write path accepted")
	}
}

func TestCredentialStatusPrintsClassificationsOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeNativeAccounts(t)
	mustCreate(t, "personal")
	accountStateOperation = func(_ context.Context, _ string, req accountstate.Request) (accountstate.Result, error) {
		var spec accountstate.AuthStatusSpec
		if req.Action != "auth-status" || json.Unmarshal(req.Data, &spec) != nil {
			t.Fatal("wrong status request")
		}
		return accountstate.Result{Auth: []accountstate.AuthFinding{{Source: req.Path, Outcome: "invalid"}, {Source: spec.ProfilesDir + "/credentials", Outcome: "credentials-present"}}}, nil
	}
	out, stderr, err := execAccount(t, newAccountAuthStatusCmd(), "", "personal", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "credentials present") || !strings.Contains(out, "provider validity not checked") || strings.Contains(out+stderr, "authenticated") {
		t.Fatalf("dishonest status: %s %v", out, err)
	}
}
