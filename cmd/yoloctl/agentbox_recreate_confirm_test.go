package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/spf13/cobra"
)

// An unconfirmed `box recreate` (no answer, as without a terminal, or a
// declined prompt) changes nothing and fails as a user error, like the other
// confirmed commands. A confirmed one, by prompt or --force, goes on to the
// recreate work; here an unknown capability stops it at the policy step,
// before any VM work, which proves it passed the confirmation.
func TestBoxRecreateFailsUnlessConfirmed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, "no.such.capability")
	before := readManifest(t, path)
	run := func(input string, force bool) (string, error) {
		t.Helper()
		var errw bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader(input))
		cmd.SetErr(&errw)
		err := recreateAgentbox(cmd, "default", "coder", force, true, true, nil)
		return errw.String(), err
	}
	for _, tc := range []struct {
		name, input string
	}{
		{"no answer", ""},
		{"declined", "n\n"},
		{"anything but yes", "maybe\n"},
	} {
		out, err := run(tc.input, false)
		if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "recreate not confirmed") || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("%s: err %v, want a user error naming --force", tc.name, err)
		}
		if !strings.Contains(out, "continue? [y/N]") {
			t.Fatalf("%s: no prompt: %q", tc.name, out)
		}
		if readManifest(t, path) != before {
			t.Fatalf("%s: the manifest changed", tc.name)
		}
	}
	for _, tc := range []struct {
		name, input string
		force       bool
	}{
		{"accepted", "y\n", false},
		{"accepted in full", "YES\n", false},
		{"forced", "", true},
	} {
		out, err := run(tc.input, tc.force)
		if err == nil || strings.Contains(err.Error(), "not confirmed") || !strings.Contains(err.Error(), "compose policy") {
			t.Fatalf("%s: err %v, want it past the confirmation", tc.name, err)
		}
		if tc.force == strings.Contains(out, "continue? [y/N]") {
			t.Fatalf("%s: prompt shown %v with force %v", tc.name, !tc.force, tc.force)
		}
	}
	if _, err := agentbox.Load("default", "coder"); err != nil {
		t.Fatal(err)
	}
}
