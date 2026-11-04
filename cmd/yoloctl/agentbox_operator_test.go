package main

import (
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

// Exercise the shared worker handler using the same request-to-Cobra decoding
// as host and in-box operator calls, with fixture declarations and no runtime.
func TestBoxCreateOperatorPreset(t *testing.T) {
	cases := []struct {
		name       string
		flags      map[string][]string
		features   []string
		unconfined bool
		workdir    string
	}{
		{"defaults", nil, []string{"codex", "github", "yolo.agent.observe-codex", "yolo.space.operator", "yolo.chat.skill"}, true, "default"},
		{"explicit network false", map[string][]string{"unconfined-network": {"false"}}, []string{"codex", "github", "yolo.agent.observe-codex", "yolo.space.operator", "yolo.chat.skill"}, false, "default"},
		{"merged features", map[string][]string{"features": {"go", "yolo.space.operator", "go"}, "feature": {"yolo.chat.skill", "go", "python-3.12"}}, []string{"go", "yolo.space.operator", "yolo.chat.skill", "python-3.12"}, true, "default"},
		{"workdir override", nil, []string{"codex", "github", "yolo.agent.observe-codex", "yolo.space.operator", "yolo.chat.skill"}, true, "override"},
		{"ephemeral override", map[string][]string{"workdir-mode": {"ephemeral"}}, []string{"codex", "github", "yolo.agent.observe-codex", "yolo.space.operator", "yolo.chat.skill"}, true, "ephemeral"},
		{"disabled", map[string][]string{"operator": {"false"}}, []string{"codex", "github", "yolo.agent.observe-codex"}, false, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			stubStoppedSpaceResolver(t)
			defaultDir, overrideDir := t.TempDir(), t.TempDir()
			saveSpaceForTest(t, "default", defaultDir)
			if err := account.New("default", "personal").Save(); err != nil {
				t.Fatal(err)
			}
			flags := map[string][]string{"agent": {"codex"}, "account": {"personal"}, "operator": {"true"}, "manifest-only": {"true"}}
			maps.Copy(flags, tc.flags)
			if tc.workdir == "override" {
				flags["workdir"] = []string{overrideDir}
			}
			root := newRootCmd()
			argv, err := controllerCommandArgs(root, controller.Request{ID: "test-request-0123456789", Method: "box.create", Space: "default", Args: []string{"coordinator"}, Dash: -1, Flags: flags})
			if err != nil {
				t.Fatal(err)
			}
			if _, stderr, err := execCmd(t, root, "", argv...); err != nil {
				t.Fatalf("create: %v; %s", err, stderr)
			}
			cfg, err := agentbox.Load("default", "coordinator")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.Features, tc.features) || cfg.UnconfinedNetwork != tc.unconfined {
				t.Fatalf("features=%v network=%v, want %v / %v", cfg.Features, cfg.UnconfinedNetwork, tc.features, tc.unconfined)
			}
			if cfg.Agent != "codex" || cfg.Account != "personal" || len(cfg.SessionCommand) != 0 {
				t.Fatalf("preset changed agent/account/session: %+v", cfg)
			}
			if enabled, err := agentbox.OperatorEnabled(cfg); err != nil || enabled != (tc.name != "disabled") {
				t.Fatalf("operator grant=%v, %v", enabled, err)
			}
			if tc.workdir == "ephemeral" {
				if cfg.Workdir != agentbox.NewEphemeralWorkdir("coordinator") {
					t.Fatalf("workdir=%+v", cfg.Workdir)
				}
			} else {
				want := defaultDir
				if tc.workdir == "override" {
					want = overrideDir
				}
				canonical, _, err := agentbox.CanonicalizeWorkdir(want)
				if err != nil || !cfg.Workdir.IsHost() || cfg.Workdir.Canonical != canonical {
					t.Fatalf("workdir=%+v, want host %s: %v", cfg.Workdir, canonical, err)
				}
			}
			raw, err := os.ReadFile(cfg.Path())
			if err != nil || strings.Contains(string(raw), "\noperator:") {
				t.Fatalf("manifest must store expanded settings only: %s, %v", raw, err)
			}
		})
	}
}

func TestBoxCreateOperatorPreservesValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubStoppedSpaceResolver(t)
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--agent", "codex", "--account", "none"}, "real agents require real accounts"},
		{[]string{"--agent", "codex", "--account", "missing"}, "does not exist"},
		{[]string{"--agent", "unknown", "--account", "personal"}, "unknown agent"},
		{[]string{"--agent", "codex", "--account", "personal", "--features", "unknown-feature"}, "unknown feature"},
		{[]string{"--agent", "codex", "--account", "personal", "--workdir", "/unused"}, "incompatible"},
	} {
		args := append([]string{"coordinator", "--operator", "--manifest-only", "--workdir-mode", "ephemeral"}, tc.args...)
		if _, _, err := execCmd(t, newAgentboxCreateCmd(), "", args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: %v, want %q", args, err, tc.want)
		}
		if boxManifestExists(t, "default", "coordinator") {
			t.Fatal("invalid create persisted a declaration")
		}
	}
}

func TestOperatorPresetFlagCompletionAndScope(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, factory := range []func() *cobra.Command{newRootCmd, newClientRootCmd} {
		root := factory()
		for _, action := range []string{"set", "copy"} {
			if findByPath(root, "box", action).Flag("operator") != nil {
				t.Fatalf("box %s unexpectedly accepts --operator", action)
			}
		}
		root.SetErr(io.Discard)
		out, _, err := execCmd(t, root, "", "__complete", "box", "create", "coordinator", "--op")
		if err != nil || !strings.Contains(out, "--operator\t") {
			t.Fatalf("Cobra/palette flag completion: %q, %v", out, err)
		}
	}
}
