package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/spf13/cobra"
)

func followSeams(t *testing.T) {
	t.Helper()
	oldHost, oldInteractive, oldRun, oldDefault, oldGrants, oldCommand := followHostAllowed, followInteractive, followRun, followDefault, followGrants, followCommand
	t.Cleanup(func() {
		followHostAllowed, followInteractive, followRun, followDefault, followGrants, followCommand = oldHost, oldInteractive, oldRun, oldDefault, oldGrants, oldCommand
	})
	followHostAllowed = func() error { return nil }
	followInteractive = func(*cobra.Command) bool { return true }
	t.Setenv("YOLOCTL_HOME", t.TempDir())
}
func executeFollow(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(t.Context())
}
func TestFollowPinsRelativeVMNameAndSpaceAtLaunch(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	var selected followview.Options
	ctx, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	ctx.Space = "first"
	if err := ctx.Save(); err != nil {
		t.Fatal(err)
	}
	followDefault = func(_ context.Context, space string) (string, error) {
		if space != "first" {
			t.Fatal("wrong launch space", space)
		}
		return root, nil
	}
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	if err := executeFollow(t, "notes.md"); err != nil {
		t.Fatal(err)
	}
	if selected.Path != filepath.Join(root, "notes.md") || selected.Space != "first" || selected.Format != followview.Markdown {
		t.Fatal("target not pinned", selected)
	}
	ctx.Space = "second"
	if err := ctx.Save(); err != nil {
		t.Fatal(err)
	}
	var readSpace string
	followGrants = func(_ context.Context, space string) ([]string, error) {
		readSpace = space
		return nil, errors.New("grant removed")
	}
	if _, err := selected.Reader.Read(t.Context(), followview.Query{}); err == nil || readSpace != "first" {
		t.Fatal("changed context redirected the view", readSpace, err)
	}
}
func TestHostFollowNeverResolvesASpaceOrVM(t *testing.T) {
	followSeams(t)
	var selected followview.Options
	followDefault = func(context.Context, string) (string, error) { t.Fatal("host read default workdir"); return "", nil }
	followGrants = func(context.Context, string) ([]string, error) { t.Fatal("host read grants"); return nil, nil }
	followCommand = func(context.Context, string, ...string) *exec.Cmd { t.Fatal("host opened VM transport"); return nil }
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executeFollow(t, "--host", path); err != nil {
		t.Fatal(err)
	}
	if selected.Space != "" || selected.Path != path {
		t.Fatal(selected)
	}
	s, err := selected.Reader.Read(t.Context(), followview.Query{})
	if err != nil || string(s.Data) != "host" {
		t.Fatal(s, err)
	}
	if !readOnlyStartup([]string{"follow", "--host", path}) {
		t.Fatal("follow initialized VM home before host mode")
	}
}
func TestFollowValidationPrecedesReaderAndHostEffects(t *testing.T) {
	followSeams(t)
	followHostAllowed = func() error { t.Fatal("invalid argv reached host/controller selection"); return nil }
	for _, args := range [][]string{{"--host", "--space", "s", "file"}, {"--format", "html", "file"}, {}, {"one", "two"}, {""}} {
		if err := executeFollow(t, args...); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
}
func TestFollowCompletionUsesVMListAndNeverHostFallback(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guest-not-really-host"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	followGrants = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	followCommand = func(ctx context.Context, space string, args ...string) *exec.Cmd {
		if args[0] != "python3" || len(args) != 3 || args[1] != "-c" {
			t.Fatal("not a fixed script request", args)
		}
		return exec.CommandContext(ctx, "python3", "-c", `print('{"entries":["guest-only"]}')`)
	}
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	if err := cmd.Flags().Set("space", "fixed"); err != nil {
		t.Fatal(err)
	}
	out, directive := cmd.ValidArgsFunction(cmd, nil, root+"/g")
	if len(out) != 1 || out[0] != root+"/guest-only" || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("not VM-based completion", out, directive)
	}
	followGrants = func(context.Context, string) ([]string, error) { return nil, errors.New("VM stopped") }
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/g")
	if len(out) != 0 || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("unavailable VM fell back to host", out, directive)
	}
}
func TestFollowCompletionSeedsOnlyCurrentGrantsAndHostIsExplicit(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	followGrants = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	out, directive := cmd.ValidArgsFunction(cmd, nil, "")
	if len(out) != 1 || !strings.HasPrefix(out[0], root+"/") || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal(out, directive)
	}
	if err := os.WriteFile(filepath.Join(root, "host-only"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("host", "true"); err != nil {
		t.Fatal(err)
	}
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/host-")
	if len(out) != 1 || out[0] != root+"/host-only" || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal(out, directive)
	}
}
func TestFollowIsHostOnlyAndItsFlagsHaveCompleters(t *testing.T) {
	followSeams(t)
	root := newClientRootCmd()
	cmd, _, err := root.Find([]string{"follow"})
	if err != nil || cmd.Name() != "follow" {
		t.Fatal(err)
	}
	for _, flag := range []string{"space", "format"} {
		if _, ok := cmd.GetFlagCompletionFunc(flag); !ok {
			t.Fatal("missing flag completer", flag)
		}
	}
	worker := newRootCmd()
	if cmd, _, _ := worker.Find([]string{"follow"}); cmd != worker {
		t.Fatal("native worker exposed follow")
	}
	followHostAllowed = func() error { return errors.New("not a host") }
	followRun = func(context.Context, followview.Options) error { t.Fatal("host refusal bypassed"); return nil }
	if err := executeFollow(t, "--host", "file"); err == nil || !strings.Contains(err.Error(), "not a host") {
		t.Fatal(err)
	}
	// Help is available without a VM or interactive terminal.
	cmd = newFollowCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), "1 MiB") {
		t.Fatal(err, buf.String())
	}
}
