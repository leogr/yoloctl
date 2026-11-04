package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// hostCompletion runs the host client's hidden __complete command, exactly as
// a shell or the TUI palette does, with no in-box operator identity.
func hostCompletion(t *testing.T, args ...string) []string {
	t.Helper()
	previous := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) { return nil, nil }
	t.Cleanup(func() { loadOperatorClient = previous })
	root := newClientRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	return lines
}

// A persistent --space (chat, account) is declared once on the parent and is
// absent from Flags() until Cobra merges it, so the generic flag walk used to
// skip it: `yoloctl chat --space <TAB>` offered nothing.
func TestPersistentSpaceFlagCompletesConfiguredSpaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", t.TempDir())
	saveSpaceForTest(t, "beta", t.TempDir())
	want := []string{"alpha", "beta", fmt.Sprintf(":%d", cobra.ShellCompDirectiveNoFileComp)}
	for _, args := range [][]string{
		{"chat", "--space", ""},
		{"chat", "status", "--space", ""},
		{"chat", "enroll", "--space", ""},
		{"chat", "channel", "add", "--space", ""},
		{"account", "list", "--space", ""},
		{"account", "auth", "import", "--space", ""},
	} {
		if got := hostCompletion(t, args...); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%v = %q, want %q", args, got, want)
		}
	}
}

func TestBackupAndObservePositionalsComplete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", t.TempDir())
	spaces := []string{"alpha", fmt.Sprintf(":%d", cobra.ShellCompDirectiveNoFileComp)}
	for _, args := range [][]string{
		{"space", "export", ""},
		{"space", "operation", ""},
	} {
		if got := hostCompletion(t, args...); strings.Join(got, "\n") != strings.Join(spaces, "\n") {
			t.Errorf("%v = %q, want %q", args, got, spaces)
		}
	}
	// import names a NEW space: no existing names and no files.
	if got := hostCompletion(t, "space", "import", ""); len(got) != 1 || got[0] != fmt.Sprintf(":%d", cobra.ShellCompDirectiveNoFileComp) {
		t.Errorf("space import NEW name = %q, want nothing", got)
	}
	for _, path := range [][]string{{"space", "export"}, {"space", "import"}, {"space", "operation"}, {"account", "observe"}} {
		cmd, _, err := newClientRootCmd().Find(path)
		if err != nil || cmd.ValidArgsFunction == nil {
			t.Errorf("%v: no positional completer wired (%v)", path, err)
		}
	}
}

// Every flag that names an existing space/box/account/agent/feature/capability,
// on every command of the host client tree, has a value completer; free-form
// name flags suppress file suggestions instead of listing host files.
func TestEveryEntityFlagHasACompleter(t *testing.T) {
	entity := map[string]bool{
		"space": true, "from-space": true, "agent": true, "account": true, "agentbox": true,
		"feature": true, "features": true, "feature-option": true, "capability": true, "drop-capability": true,
		"box": true, "human": true, "client": true,
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if !entity[f.Name] || f.Hidden {
				return
			}
			if _, ok := c.GetFlagCompletionFunc(f.Name); !ok {
				t.Errorf("%s --%s: no completer wired", c.CommandPath(), f.Name)
			}
		}
		c.LocalFlags().VisitAll(check)
		c.InheritedFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newClientRootCmd())
}

// Every runnable command whose usage starts with a positional placeholder has
// a positional completer (a NEW-name positional uses completeNoArgs).
func TestEveryPositionalCommandHasACompleter(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		fields := strings.Fields(c.Use)
		if c.Runnable() && len(fields) > 1 {
			first := strings.TrimLeft(fields[1], "[")
			if first != "" && first[0] >= 'A' && first[0] <= 'Z' && c.ValidArgsFunction == nil && len(c.ValidArgs) == 0 {
				t.Errorf("%s (%s): no positional completer wired", c.CommandPath(), c.Use)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newClientRootCmd())
}
