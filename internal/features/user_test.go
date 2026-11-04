package features

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func writeUserFeature(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUserFeatures_MissingDirReturnsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := LoadUserFeatures()
	if err != nil {
		t.Fatalf("LoadUserFeatures: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing dir should return empty, got %v", got)
	}
}

func TestLoadUserFeatures_BasicLoad(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "internal-tools", `
name: internal-tools
description: Corporate internal tools
install:
  kind: dockerfile
  dockerfile: "RUN apt-get update && apt-get install -y --no-install-recommends jq && rm -rf /var/lib/apt/lists/*"
requires_capabilities:
  - network.internal-tools
`)
	got, err := LoadUserFeatures()
	if err != nil {
		t.Fatalf("LoadUserFeatures: %v", err)
	}
	f, ok := got["internal-tools"]
	if !ok {
		t.Fatalf("internal-tools not loaded; got keys=%v", keys(got))
	}
	if f.Description != "Corporate internal tools" {
		t.Errorf("Description = %q", f.Description)
	}
	if f.Install == nil || f.Install.Kind != InstallDockerfile || !strings.Contains(f.Install.Dockerfile, "jq") {
		t.Errorf("install not parsed: %+v", f.Install)
	}
	if len(f.RequiresCapabilities) != 1 || f.RequiresCapabilities[0] != "network.internal-tools" {
		t.Errorf("RequiresCapabilities = %v", f.RequiresCapabilities)
	}
}

func TestLoadUserFeatures_NameFilenameMismatchErrors(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "foo", "name: bar\ndescription: Mismatched\n")
	_, err := LoadUserFeatures()
	if err == nil {
		t.Fatal("expected name/filename mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), `name "bar" must equal filename "foo"`) {
		t.Errorf("error %q does not call out the mismatch", err.Error())
	}
}

func TestLoadUserFeatures_MissingNameErrors(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "anonymous", "description: No name set\n")
	_, err := LoadUserFeatures()
	if err == nil {
		t.Fatal("expected missing-name error, got nil")
	}
	if !strings.Contains(err.Error(), "`name` is required") {
		t.Errorf("error %q does not mention missing name", err.Error())
	}
}

// TestLoadUserFeatures_InvalidInstallKindErrors: install validation rejects an
// unknown install kind.
func TestLoadUserFeatures_InvalidInstallKindErrors(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "bad-install", `
name: bad-install
description: Bogus install kind
install:
  kind: wizardry
`)
	_, err := LoadUserFeatures()
	if err == nil {
		t.Fatal("expected invalid install.kind error, got nil")
	}
	if !strings.Contains(err.Error(), "install.kind") {
		t.Errorf("error %q does not mention install.kind", err.Error())
	}
}

// TestLoadUserFeatures_NpmInstallNeedsPackage: kind=npm without a package is
// rejected.
func TestLoadUserFeatures_NpmInstallNeedsPackage(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "no-pkg", "name: no-pkg\ninstall:\n  kind: npm\n")
	_, err := LoadUserFeatures()
	if err == nil || !strings.Contains(err.Error(), "install.package") {
		t.Fatalf("expected install.package error, got %v", err)
	}
}

func TestLoadUserFeatures_SkipsNonYAMLAndDirs(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserFeature(t, dir, "real", "name: real\ndescription: A real feature\n")
	got, err := LoadUserFeatures()
	if err != nil {
		t.Fatalf("LoadUserFeatures: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d features, want 1; keys=%v", len(got), keys(got))
	}
	if _, ok := got["real"]; !ok {
		t.Errorf("`real` feature missing from result")
	}
}

// TestLookup_UserCannotOverrideBuiltin: a custom feature whose name collides
// with a built-in is REJECTED (same-name overrides of built-ins
// are not allowed; the user must copy the built-in to a new name). This replaces
// the old "user overrides embedded" behavior.
func TestLookup_UserCannotOverrideBuiltin(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	// A custom file shadowing the built-in `github` must error, not override.
	writeUserFeature(t, dir, "github", `
name: github
description: Local override attempt
requires_capabilities:
  - network.github
`)
	_, err := Lookup("github")
	if err == nil {
		t.Fatal("expected a custom feature shadowing a built-in name to be rejected")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("rejection error %q should mention it is a built-in", err)
	}
	// LoadUserFeatures surfaces the same rejection directly.
	if _, err := LoadUserFeatures(); err == nil {
		t.Error("LoadUserFeatures should reject a custom file with a built-in name")
	}
}

