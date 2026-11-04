package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

func nativePreparationClient(t *testing.T) (*container.Client, string) {
	t.Helper()
	directory := t.TempDir()
	log := filepath.Join(directory, "commands.log")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	t.Setenv("NATIVE_PREPARATION_LOG", log)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$NATIVE_PREPARATION_LOG"
case "$*" in *'sudo mkdir -p /run/yoloctl/coder') exit 0;; *) exit 42;; esac
`
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), log
}
func TestNativeRuntimePreparationQualificationPrecedesNormalizedStateAndPaths(t *testing.T) {
	for _, normalized := range []bool{false, true} {
		t.Run(map[bool]string{false: "paths", true: "declaration"}[normalized], func(t *testing.T) {
			client, log := nativePreparationClient(t)
			cause := errors.New("native exact helper/policy qualification unavailable")
			calls := 0
			cfg := &agentbox.Config{Name: "coder", Space: "fixture", Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost}}
			prep := runtimePreparation{normalized: normalized}
			prep.stateMount.Enabled = true
			err := finishAgentboxRuntimePreparation(t.Context(), client, cfg, prep, func(_ context.Context, actual *agentbox.Config) (bool, error) {
				calls++
				if actual != cfg {
					t.Fatal("qualification received a different declaration")
				}
				return true, cause
			})
			raw, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !errors.Is(err, cause) || calls != 1 || len(raw) != 0 {
				t.Fatal("native preparation effects preceded qualification refusal", err, calls, string(raw))
			}
		})
	}
}
func TestNativeRuntimePreparationSkipsLegacySocketCreationOnlyForNative(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "native"}[native], func(t *testing.T) {
			client, log := nativePreparationClient(t)
			called := 0
			cfg := &agentbox.Config{Name: "coder", Space: "fixture", Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost}}
			err := finishAgentboxRuntimePreparation(t.Context(), client, cfg, runtimePreparation{}, func(context.Context, *agentbox.Config) (bool, error) { called++; return native, nil })
			raw, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err != nil || called != 1 || strings.Contains(string(raw), "sudo mkdir -p /run/yoloctl/coder") == native {
				t.Fatal("native preparation used legacy socket mkdir or lost legacy control", err, called, string(raw))
			}
		})
	}
}
func TestNativeRuntimePreparationCancellationPrecedesAllEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	err := finishAgentboxRuntimePreparation(ctx, nil, nil, runtimePreparation{normalized: true}, func(context.Context, *agentbox.Config) (bool, error) { calls++; return true, nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled native preparation reached verifier/writes", err, calls)
	}
}
