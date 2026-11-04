package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestGeminiManagedLaunchAndState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile, err := agentprofile.Lookup("gemini")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		Name: "reviewer", Space: "default", Agent: "gemini", Account: "personal",
		Image: profile.Image, AgentExtraArgs: []string{"--model", "test-model"},
	}
	argv, err := resolveSessionArgv("default", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env", "GEMINI_SANDBOX=false", "NO_BROWSER=true", "gemini", "--approval-mode", "yolo", "--model", "test-model"}
	if !slices.Equal(argv, want) {
		t.Fatalf("managed launch = %v, want %v", argv, want)
	}
	mount, err := resolveAgentStateMount(profile, cfg)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := layout.AccountStatePath("personal", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !mount.Enabled || mount.Source != leaf || mount.Destination != "/home/sandbox/.gemini" {
		t.Fatalf("wrong native leaf bind: %+v", mount)
	}
	effective, err := agentbox.EffectiveFeaturesStrict(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(effective, []string{"gemini", "github"}) {
		t.Fatalf("effective features = %v", effective)
	}
	_, recipe, installed, err := agentboximage.ComposedImageRef(cfg.Image, effective)
	if err != nil || !installed || !strings.Contains(recipe, "@google/gemini-cli@latest") {
		t.Fatalf("Gemini image does not install CLI: installed=%v, error=%v", installed, err)
	}
	if profile.NpmPackage != "@google/gemini-cli" {
		t.Fatalf("update package = %q", profile.NpmPackage)
	}
	argv = npmInstallGlobalAtLatestArgs(cfg.Name, profile.NpmPackage)
	if !slices.Contains(argv, "@google/gemini-cli@latest") || !slices.Contains(argv, "--prefix=/home/sandbox/.npm-global") {
		t.Fatalf("Gemini update does not use managed npm installation: %v", argv)
	}
}

func TestGeminiNativeImportDoesNotCopySettingsOrSynchronize(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	native := fakeNativeAccounts(t)
	mustCreate(t, "personal")
	hostDir := filepath.Join(hostHome, ".gemini")
	if err := os.Mkdir(hostDir, 0700); err != nil {
		t.Fatal(err)
	}
	const initial = `{"refresh_token":"synthetic-import"}`
	hostAuth := filepath.Join(hostDir, "oauth_creds.json")
	if err := os.WriteFile(hostAuth, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+stderr, "synthetic-import") {
		t.Fatal("import printed credential contents")
	}
	dst := filepath.Join(native("personal", "gemini"), "oauth_creds.json")
	raw, err := os.ReadFile(dst)
	if err != nil || string(raw) != initial {
		t.Fatalf("import did not copy native credentials: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("import permissions: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(native("personal", "gemini"), "settings.json")); !os.IsNotExist(err) {
		t.Fatal("credential import unexpectedly copied settings")
	}
	const refreshed = `{"refresh_token":"synthetic-agent-refresh"}`
	if err := os.WriteFile(dst, []byte(refreshed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hostAuth); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execAccount(t, newAccountAuthStatusCmd(), "", "personal", "gemini"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(dst)
	if err != nil || string(raw) != refreshed {
		t.Fatal("status changed agent-owned credentials or depended on the host file")
	}
}

func TestGeminiCatalogCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, path := range [][]string{{"agent", "show"}, {"agent", "copy"}, {"agent", "update"}} {
		got, _ := runCompleter(t, nil, path...)
		if !slices.Contains(got, "gemini") {
			t.Fatalf("%v does not complete Gemini: %v", path, got)
		}
	}
	for _, path := range [][]string{{"account", "auth", "import"}, {"account", "auth", "status"}} {
		got, _ := runCompleter(t, []string{"personal"}, path...)
		if !slices.Contains(got, "gemini") {
			t.Fatalf("%v does not complete Gemini: %v", path, got)
		}
	}
}