// TestIsBuiltin: built-in feature names report true; custom ones false.
func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("github") || !IsBuiltin("claude") {
		t.Error("github/claude should be built-in features")
	}
	if IsBuiltin("my-github") {
		t.Error("my-github should not be a built-in")
	}
}

// TestCopyBuiltinToCustomLoads: SaveUser writing a BUILT-IN feature under a new
// name produces a valid custom YAML the loader re-reads — the `feature copy`
// path. Features are stored verbatim, so install + requires_capabilities carry
// over.
func TestCopyBuiltinToCustomLoads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	src.Name = "my-claude"
	if err := SaveUser(src); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := Lookup("my-claude")
	if err != nil {
		t.Fatalf("Lookup copied feature: %v", err)
	}
	if IsBuiltin("my-claude") {
		t.Error("the copy must be custom, not built-in")
	}
	if got.Install == nil || got.Install.Kind != InstallNpm || got.Install.Package != "@anthropic-ai/claude-code" {
		t.Errorf("copied install = %+v, want npm @anthropic-ai/claude-code", got.Install)
	}
	if len(got.RequiresCapabilities) != 1 || got.RequiresCapabilities[0] != "network.anthropic" {
		t.Errorf("copied requires_capabilities = %v, want [network.anthropic]", got.RequiresCapabilities)
	}
}

// TestSaveUserRejectsBuiltinName / DeleteUser refuse built-in names.
func TestSaveDeleteUserRejectsBuiltin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := Lookup("github")
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	if err := SaveUser(f); err == nil {
		t.Error("SaveUser should refuse a built-in name")
	}
	if err := DeleteUser("github"); err == nil {
		t.Error("DeleteUser should refuse a built-in name")
	}
}

// TestDeleteUserCustom: a custom feature is removed by DeleteUser; Lookup then
// fails.
func TestDeleteUserCustom(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	f := Feature{Name: "tmp-feature", Description: "x", RequiresCapabilities: []string{"network.github"}}
	if err := SaveUser(f); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	if _, err := Lookup("tmp-feature"); err != nil {
		t.Fatalf("precondition Lookup: %v", err)
	}
	if err := DeleteUser("tmp-feature"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := Lookup("tmp-feature"); err == nil {
		t.Error("expected Lookup to fail after DeleteUser")
	}
}

func TestLookup_EmbeddedFallbackWhenUserMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	if f.Install == nil || f.Install.Kind != InstallNpm {
		t.Errorf("expected embedded claude npm-install feature, got %+v", f)
	}
	if len(f.RequiresCapabilities) != 1 || f.RequiresCapabilities[0] != "network.anthropic" {
		t.Errorf("claude RequiresCapabilities = %v", f.RequiresCapabilities)
	}
}

func TestLookup_BrokenUserFeatureSurfacesError(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte(":\n  not: valid: yaml: :"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Lookup("claude")
	if err == nil {
		t.Fatal("expected load-user-features error, got nil")
	}
	if !strings.Contains(err.Error(), "load user features") {
		t.Errorf("error %q does not wrap LoadUserFeatures", err.Error())
	}
}

func TestNames_IncludesUserFeatures(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := testFeaturesDir(t)
	writeUserFeature(t, dir, "internal-tools", "name: internal-tools\ndescription: Local\n")
	names := Names()
	if !slices.Contains(names, "internal-tools") {
		t.Errorf("user feature `internal-tools` missing from Names(): %v", names)
	}
	for _, want := range []string{"claude", "github", "go"} {
		if !slices.Contains(names, want) {
			t.Errorf("embedded feature %q missing from Names(): %v", want, names)
		}
	}
}

func keys(m map[string]Feature) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func testFeaturesDir(t *testing.T) string {
	t.Helper()
	dir, err := layout.FeaturesDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
