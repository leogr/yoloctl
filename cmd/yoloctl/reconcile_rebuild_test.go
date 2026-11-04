package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
)

// --rebuild and --dry-run are mutually exclusive: one forces a build+recreate,
// the other promises to do nothing.
func TestReconcileRebuildDryRunMutuallyExclusive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := newAgentboxReconcileCmd()
	cmd.SetArgs([]string{"coder", "--dry-run", "--rebuild"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutual-exclusion error, got %v", err)
	}
}

// The --rebuild flag is registered on the reconcile command.
func TestReconcileRebuildFlagRegistered(t *testing.T) {
	if newAgentboxReconcileCmd().Flags().Lookup("rebuild") == nil {
		t.Fatal("reconcile command is missing the --rebuild flag")
	}
}

// A rebuild validates the declaration and composes its policy before the
// forced image build, so a retired privileged grant, explicit or required by a
// feature, builds nothing, with or without the setting.
func TestRebuildValidatesBeforeBuilding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeTestFeature(t, "kernel-dev", capabilities.RetiredPrivilegedCapability)
	for _, privileged := range []bool{false, true} {
		explicit := &agentbox.Config{Name: "coder", Space: "default", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("coder"),
			Features: []string{"go"}, Capabilities: []string{capabilities.RetiredPrivilegedCapability}, Privileged: privileged}
		err := runRebuild(t.Context(), nil, io.Discard, io.Discard, container.New("default"), explicit, "coder", agentbox.LiveInspector{}, nil)
		if !errors.Is(err, capabilities.ErrRetiredPrivileged) {
			t.Fatalf("explicit, privileged=%v: rebuild = %v", privileged, err)
		}
		required := &agentbox.Config{Name: "coder", Space: "default", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("coder"),
			Features: []string{"go", "kernel-dev"}, Privileged: privileged}
		err = runRebuild(t.Context(), nil, io.Discard, io.Discard, container.New("default"), required, "coder", agentbox.LiveInspector{}, nil)
		if err == nil || !strings.Contains(err.Error(), `feature "kernel-dev" requires the retired `+capabilities.RetiredPrivilegedCapability+` capability`) {
			t.Fatalf("feature-required, privileged=%v: rebuild = %v", privileged, err)
		}
	}
}
