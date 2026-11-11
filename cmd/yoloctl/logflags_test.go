package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestLogSettingsFlagsWinOverEnvironment(t *testing.T) {
	for _, c := range []struct {
		args  []string
		env   map[string]string
		want  prettylog.Verbosity
		color prettylog.ColorMode
		fail  bool
	}{
		{nil, nil, prettylog.Normal, prettylog.ColorAuto, false},
		{[]string{"-q"}, map[string]string{"YOLOCTL_LOG": "debug"}, prettylog.Quiet, prettylog.ColorAuto, false},
		{[]string{"--log-level=debug"}, nil, prettylog.Trace, prettylog.ColorAuto, false},
		{[]string{"--log-level", "info"}, map[string]string{"YOLOCTL_LOG": "error"}, prettylog.Normal, prettylog.ColorAuto, false},
		{[]string{"--log-level=warn", "-q"}, nil, prettylog.Quiet, prettylog.ColorAuto, false},
		{[]string{"--log-level=error", "--color=never"}, nil, prettylog.Errors, prettylog.ColorNever, false},
		{nil, map[string]string{"YOLOCTL_LOG": "warn"}, prettylog.Quiet, prettylog.ColorAuto, false},
		{nil, map[string]string{"YOLOCTL_DEBUG": "1"}, prettylog.Trace, prettylog.ColorAuto, false},
		{[]string{"-q", "--log-level=debug"}, nil, 0, 0, true},
		{[]string{"--log-level=trace"}, nil, 0, 0, true},
		{[]string{"--color=sometimes"}, nil, 0, 0, true},
	} {
		root := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		addLogFlags(root)
		if err := root.ParseFlags(c.args); err != nil {
			t.Fatal(err)
		}
		s, err := logSettings(root.Flags(), func(k string) string { return c.env[k] })
		if (err != nil) != c.fail {
			t.Fatalf("%v: err %v", c.args, err)
		}
		if !c.fail && (s.Verbosity != c.want || s.Color != c.color) {
			t.Errorf("%v %v: got %+v", c.args, c.env, s)
		}
	}
}

func TestShortVKeepsPrintingTheVersion(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs([]string{arg})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), version) {
			t.Fatalf("%s: output %q", arg, out.String())
		}
	}
	root := newRootCmd()
	root.InitDefaultVersionFlag()
	if f := root.Flags().ShorthandLookup("v"); f == nil || f.Name != "version" {
		t.Fatalf("-v is %v", f)
	}
}

func TestExecuteRootRendersUnsilencedErrorsOnce(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		name     string
		silenced bool
		want     string
	}{
		{"shown", false, "Error: boom\n"},
		{"silenced", true, ""},
	} {
		root := &cobra.Command{Use: "x"}
		leaf := &cobra.Command{Use: "leaf", RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceErrors = c.silenced
			return boom
		}}
		root.AddCommand(leaf)
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetArgs([]string{"leaf"})
		err := executeRoot(root, root.ExecuteC)
		if !errors.Is(err, boom) || errOut.String() != c.want {
			t.Errorf("%s: err %v output %q", c.name, err, errOut.String())
		}
	}
	// An unknown command is reported by the root itself.
	root := &cobra.Command{Use: "x"}
	root.AddCommand(&cobra.Command{Use: "leaf", Run: func(*cobra.Command, []string) {}})
	var errOut bytes.Buffer
	root.SetErr(&errOut)
	root.SetArgs([]string{"nope"})
	if err := executeRoot(root, root.ExecuteC); err == nil || !strings.HasPrefix(errOut.String(), "Error: unknown command") {
		t.Fatalf("err %v output %q", err, errOut.String())
	}
}
