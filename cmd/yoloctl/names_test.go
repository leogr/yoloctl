package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", `''`},
		{"hello", `'hello'`},
		{"with space", `'with space'`},
		{"with'quote", `'with'\''quote'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
		{"`whoami`", "'`whoami`'"},
		{"a;b|c&d", `'a;b|c&d'`},
		{"'", `''\'''`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

// TestShellQuote_RoundTripThroughSh verifies that /bin/sh -c sees the
// quoted form as a single literal argument, which is the property the
// ProxyCommand call sites rely on.
func TestShellQuote_RoundTripThroughSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	inputs := []string{
		"plain",
		"with space",
		"with'quote",
		"a;b|c&d",
		"$(touch /tmp/yoloctl-test-injected)",
		"`echo injected`",
	}
	for _, in := range inputs {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil {
			t.Errorf("sh -c with %q: %v", in, err)
			continue
		}
		if got := string(out); got != in {
			t.Errorf("round-trip %q: got %q (sh re-interpreted)", in, got)
		}
		if strings.Contains(in, "yoloctl-test-injected") {
			// Belt-and-braces: make sure the command substitution didn't fire.
			if _, statErr := exec.Command("test", "-e", "/tmp/yoloctl-test-injected").CombinedOutput(); statErr == nil {
				t.Errorf("command substitution from %q leaked through quoting", in)
			}
		}
	}
}

func TestRemoteCommandStringPreservesArgBoundaries(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	cmd := remoteCommandString([]string{"sh", "-lc", "printf %s exec-ok"})
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", cmd, err)
	}
	if got := string(out); got != "exec-ok" {
		t.Fatalf("remote command output: got %q want %q", got, "exec-ok")
	}
}

func TestRemoteCommandStringQuotesEveryArg(t *testing.T) {
	got := remoteCommandString([]string{"sh", "-lc", "echo exec-ok", "with'quote"})
	want := "'sh' '-lc' 'echo exec-ok' 'with'\\''quote'"
	if got != want {
		t.Fatalf("remoteCommandString() got %q want %q", got, want)
	}
}

func TestBoxNameRefusedBeforeControllerOrPersistence(t *testing.T) {
	// Any execution beyond Args would try to select host/operator authority.
	previous := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) {
		t.Fatal("invalid box arguments reached controller selection")
		return nil, nil
	}
	t.Cleanup(func() { loadOperatorClient = previous })

	for _, rootCase := range []struct {
		name    string
		newRoot func() *cobra.Command
	}{
		{"public", newClientRootCmd},
		{"worker", newRootCmd},
	} {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"box", "create", "a"}, `invalid agentbox name "a"`},
			{[]string{"box", "create", "7", "--manifest-only"}, `invalid agentbox name "7"`},
			{[]string{"box", "copy", "source", "a"}, `invalid agentbox name "a"`},
			{[]string{"box", "copy", "source", "7", "--manifest-only"}, `invalid agentbox name "7"`},
			{[]string{"box", "copy", "a", "target"}, `invalid agentbox name "a"`},
			{[]string{"box", "create"}, "accepts 1 arg(s), received 0"},
			{[]string{"box", "create", "a", "extra"}, "accepts 1 arg(s), received 2"},
			{[]string{"box", "copy"}, "accepts 2 arg(s), received 0"},
			{[]string{"box", "copy", "a"}, "accepts 2 arg(s), received 1"},
			{[]string{"box", "copy", "source", "a", "extra"}, "accepts 2 arg(s), received 3"},
		} {
			t.Run(rootCase.name+"/"+strings.Join(tc.args, " "), func(t *testing.T) {
				config, cache := t.TempDir(), t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", config)
				t.Setenv("XDG_CACHE_HOME", cache)
				t.Setenv("PATH", t.TempDir()) // No external VM/runtime tools.
				_, _, err := execCmd(t, rootCase.newRoot(), "", tc.args...)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q before runtime access, got %v", tc.want, err)
				}
				if strings.HasPrefix(tc.want, "invalid") && !IsUserError(err) {
					t.Fatalf("invalid box name must be a user error: %v", err)
				}
				for _, dir := range []string{config, cache} {
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Fatalf("invalid arguments changed %s: %v, %v", dir, entries, err)
					}
				}
			})
		}
	}
}
