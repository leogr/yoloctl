package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFalcoDiagnosticChild(t *testing.T) {
	if os.Getenv("YOLOCTL_AUDIT_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	phase := "collection"
	for _, arg := range os.Args {
		if arg == "-V" {
			phase = "validation"
		}
		if arg == "-M" {
			phase = "preflight"
		}
	}
	scenario := os.Getenv("YOLOCTL_AUDIT_DIAGNOSTIC_CASE")
	if scenario == "timeout" || scenario == "cancel" && phase == "collection" {
		_, _ = fmt.Fprintln(os.Stderr, "waiting for cancellation")
		time.Sleep(time.Minute)
	}
	if scenario == phase || scenario == "large" && phase == "validation" {
		_, _ = fmt.Fprintln(os.Stdout, "RAW_EVENT_STDOUT_MUST_NOT_ENTER_HEALTH")
		_, _ = fmt.Fprintln(os.Stderr, "diagnostic: plugin schema mismatch\x1b[31m\nsecond diagnostic")
		if scenario == "large" {
			_, _ = fmt.Fprint(os.Stderr, strings.Repeat("more diagnostics ", 32<<10))
		}
		os.Exit(7)
	}
	os.Exit(0)
}

func diagnosticFixture(t *testing.T, scenario string) (string, string, string) {
	t.Helper()
	t.Setenv("YOLOCTL_AUDIT_DIAGNOSTIC_CHILD", "1")
	t.Setenv("YOLOCTL_AUDIT_DIAGNOSTIC_CASE", scenario)
	original := collectProbeSupport
	collectProbeSupport = func() error { return nil }
	t.Cleanup(func() { collectProbeSupport = original })
	root, run, tools := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(tools, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "'"
	script := "#!/bin/sh\nexec " + quoted + " -test.run=^TestFalcoDiagnosticChild$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "bin", "falco"), []byte(script), 0500); err != nil {
		t.Fatal(err)
	}
	return root, run, tools
}

func readDiagnosticHealth(t *testing.T, run string) Health {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(run, "health.json"))
	if err != nil {
		t.Fatal(err)
	}
	var health Health
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatal(err)
	}
	return health
}

func TestCollectRetainsStageAndStderrWithoutEventOutput(t *testing.T) {
	for scenario, stage := range map[string]string{"validation": "rules validation", "preflight": "modern eBPF preflight", "collection": "collection", "large": "rules validation"} {
		t.Run(scenario, func(t *testing.T) {
			root, run, tools := diagnosticFixture(t, scenario)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := Collect(ctx, root, run, tools, ServiceConfig{Epoch: "fixture", Version: Version, RulesSHA256: RulesDigest()})
			if err == nil || ctx.Err() != nil {
				t.Fatal("expected prompt child failure", err, ctx.Err())
			}
			health := readDiagnosticHealth(t, run)
			if health.State != "failed" || health.Loaded || !strings.Contains(health.Error, "falco "+stage+" failed") || !strings.Contains(health.Error, "plugin schema mismatch") {
				t.Fatal(health)
			}
			if len(health.Error) > 1024 || strings.Contains(health.Error, "RAW_EVENT_STDOUT") || strings.Contains(health.Error, "\x1b") || strings.Contains(health.Error, "compatible VM kernel") {
				t.Fatal("unsafe or misleading health", health.Error)
			}
		})
	}
}

func TestFalcoPhaseTimeoutRetainsItsCause(t *testing.T) {
	_, _, tools := diagnosticFixture(t, "timeout")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := runFalcoPhase(ctx, "modern eBPF preflight", filepath.Join(tools, "bin", "falco"))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "modern eBPF preflight") {
		t.Fatal(err)
	}
}

func TestCollectCancellationRemainsInactive(t *testing.T) {
	root, run, tools := diagnosticFixture(t, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Collect(ctx, root, run, tools, ServiceConfig{Epoch: "fixture", Version: Version, RulesSHA256: RulesDigest()})
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		raw, _ := os.ReadFile(filepath.Join(run, "health.json"))
		var health Health
		_ = json.Unmarshal(raw, &health)
		if health.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not join after cancellation")
	}
	health := readDiagnosticHealth(t, run)
	if health.State != "inactive" || health.Loaded || health.Error != "" {
		t.Fatal(health)
	}
}

func TestCollectRetainsNonFalcoFailure(t *testing.T) {
	root, run, tools := diagnosticFixture(t, "unused")
	err := Collect(t.Context(), root, run, tools, ServiceConfig{Version: "wrong"})
	health := readDiagnosticHealth(t, run)
	if err == nil || !strings.Contains(health.Error, "pins differ") || strings.Contains(health.Error, "kernel") {
		t.Fatal(err, health)
	}
}
