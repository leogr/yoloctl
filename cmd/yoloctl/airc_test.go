package main

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/spf13/cobra"
)

func TestAgentlessSessionCommandPersistsAndResolves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := flagsTestOpts("room")
	opts.Agent, opts.Account = "none", "none"
	opts.SessionCommand = []string{"aircd", "serve", "--config", "/workdir/chat with spaces,commas/server.toml"}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentbox.Load(cfg.Space, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := resolveSessionArgv(cfg.Space, cfg)
	if err != nil || !slices.Equal(argv, opts.SessionCommand) {
		t.Fatalf("resolved = %q, %v", argv, err)
	}
	argv[0] = "changed"
	if cfg.SessionCommand[0] != "aircd" {
		t.Fatal("resolver aliases declaration argv")
	}
	if got := buildInspectManifest(cfg).SessionCommand; !slices.Equal(got, opts.SessionCommand) {
		t.Fatalf("inspect = %q", got)
	}

}

func TestSessionCommandFlagsPreserveArgv(t *testing.T) {
	for _, cmd := range []*cobra.Command{newAgentboxCreateCmd(), newAgentboxCopyCmd(), newAgentboxSetCmd()} {
		if err := cmd.ParseFlags([]string{"--session-command=printf", "--session-command=a,b", "--session-command="}); err != nil {
			t.Fatal(err)
		}
		got, err := cmd.Flags().GetStringArray("session-command")
		if err != nil || !slices.Equal(got, []string{"printf", "a,b", ""}) {
			t.Fatalf("%s argv = %q, %v", cmd.Name(), got, err)
		}
	}
}

func TestBoxSetSessionCommandAndClear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedSetBox(t)
	calls := stubSetReconcile(t)
	command := []string{"aircd", "serve", "--config", ".airc/server.toml"}
	var out bytes.Buffer
	for _, want := range [][]string{command, {""}} {
		if err := runSet(context.Background(), &out, "default", "coder", setOptions{SessionCommand: &want}); err != nil {
			t.Fatal(err)
		}
		cfg, err := agentbox.Load("default", "coder")
		if err != nil {
			t.Fatal(err)
		}
		if len(want) == 1 {
			want = nil
		}
		if !slices.Equal(cfg.SessionCommand, want) {
			t.Fatalf("saved = %q, want %q", cfg.SessionCommand, want)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("reconcile calls = %d", len(*calls))
	}
}

func TestCopyAgentlessSessionCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())
	src.Agent, src.Account, src.AgentExtraArgs = "none", "none", nil
	src.SessionCommand = []string{"aircd", "serve", "--config", ".airc/server.toml"}
	opts, _, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, src, "default", "dst")
	if err != nil || !slices.Equal(opts.SessionCommand, src.SessionCommand) {
		t.Fatalf("copy = %q, %v", opts.SessionCommand, err)
	}
	opts.SessionCommand[0] = "changed"
	if src.SessionCommand[0] != "aircd" {
		t.Fatal("copy aliases source")
	}
	opts, _, err = copyOptionsFromSource(copyChanged("agent", "account"), agentboxCreateOptions{Agent: "claude", Account: "default"}, src, "default", "dst")
	if err != nil || len(opts.SessionCommand) != 0 {
		t.Fatalf("agent conversion = %q, %v", opts.SessionCommand, err)
	}
}
