package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
)

// seedAgentbox writes a minimal valid agentbox YAML under the test's
// XDG_CONFIG_HOME and returns its path.
func seedAgentbox(t *testing.T, space, name string) string {
	t.Helper()
	cfg := &agentbox.Config{
		Name:    name,
		Space:   space,
		Image:   "img:1",
		Workdir: agentbox.NewEphemeralWorkdir(name),
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("seed Save(%s/%s): %v", space, name, err)
	}
	return cfg.Path()
}

// Finding 1: create-only. An existing declaration (YAML) makes
// ensureAgentboxAbsent fail before it ever consults the container
// runtime — so `box create` refuses to clobber it. The YAML check
// short-circuits before any runtime probe, so no live VM is needed here.
func TestEnsureAgentboxAbsent_ExistingYAMLFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedAgentbox(t, "default", "coder")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded YAML: %v", err)
	}

	err = ensureAgentboxAbsent(context.Background(), "default", "coder")
	if err == nil {
		t.Fatal("expected error for an existing declaration, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention 'already exists'", err)
	}

	// Finding 1: the failed create must NOT overwrite the existing YAML.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read YAML: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("declaration was modified by a refused create:\nbefore=%q\nafter=%q", before, after)
	}
}

// Creating a configured-but-not-running box (a YAML on disk, no live
// container) must fail rather than reuse it, through ensureAgentboxAbsent.
func TestEnsureAgentboxAbsent_ConfiguredNotRunningFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedAgentbox(t, "default", "myrepo-claude")

	err := ensureAgentboxAbsent(context.Background(), "default", "myrepo-claude")
	if err == nil {
		t.Fatal("expected error for a configured-but-not-running agentbox, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want 'already exists'", err)
	}
}

// Finding 5: apply / edit / set / policy / start no longer synthesize a
// config from container state — a missing declaration is a clear error.
func TestLoadManagedAgentbox_MissingDeclarationFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := loadManagedAgentbox("default", "ghost")
	if err == nil {
		t.Fatal("expected error for a missing declaration, got nil")
	}
	if !strings.Contains(err.Error(), "no declaration") {
		t.Errorf("error = %v, want it to mention 'no declaration'", err)
	}
}

func TestLoadManagedAgentbox_PresentDeclarationLoads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedAgentbox(t, "default", "coder")
	cfg, err := loadManagedAgentbox("default", "coder")
	if err != nil {
		t.Fatalf("loadManagedAgentbox: %v", err)
	}
	if cfg.Name != "coder" || cfg.Space != "default" {
		t.Errorf("loaded wrong config: %+v", cfg)
	}
}

// Finding 2: the shared preflight validates workdir presence before any
// VM I/O, so a declaration with no workdir fails fast (and unit-testably,
// without a live VM).
func TestPreflightAgentboxRuntime_RejectsWorkdirless(t *testing.T) {
	cfg := &agentbox.Config{Name: "coder", Space: "default"} // no Workdir
	c := container.New("default")
	err := preflightAgentboxRuntime(context.Background(), c, cfg, false)
	if err == nil {
		t.Fatal("expected error for a workdirless agentbox, got nil")
	}
	if !strings.Contains(err.Error(), "no workdir") {
		t.Errorf("error = %v, want it to mention 'no workdir'", err)
	}
}

func assertHasPath(t *testing.T, paths []string, want string) {
	t.Helper()
	if !slices.Contains(paths, want) {
		t.Errorf("path %q not in %v", want, paths)
	}
}

// countMountsAt returns how many extra mounts target dest.
func countMountsAt(ms []agentbox.Mount, dest string) int {
	n := 0
	for _, m := range ms {
		if m.Target == dest {
			n++
		}
	}
	return n
}

func managedMountSource(ms []agentbox.Mount, dest string) string {
	for _, m := range ms {
		if m.Target == dest {
			return m.Source
		}
	}
	return ""
}

// applyAgentStateMount keeps cfg.Mounts carrying exactly the managed
// account-state mount the CURRENT account resolves to — re-pointing it when
// the box's account changes (a different account = a different identity home,
// same agent leaf name) — without disturbing unrelated user mounts.
func TestApplyAgentStateMount_AccountChanges(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wantDefault, err := layout.AccountStatePath("default", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	wantPersonal, err := layout.AccountStatePath("personal", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	const stateDest = "/home/sandbox/.claude" // claude profile StateContainerPath

	wd := t.TempDir()
	cfg := &agentbox.Config{Name: "coder", Space: "default", Agent: "claude", Account: "default"}
	workdir, _, err := agentbox.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	cfg.Workdir = workdir
	userMount := agentbox.Mount{Source: "/host/data", Target: "/data", ReadOnly: true}
	cfg.Mounts = []agentbox.Mount{userMount}

	// default account → exactly one managed mount at its claude leaf; user kept.
	if _, changed, err := applyAgentStateMount(cfg); err != nil || !changed {
		t.Fatalf("default apply: changed=%v err=%v", changed, err)
	}
	if got := managedMountSource(cfg.Mounts, stateDest); got != wantDefault {
		t.Errorf("default account source = %q, want %q", got, wantDefault)
	}
	if countMountsAt(cfg.Mounts, stateDest) != 1 {
		t.Errorf("expected exactly one managed mount, got %d (%+v)", countMountsAt(cfg.Mounts, stateDest), cfg.Mounts)
	}
	if !slices.Contains(cfg.Mounts, userMount) {
		t.Errorf("user mount dropped: %+v", cfg.Mounts)
	}
	// Idempotent: a second apply with the same account changes nothing.
	if _, changed, _ := applyAgentStateMount(cfg); changed {
		t.Errorf("second apply with same account should be a no-op: %+v", cfg.Mounts)
	}

	// Re-point to another account: managed mount re-points to that identity's
	// claude leaf, still exactly one.
	cfg.Account = "personal"
	if _, changed, err := applyAgentStateMount(cfg); err != nil || !changed {
		t.Fatalf("re-point apply: changed=%v err=%v", changed, err)
	}
	if got := managedMountSource(cfg.Mounts, stateDest); got != wantPersonal {
		t.Errorf("re-pointed account source = %q, want %q", got, wantPersonal)
	}
	if countMountsAt(cfg.Mounts, stateDest) != 1 {
		t.Errorf("expected exactly one managed mount after re-point, got %d", countMountsAt(cfg.Mounts, stateDest))
	}
	if !slices.Contains(cfg.Mounts, userMount) {
		t.Errorf("user mount dropped after re-point: %+v", cfg.Mounts)
	}
}
