package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Directive 56: each one-box command prints the warning once, whatever its
// result; recreate and destroy print none; an unreadable box prints nothing.
func TestStartModeWarningOncePerOneBoxCommand(t *testing.T) {
	previousActive, previousFor := startModeWarningsActive, startModeWarningForFn
	t.Cleanup(func() { startModeWarningsActive, startModeWarningForFn = previousActive, previousFor })
	startModeWarningsActive = func() bool { return true }
	message := "box coder was created before this version; recreate it (box recreate coder): data stored only inside the container is lost"
	readable := true
	startModeWarningForFn = func(*cobra.Command, string) string {
		if !readable {
			return ""
		}
		return message
	}
	failure := errors.New("start failed: something")
	build := func(sub string, run func(*cobra.Command) error) *cobra.Command {
		root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
		box := &cobra.Command{Use: "box"}
		cmd := &cobra.Command{Use: sub, RunE: func(cmd *cobra.Command, _ []string) error { return run(cmd) }}
		cmd.Flags().String("space", "work", "")
		box.AddCommand(cmd)
		root.AddCommand(box)
		registerStartModeWarnings(root)
		return root
	}
	for _, tc := range []struct {
		sub      string
		run      func(*cobra.Command) error
		readable bool
		want     int
		err      error
	}{
		{"start", func(*cobra.Command) error { return nil }, true, 1, nil},
		{"stop", func(*cobra.Command) error { return failure }, true, 1, failure},
		// The session start already warned through the same marker.
		{"attach", func(cmd *cobra.Command) error { warnStartModeOnce(cmd.Context(), message); return nil }, true, 1, nil},
		{"recreate", func(*cobra.Command) error { return nil }, true, 0, nil},
		{"destroy", func(*cobra.Command) error { return nil }, true, 0, nil},
		{"exec", func(*cobra.Command) error { return nil }, false, 0, nil},
	} {
		var out strings.Builder
		restore := bindCLITestLogger(t, &out)
		readable = tc.readable
		root := build(tc.sub, tc.run)
		root.SetArgs([]string{"box", tc.sub, "coder", "--space", "work"})
		err := root.ExecuteContext(t.Context())
		restore()
		if !errors.Is(err, tc.err) || strings.Count(out.String(), "created before this version") != tc.want {
			t.Fatalf("%s: err %v, output:\n%s", tc.sub, err, out.String())
		}
	}
}
