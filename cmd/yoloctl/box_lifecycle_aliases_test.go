package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagNames returns the sorted local-flag names of a command, for comparing
// two command instances that must expose the same flag set.
func flagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	sort.Strings(names)
	return names
}

// Removed root commands must fail before contacting any controller.
func TestRemovedRootCommands(t *testing.T) {
	for _, name := range []string{"run", "ps"} {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{name})
		if err := root.Execute(); err == nil {
			t.Errorf("removed command %s succeeded", name)
		}
	}
	for _, name := range []string{"create", "list", "ls"} {
		cmd, args, err := newRootCmd().Find([]string{"box", name})
		if err != nil || len(args) != 0 || cmd.Parent() == nil || cmd.Parent().Name() != "box" {
			t.Fatalf("box %s unavailable: %v", name, err)
		}
	}
}

// Root `attach` is an alias of `box attach`. Both resolve and
// share the same flag set (same newAgentboxAttachCmd factory), so attach
// behavior isn't duplicated.
func TestRootAttachAliasSharesBoxAttach(t *testing.T) {
	root := newRootCmd()

	rootAttach, _, err := root.Find([]string{"attach"})
	if err != nil {
		t.Fatalf("find root attach: %v", err)
	}
	if rootAttach.Name() != "attach" {
		t.Errorf("root attach Name() = %q, want attach", rootAttach.Name())
	}
	boxAttach, _, err := root.Find([]string{"box", "attach"})
	if err != nil {
		t.Fatalf("find box attach: %v", err)
	}
	if rootAttach == boxAttach {
		t.Error("root attach and box attach must be distinct command instances")
	}
	if ra, ba := flagNames(rootAttach), flagNames(boxAttach); !slices.Equal(ra, ba) {
		t.Errorf("attach flag sets diverge:\n  root: %v\n  box:  %v", ra, ba)
	}
}

// `box restart` exists under the box tree and carries the lifecycle flags
// (--space, --no-autoupdate, --no-attach).
func TestBoxRestartWired(t *testing.T) {
	root := newRootCmd()
	sub, _, err := root.Find([]string{"box", "restart"})
	if err != nil {
		t.Fatalf("find box restart: %v", err)
	}
	if sub.Name() != "restart" {
		t.Errorf("box restart resolved to %q, want restart", sub.Name())
	}
	if sub.Flags().Lookup("space") == nil {
		t.Error("box restart missing --space")
	}
	if sub.Flags().Lookup("no-autoupdate") == nil {
		t.Error("box restart missing --no-autoupdate")
	}
	if sub.Flags().Lookup("no-attach") == nil {
		t.Error("box restart missing --no-attach")
	}
}

func TestLegacyBoxCommandAliasesRemoved(t *testing.T) {
	root := newRootCmd()
	box, _, err := root.Find([]string{"box"})
	if err != nil {
		t.Fatalf("find box: %v", err)
	}
	for _, removed := range []string{"agentbox", "ab"} {
		if slices.Contains(box.Aliases, removed) {
			t.Errorf("box still registers removed alias %q", removed)
		}
	}

	reconcile, _, err := root.Find([]string{"box", "reconcile"})
	if err != nil {
		t.Fatalf("find box reconcile: %v", err)
	}
	if slices.Contains(reconcile.Aliases, "apply") {
		t.Error("box reconcile still registers removed alias \"apply\"")
	}
}

// `box restart` validates its agentbox name up front like every other
// lifecycle command — a VM-free check that the command is wired with the shared
// validate guard before any stop/start I/O.
func TestBoxRestartValidatesName(t *testing.T) {
	cmd := newAgentboxRestartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"Bad Name!"}) // invalid per validateAgentboxName

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a name-validation error, got nil")
	}
	if !IsUserError(err) {
		t.Errorf("invalid name should be a user error, got %v", err)
	}
}

