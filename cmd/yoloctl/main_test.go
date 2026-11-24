package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestUnknownCommandGroupSubcommandsFail(t *testing.T) {
	for _, args := range [][]string{
		{"port", "stop", "coder"}, // removed command must not silently succeed
		{"box", "bogus"},
		{"account", "secret", "bogus"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := execCmd(t, newRootCmd(), "", args...)
			if err == nil || !strings.Contains(err.Error(), "unknown command") || !IsUserError(err) {
				t.Fatalf("%q error = %v, want an unknown-command user error", args, err)
			}
			if code := classifyError(err); code != exitUser {
				t.Errorf("%q exit code = %d, want %d", args, code, exitUser)
			}
		})
	}

	out, _, err := execCmd(t, newRootCmd(), "", "port")
	if err != nil || !strings.Contains(out, "Available Commands:") {
		t.Errorf("bare port group should still show help: output %q, error %v", out, err)
	}
}

func TestDescribeSpaceFlagHelpParity(t *testing.T) {
	for _, root := range []struct {
		name string
		make func() *cobra.Command
	}{
		{"client", newClientRootCmd},
		{"worker", newRootCmd},
	} {
		t.Run(root.name, func(t *testing.T) {
			want, got := root.make(), root.make()
			describeSpaceFlagHelpEager(want)
			describeSpaceFlagHelp(got)
			compareSpaceFlagHelp(t, want, got)

			for _, args := range [][]string{
				{"--help"},
				{"box", "create", "--space", "example", "--help"},
				{"account", "list", "--space", "example", "--help"},
			} {
				want, got := root.make(), root.make()
				describeSpaceFlagHelpEager(want)
				describeSpaceFlagHelp(got)
				wantOut, wantErrOut, wantErr := execCmd(t, want, "", args...)
				gotOut, gotErrOut, gotErr := execCmd(t, got, "", args...)
				if wantErr != nil || gotErr != nil || gotOut != wantOut || gotErrOut != wantErrOut {
					t.Errorf("%q help changed:\nwant stdout %q, stderr %q, err %v\ngot stdout %q, stderr %q, err %v", args,
						wantOut, wantErrOut, wantErr, gotOut, gotErrOut, gotErr)
				}
			}
		})
	}
	t.Run("unmerged persistent flag", func(t *testing.T) {
		makeRoot := func() *cobra.Command {
			root := &cobra.Command{Use: "root"}
			root.PersistentFlags().String("space", defaultSpaceName, "Root space")
			root.AddCommand(&cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}})
			return root
		}
		want, got := makeRoot(), makeRoot()
		describeSpaceFlagHelpEager(want)
		describeSpaceFlagHelp(got)
		compareSpaceFlagHelp(t, want, got)
	})
}

// Keep the eager Cobra path as a reference when upgrading Cobra.
func describeSpaceFlagHelpEager(cmd *cobra.Command) {
	if f := cmd.LocalFlags().Lookup("space"); f != nil && f.DefValue == defaultSpaceName {
		f.DefValue = ""
		f.Usage += " (omit to use the current space selection)"
	}
	for _, child := range cmd.Commands() {
		describeSpaceFlagHelpEager(child)
	}
}

func compareSpaceFlagHelp(t *testing.T, want, got *cobra.Command) {
	t.Helper()
	path := want.CommandPath()
	if got.CommandPath() != path {
		t.Fatalf("command %q, want %q", got.CommandPath(), path)
	}
	var wantHelp, gotHelp bytes.Buffer
	want.SetOut(&wantHelp)
	got.SetOut(&gotHelp)
	if err := want.Help(); err != nil {
		t.Fatal(err)
	}
	if err := got.Help(); err != nil {
		t.Fatal(err)
	}
	if gotHelp.String() != wantHelp.String() {
		t.Errorf("%s help changed:\nwant:\n%s\ngot:\n%s", path, wantHelp.String(), gotHelp.String())
	}
	if wantUsage, gotUsage := want.UsageString(), got.UsageString(); gotUsage != wantUsage {
		t.Errorf("%s usage changed:\nwant:\n%s\ngot:\n%s", path, wantUsage, gotUsage)
	}
	for _, flags := range []struct {
		kind      string
		want, got *pflag.FlagSet
	}{
		{"local", want.LocalFlags(), got.LocalFlags()},
		{"inherited", want.InheritedFlags(), got.InheritedFlags()},
	} {
		before, after := helpFlagStates(flags.want), helpFlagStates(flags.got)
		if !slices.Equal(before, after) {
			t.Errorf("%s %s flags changed:\nwant: %+v\ngot: %+v", path, flags.kind, before, after)
		}
	}
	wantChildren, gotChildren := want.Commands(), got.Commands()
	if len(gotChildren) != len(wantChildren) {
		t.Fatalf("%s children: got %d, want %d", path, len(gotChildren), len(wantChildren))
	}
	for i, child := range wantChildren {
		compareSpaceFlagHelp(t, child, gotChildren[i])
	}
}

type helpFlagState struct {
	Name, Usage, Default, Value string
	Changed                     bool
}

func helpFlagStates(flags *pflag.FlagSet) []helpFlagState {
	var states []helpFlagState
	flags.VisitAll(func(f *pflag.Flag) {
		states = append(states, helpFlagState{f.Name, f.Usage, f.DefValue, f.Value.String(), f.Changed})
	})
	return states
}
