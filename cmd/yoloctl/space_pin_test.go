package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

func TestSharedEnvironmentSpaceSelection(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	t.Setenv(config.SpaceEnv, "local")
	c, _ := config.LoadContext()
	c.Space = "global"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("space", "default", "")
	for _, explicit := range []bool{false, true} {
		want := "local"
		if explicit {
			_ = cmd.Flags().Set("space", "explicit")
			want = "explicit"
		}
		got, err := selectedSpace(cmd)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
		if got := spaceScope(cmd); got != want {
			t.Fatal("completion differs", got, want)
		}
	}
	got, err := resolveSpacePositional([]string{"positional"})
	if err != nil || got != "positional" {
		t.Fatal(got, err)
	}
	got, err = resolveSpacePositional(nil)
	if err != nil || got != "local" {
		t.Fatal(got, err)
	}
	t.Setenv(config.SpaceEnv, "bad/name")
	clean := &cobra.Command{}
	clean.Flags().String("space", "default", "")
	if _, err := selectedSpace(clean); err == nil {
		t.Fatal("invalid pin silently ignored")
	}
	if scope := spaceScope(clean); scope != "" {
		t.Fatal("invalid pin completed another space", scope)
	}
}

func TestSpacePinCannotOverrideNativeContext(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_SPACE_PIN_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSpacePinCannotOverrideNativeContext$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_SPACE_PIN_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	root := t.TempDir()
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "bound", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: root, ConfigDir: filepath.Join(root, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: root, UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.SpaceEnv, "other")
	cmd := &cobra.Command{}
	cmd.Flags().String("space", "default", "")
	if got, err := selectedSpace(cmd); err != nil || got != "bound" {
		t.Fatal(got, err)
	}
	if got, err := resolveSpacePositional(nil); err != nil || got != "bound" {
		t.Fatal(got, err)
	}
	if got := spaceScope(cmd); got != "bound" {
		t.Fatal("native completion retargeted", got)
	}
}

func TestSpacePinCannotOverrideOperatorTarget(t *testing.T) {
	t.Setenv(config.SpaceEnv, "other")
	saved := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) {
		return &controller.OperatorClient{Space: "bound", Generation: "fixture"}, nil
	}
	t.Cleanup(func() { loadOperatorClient = saved })
	cmd := &cobra.Command{}
	cmd.Flags().String("space", "default", "")
	target, err := selectControllerTarget(context.Background(), cmd)
	if err != nil || target.Space != "bound" || !target.Operator {
		t.Fatal("pin overrode operator binding", target, err)
	}
	_ = cmd.Flags().Set("space", "outside")
	if _, err := selectControllerTarget(context.Background(), cmd); err == nil || !strings.Contains(err.Error(), "limited") {
		t.Fatal("operator scope widened", err)
	}
}
