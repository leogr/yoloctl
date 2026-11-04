package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

// createFlagsTestCmd builds a command registering the image + features flags
// that `box create` exposes, binding each to the given opts (so Flags().Set
// both flips Changed and populates the field, exactly as cobra does at runtime).
func createFlagsTestCmd(opts *agentboxCreateOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().StringVar(&opts.Image, "image", "", "")
	cmd.Flags().StringSliceVar(&opts.Features, "features", nil, "")
	return cmd
}

// applyAgentProfileDefaults prefills --image and --features (the agent's
// required features, which include its CLI-installing feature) from the agent
// profile, but an explicit --features must suppress the feature prefill so the
// user's list wins. This is the positive-grant replacement for the old
// policy_preset prefill.
func TestApplyAgentProfileDefaults(t *testing.T) {
	profile := &agentprofile.Profile{
		Image:            "yoloctl/agentbox:test",
		RequiresFeatures: []string{"claude", "github"},
	}

	t.Run("agent only prefills image and features", func(t *testing.T) {
		var opts agentboxCreateOptions
		opts.Agent = "claude"
		cmd := createFlagsTestCmd(&opts)
		applyAgentProfileDefaults(cmd, &opts, profile)
		if opts.Image != profile.Image {
			t.Errorf("image: got %q, want %q", opts.Image, profile.Image)
		}
		if want := []string{"claude", "github"}; !reflect.DeepEqual(opts.Features, want) {
			t.Errorf("features: got %v, want %v", opts.Features, want)
		}
	})

	t.Run("explicit --features suppresses the feature prefill", func(t *testing.T) {
		var opts agentboxCreateOptions
		opts.Agent = "claude"
		cmd := createFlagsTestCmd(&opts)
		if err := cmd.Flags().Set("features", "go,github,npm"); err != nil {
			t.Fatalf("set --features: %v", err)
		}
		applyAgentProfileDefaults(cmd, &opts, profile)
		if want := []string{"go", "github", "npm"}; !reflect.DeepEqual(opts.Features, want) {
			t.Errorf("explicit features must win: got %v, want %v", opts.Features, want)
		}
		if opts.Image != profile.Image {
			t.Errorf("image should still prefill (not a feature source): got %q", opts.Image)
		}
	})

	t.Run("explicit --image is not overwritten", func(t *testing.T) {
		var opts agentboxCreateOptions
		opts.Agent = "claude"
		cmd := createFlagsTestCmd(&opts)
		if err := cmd.Flags().Set("image", "example.com/custom:1"); err != nil {
			t.Fatalf("set --image: %v", err)
		}
		applyAgentProfileDefaults(cmd, &opts, profile)
		if opts.Image != "example.com/custom:1" {
			t.Errorf("explicit image overwritten by profile: got %q", opts.Image)
		}
	})
}

// Partial command definitions still prefill required features when the
// optional feature flag is absent; flagChanged remains nil-safe.
func TestApplyAgentProfileDefaultsWithoutFeatureFlag(t *testing.T) {
	profile := &agentprofile.Profile{Image: "img:1", RequiresFeatures: []string{"claude", "github"}}
	var opts agentboxCreateOptions
	opts.Agent = "claude"
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().StringVar(&opts.Image, "image", "", "")
	// Deliberately omit --features.
	applyAgentProfileDefaults(cmd, &opts, profile)
	if want := []string{"claude", "github"}; !reflect.DeepEqual(opts.Features, want) {
		t.Errorf("features must prefill without a feature flag, got %v", opts.Features)
	}
	if opts.Image != "img:1" {
		t.Errorf("image must prefill, got %q", opts.Image)
	}
}

// A stale built-in file left by the retired install-to-disk catalog must not
// leak its per-agent image into box create. This mirrors:
//
//	yoloctl box create --agent claude --features go --features github NAME
//
// The explicit box features stay explicit, while the strict runtime feature
// view also adds the embedded agent's required `claude` feature and its
// reporter.
func TestApplyAgentProfileDefaultsIgnoresLegacyBuiltinFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := layout.AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := []byte("name: claude\nimage: legacy.invalid/claude:old\ncommand: claude\n")
	if err := os.WriteFile(filepath.Join(dir, "claude.yaml"), stale, 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := lookupAgentProfile("claude")
	if err != nil {
		t.Fatal(err)
	}
	var opts agentboxCreateOptions
	opts.Agent = "claude"
	cmd := createFlagsTestCmd(&opts)
	if err := cmd.Flags().Set("features", "go,github"); err != nil {
		t.Fatal(err)
	}
	applyAgentProfileDefaults(cmd, &opts, profile)

	if opts.Image != "yoloctl/agentbox:ubuntu-24.04" {
		t.Fatalf("image = %q, want the embedded plain base", opts.Image)
	}
	cfg := &agentbox.Config{Agent: opts.Agent, Image: opts.Image, Features: opts.Features}
	effective, err := agentbox.EffectiveFeaturesStrict(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(effective, ","); got != "go,github,claude,yolo.agent.observe" {
		t.Fatalf("effective features = %q, want go,github,claude,yolo.agent.observe", got)
	}
}
