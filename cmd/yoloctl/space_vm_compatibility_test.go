package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

func TestVMCompatibilityScriptsCancellationAndUnknownErrorsNeverRecreate(t *testing.T) {
	old := recreateCompatibleSpace
	t.Cleanup(func() { recreateCompatibleSpace = old })
	recreateCompatibleSpace = func(*cobra.Command, *spaceconfig.Bootstrap) error { t.Fatal("unapproved recreation"); return nil }
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: "lima"}}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetErr(io.Discard)
	cause := asUserError(&spaceVMCompatibilityError{Space: boot.Name, Backend: "lima"})
	if got := offerSpaceVMRecreate(cmd, boot, cause); got != cause || !strings.Contains(got.Error(), "yoloctl space recreate fixture") {
		t.Fatal("script lost actionable unchanged error", got)
	}
	unknown := errors.New("lost transport reply")
	if got := offerSpaceVMRecreate(cmd, boot, unknown); got != unknown {
		t.Fatal("unknown reply became recreate/replay", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd.SetContext(ctx)
	if got := offerSpaceVMRecreate(cmd, boot, cause); !errors.Is(got, context.Canceled) {
		t.Fatal(got)
	}
}
func TestVMCompatibilityRechecksOnceWithoutReplayingOrPromptLoop(t *testing.T) {
	boot := imageUpdateFixture(t)
	boot.VM.Backend = "lima"
	boot.VM.Image = ""
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	oldCheck, oldRecreate, oldTTY := checkCommandVMCompatibility, recreateCompatibleSpace, stdinIsTerminalFn
	t.Cleanup(func() {
		checkCommandVMCompatibility = oldCheck
		recreateCompatibleSpace = oldRecreate
		stdinIsTerminalFn = oldTTY
	})
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	stdinIsTerminalFn = func(io.Reader) bool { return true }
	reads, recreates := 0, 0
	mismatch := asUserError(&spaceVMCompatibilityError{Space: boot.Name, Backend: "lima"})
	checkCommandVMCompatibility = func(context.Context, *spaceconfig.Bootstrap) error { reads++; return mismatch }
	recreateCompatibleSpace = func(*cobra.Command, *spaceconfig.Bootstrap) error { recreates++; return nil }
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader("y\ny\n"))
	cmd.SetErr(slave)
	if err := ensureCommandVMCompatibility(cmd, boot.Name); err != mismatch || reads != 2 || recreates != 1 {
		t.Fatal("recreate retry/prompt loop or lost refusal", err, reads, recreates)
	}
}
func TestVMCompatibilityTargetsOnlyRuntimeMutationCommands(t *testing.T) {
	root := newRootCmd()
	for _, tc := range []struct {
		args []string
		want bool
	}{{[]string{"box", "create"}, true}, {[]string{"box", "recreate"}, true}, {[]string{"box", "start"}, true}, {[]string{"box", "list"}, false}, {[]string{"box", "inspect"}, false}} {
		cmd, _, err := root.Find(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if commandNeedsVMCompatibility(cmd) != tc.want {
			t.Fatalf("wrong pre-effect route: %v", tc.args)
		}
	}
	cmd, _, _ := root.Find([]string{"box", "create"})
	if err := cmd.Flags().Set("manifest-only", "true"); err != nil {
		t.Fatal(err)
	}
	if commandNeedsVMCompatibility(cmd) {
		t.Fatal("manifest-only requested a VM recreation")
	}
}
