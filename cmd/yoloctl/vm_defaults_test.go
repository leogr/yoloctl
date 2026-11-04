package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
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
