package features

import (
	"strings"
	"testing"
)

func TestFeatureCacheDefaultsAndDeclaredPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dirs, err := RequiredCacheDirectories([]string{"cargo", "go", "cargo", "npm", "pip"})
	if err != nil || strings.Join(dirs, ",") != "cargo/git,cargo/registry" {
		t.Fatal(dirs, err)
	}
	for _, name := range []string{"go", "npm", "pip"} {
		dirs, err := RequiredCacheDirectories([]string{name})
		if err != nil || len(dirs) != 0 {
			t.Fatal(name, dirs, err)
		}
	}
	docker, err := GenerateDockerfile("base", []string{"go", "cargo", "npm", "pip"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GOTOOLCHAIN=local", "GOCACHE=/home/sandbox/.cache/go/build", "GOMODCACHE=/home/sandbox/.cache/go/mod", "/home/sandbox/.cache/cargo/git", "/home/sandbox/.cache/cargo/registry", "ln -sT", "chown -h sandbox:sandbox", "cache=/home/sandbox/.cache/npm"} {
		if !strings.Contains(docker, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(docker, "CARGO_HOME=") {
		t.Fatal("relocated cargo bins/credentials")
	}
	for _, bad := range []string{"", ".", "..", "../account", "/home", "cargo/../git", "cargo//git"} {
		if err := validateUserFeature(Feature{Name: "custom", CacheDirectories: []string{bad}}); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