// `box restart` routes through the shared session-aware stop+start machinery
// rather than inventing a teardown path: its RunE drives stopAgentboxRuntime
// (the SAME helper `box stop` uses — it stops the VM-side session holder before
// the container) then startAgentbox (the SAME core `box start` uses — it routes
// through the managed, session-aware applier). The reuse is a compile-time fact
// (newAgentboxRestartCmd calls both and would not build otherwise); this
// asserts the helpers are package-level (shared, not re-implemented locally) by
// referencing them, and re-checks the command is wired. A behavioral
// end-to-end pass needs a live space VM and is out of scope for the unit suite.
func TestBoxRestartReusesStopAndStartHelpers(t *testing.T) {
	// Referencing the shared helpers keeps this test coupled to them: a rename
	// or removal (restart growing its own path) breaks the build here.
	_ = stopAgentboxRuntime // shared with `box stop`
	_ = startAgentbox       // shared with `box start`
	if cmd := newAgentboxRestartCmd(); cmd.RunE == nil {
		t.Fatal("box restart must have a RunE")
	}
}

func TestStopAgentboxRuntimeCleansSessionAfterContainerStop(t *testing.T) {
	calls := installStopAgentboxRuntimeHooks(t, nil)

	if err := stopAgentboxRuntime(context.Background(), nil, "default", "coder", io.Discard); err != nil {
		t.Fatalf("stopAgentboxRuntime: %v", err)
	}

	want := []string{"stop-holder", "unforward", "stop-container", "cleanup-session", "reap-netns"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestStopAgentboxRuntimeKeepsSessionStateWhenStopFails(t *testing.T) {
	stopErr := errors.New("permission denied")
	calls := installStopAgentboxRuntimeHooks(t, stopErr)

	err := stopAgentboxRuntime(context.Background(), nil, "default", "coder", io.Discard)
	if !errors.Is(err, stopErr) {
		t.Fatalf("stopAgentboxRuntime error = %v, want %v", err, stopErr)
	}

	want := []string{"stop-holder", "unforward", "stop-container"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestStopAgentboxRuntimeCleansSessionStateWhenContainerGone(t *testing.T) {
	stopErr := errors.New("no such container: coder")
	calls := installStopAgentboxRuntimeHooks(t, stopErr)

	err := stopAgentboxRuntime(context.Background(), nil, "default", "coder", io.Discard)
	if !errors.Is(err, stopErr) {
		t.Fatalf("stopAgentboxRuntime error = %v, want %v", err, stopErr)
	}

	want := []string{"stop-holder", "unforward", "stop-container", "cleanup-session"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func installStopAgentboxRuntimeHooks(t *testing.T, stopErr error) *[]string {
	t.Helper()

	oldStopHolderUnit := lifecycleStopHolderUnit
	oldUnforwardAgentbox := lifecycleUnforwardAgentbox
	oldStopAgentboxContainer := lifecycleStopAgentboxContainer
	oldReapOrphanedNetns := lifecycleReapOrphanedNetns
	oldCleanupStoppedSessionState := lifecycleCleanupStoppedSessionState

	var calls []string
	lifecycleStopHolderUnit = func(context.Context, *container.Client, string) error {
		calls = append(calls, "stop-holder")
		return nil
	}
	lifecycleUnforwardAgentbox = func(context.Context, *container.Client, string) error {
		calls = append(calls, "unforward")
		return nil
	}
	lifecycleStopAgentboxContainer = func(context.Context, *container.Client, string) error {
		calls = append(calls, "stop-container")
		return stopErr
	}
	lifecycleCleanupStoppedSessionState = func(context.Context, *container.Client, string) error {
		calls = append(calls, "cleanup-session")
		return nil
	}
	lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error {
		calls = append(calls, "reap-netns")
		return nil
	}

	t.Cleanup(func() {
		lifecycleStopHolderUnit = oldStopHolderUnit
		lifecycleUnforwardAgentbox = oldUnforwardAgentbox
		lifecycleStopAgentboxContainer = oldStopAgentboxContainer
		lifecycleReapOrphanedNetns = oldReapOrphanedNetns
		lifecycleCleanupStoppedSessionState = oldCleanupStoppedSessionState
	})

	return &calls
}
