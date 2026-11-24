package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func TestCreateAndImportUsePlatformBackendAndOptionalImage(t *testing.T) {
	for _, cmd := range []*cobra.Command{newSpaceCreateCmd(), newSpaceImportCmd()} {
		backend, _ := cmd.Flags().GetString("backend")
		image, _ := cmd.Flags().GetString("vm-image")
		if backend != vmhost.DefaultBackend() || image != "" {
			t.Fatalf("%s defaults: backend=%q image=%q", cmd.Name(), backend, image)
		}
		if err := cmd.Flags().Set("backend", "lima"); err != nil {
			t.Fatal(err)
		}
		if backend, _ = cmd.Flags().GetString("backend"); backend != "lima" {
			t.Fatal("explicit Lima lost")
		}
	}
}

func TestHostGrantsCannotExposeVMBuildCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cache, err := layout.VMImageBuildDir("firecracker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "cache-alias")
	if err := os.Symlink(cache, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cache, filepath.Dir(cache), filepath.Join(cache, "kernel"), alias} {
		if err := rejectManagedHostPath(path); err == nil {
			t.Fatalf("accepted managed cache grant %s", path)
		}
	}
	if err := rejectManagedHostPath(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// The host's compiled terminal core is native code the TUI runs: it lives under
// the config root, so no grant may expose it to a box, directly, inside it, or
// through a symlink.
func TestHostGrantsCannotExposeTerminalCoreCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cache, err := terminalCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "0123456789abcdef"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "terminal-alias")
	if err := os.Symlink(cache, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cache, filepath.Join(cache, "0123456789abcdef"), alias} {
		if err := rejectManagedHostPath(path); err == nil {
			t.Fatalf("accepted terminal core cache grant %s", path)
		}
	}
}

func TestHostTerminalCacheOnlyOnTheHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"OPENSHELL_SANDBOX", spaceenv.ContextEnv} { // the test may itself run in a box
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	previous := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) { return nil, nil }
	t.Cleanup(func() { loadOperatorClient = previous })
	want, err := terminalCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := hostTerminalCacheDir(); got != want {
		t.Fatalf("host cache %q, want %q", got, want)
	}
	t.Setenv("OPENSHELL_SANDBOX", "1")
	if got := hostTerminalCacheDir(); got != "" {
		t.Fatalf("a box process got cache %q", got)
	}
	_ = os.Unsetenv("OPENSHELL_SANDBOX")
	loadOperatorClient = func() (*controller.OperatorClient, error) { return &controller.OperatorClient{Space: "s"}, nil }
	if got := hostTerminalCacheDir(); got != "" {
		t.Fatalf("an in-box operator client got cache %q", got)
	}
}
