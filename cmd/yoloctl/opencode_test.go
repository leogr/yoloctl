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

func TestOpenCodeManagedLaunchAndState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile, err := agentprofile.Lookup("opencode")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		Name: "reviewer", Space: "default", Agent: "opencode", Account: "personal",
		Image: profile.Image, AgentExtraArgs: []string{"--model", "test-model"},
	}
	argv, err := resolveSessionArgv("default", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env", "NO_PROXY=localhost,127.0.0.1,::1", "NPM_CONFIG_CACHE=/home/sandbox/.local/share/opencode/cache/npm",
		`OPENCODE_CONFIG_CONTENT={"share":"disabled"}`,
		"OPENCODE_DISABLE_AUTOUPDATE=true", "OPENCODE_DISABLE_LSP_DOWNLOAD=true", "OPENCODE_DISABLE_MODELS_FETCH=true",
		"XDG_CACHE_HOME=/home/sandbox/.local/share/opencode/cache", "XDG_CONFIG_HOME=/home/sandbox/.local/share/opencode/config",
		"XDG_DATA_HOME=/home/sandbox/.local/share", "XDG_STATE_HOME=/home/sandbox/.local/share/opencode/state",
		"opencode", "--auto", "--model", "test-model",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("managed launch = %v, want %v", argv, want)
	}
	mount, err := resolveAgentStateMount(profile, cfg)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := layout.AccountStatePath("personal", "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !mount.Enabled || mount.Source != leaf || mount.Destination != "/home/sandbox/.local/share/opencode" {
		t.Fatalf("wrong native leaf bind: %+v", mount)
	}
	effective, err := agentbox.EffectiveFeaturesStrict(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(effective, []string{"opencode", "github"}) {
		t.Fatalf("effective features = %v", effective)
	}
	_, recipe, installed, err := agentboximage.ComposedImageRef(cfg.Image, effective)
	if err != nil || !installed || !strings.Contains(recipe, "opencode-ai@latest") {
		t.Fatalf("OpenCode image does not install CLI: installed=%v, error=%v", installed, err)
	}
	for _, required := range []string{"--no-install-recommends ripgrep", "test -x /home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe"} {
		if !strings.Contains(recipe, required) {
			t.Fatalf("OpenCode recipe is missing %q", required)
		}
	}
	if profile.NpmPackage != "opencode-ai" {
		t.Fatalf("update package = %q", profile.NpmPackage)
	}
	argv = npmInstallGlobalAtLatestArgs(cfg.Name, profile.NpmPackage)
	if !slices.Contains(argv, "opencode-ai@latest") || !slices.Contains(argv, "--prefix=/home/sandbox/.npm-global") {
		t.Fatalf("OpenCode update does not use managed npm installation: %v", argv)
	}
}

func TestOpenCodeNativeImportDoesNotCopySettingsOrSynchronize(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	native := fakeNativeAccounts(t)
	mustCreate(t, "personal")
	hostDir := filepath.Join(hostHome, ".local", "share", "opencode")
	if err := os.MkdirAll(hostDir, 0700); err != nil {
		t.Fatal(err)
	}
	const initial = `{"openai":{"type":"api","key":"synthetic-import"}}`
	hostAuth := filepath.Join(hostDir, "auth.json")
	if err := os.WriteFile(hostAuth, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+stderr, "synthetic-import") {
		t.Fatal("import printed credential contents")
	}
	dst := filepath.Join(native("personal", "opencode"), "auth.json")
	raw, err := os.ReadFile(dst)
	if err != nil || string(raw) != initial {
		t.Fatalf("import did not copy native credentials: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("import permissions: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(native("personal", "opencode"), "settings.json")); !os.IsNotExist(err) {
		t.Fatal("credential import unexpectedly copied settings")
	}
	const refreshed = `{"openai":{"type":"api","key":"synthetic-agent-refresh"}}`
	if err := os.WriteFile(dst, []byte(refreshed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hostAuth); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execAccount(t, newAccountAuthStatusCmd(), "", "personal", "opencode"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(dst)
	if err != nil || string(raw) != refreshed {
		t.Fatal("status changed agent-owned credentials or depended on the host file")
	}
}

func TestOpenCodeCatalogCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, path := range [][]string{{"agent", "show"}, {"agent", "copy"}, {"agent", "update"}} {
		got, _ := runCompleter(t, nil, path...)
		if !slices.Contains(got, "opencode") {
			t.Fatalf("%v does not complete OpenCode: %v", path, got)
		}
	}
	for _, path := range [][]string{{"account", "auth", "import"}, {"account", "auth", "status"}} {
		got, _ := runCompleter(t, []string{"personal"}, path...)
		if !slices.Contains(got, "opencode") {
			t.Fatalf("%v does not complete OpenCode: %v", path, got)
		}
	}
}

func TestAuthImportHomeDirPreservesPreferredOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := &agentprofile.Profile{
		StateContainerPath: "/home/sandbox/.custom",
		Auth: &agentprofile.AuthSpec{
			ImportHomeDir: ".local/share/custom", CredentialFile: "auth.json",
			ImportFiles: []string{"preferred.json", "auth.json", "preferred.json"},
		},
	}
	paths, err := defaultHostAuthPaths(profile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, ".local/share/custom/preferred.json"), filepath.Join(home, ".local/share/custom/auth.json")}
	if !slices.Equal(paths, want) {
		t.Fatalf("host import candidates = %v, want %v", paths, want)
	}
	profile.Auth.ImportHomeDir = ""
	paths, err = defaultHostAuthPaths(profile)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{filepath.Join(home, ".custom/preferred.json"), filepath.Join(home, ".custom/auth.json")}
	if !slices.Equal(paths, want) {
		t.Fatalf("legacy host import candidates = %v, want %v", paths, want)
	}
}
