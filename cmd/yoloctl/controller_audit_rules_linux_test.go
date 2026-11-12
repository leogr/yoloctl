package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

func TestAuditRulesNativeTransaction(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_RULE_TRANSACTION")
	if scenario == "" {
		for _, scenario := range []string{"disabled", "enabled", "reject", "stale", "gate-denied", "stop-uncertain", "start-uncertain", "cancel"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditRulesNativeTransaction$")
				cmd.Env = append(os.Environ(), "YOLOCTL_TEST_RULE_TRANSACTION="+scenario)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v", out, err)
				}
			})
		}
		return
	}
	disk := t.TempDir()
	native := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(native); err != nil {
		t.Fatal(err)
	}
	enabled := scenario != "disabled"
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: "test", DefaultAccount: "personal", DefaultWorkdir: "/projects", Audit: &spaceconfig.AuditSettings{Enabled: enabled, Version: audit.Version, RulesSHA256: audit.RulesDigest()}}
	raw, _ := yaml.Marshal(settings)
	if err := safepath.WriteFile(native.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := spaceconfig.Load("test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := nativeAuditRules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Listing is read-only, including disabled-first with no tools/storage.
	var listed bytes.Buffer
	if handled, err := runControllerAudit(t.Context(), controller.Request{Method: "audit.rules.list", Space: "test"}, nil, &listed); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if _, err := os.Stat(filepath.Join(disk, "audit")); !os.IsNotExist(err) {
		t.Fatal("list created audit storage", err)
	}
	var events []string
	held := false
	calls, installs, starts, stops := 0, 0, 0, 0
	auditAcquire = func(context.Context, string, bool) (func(), error) {
		held = true
		events = append(events, "lock")
		return func() { held = false; events = append(events, "unlock") }, nil
	}
	auditRuntimeDir = func() string { return filepath.Join(disk, "runtime") }
	auditBackendSupport = func(context.Context, *container.Client) error { return nil }
	auditKernelSupport = func() error {
		if !enabled {
			t.Fatal("disabled preparation probed BPF")
		}
		return nil
	}
	auditRulesTools = func(context.Context, string, io.Reader) (string, error) { installs++; return "/tools", nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	nativeError := errors.New("native validation error")
	auditRulesValidate = func(context.Context, string, string) (audit.Validation, error) {
		calls++
		events = append(events, "validate")
		if !held || starts != 0 || stops != 0 {
			t.Fatal("validation touched active collector")
		}
		if scenario == "reject" {
			return audit.Validation{Diagnostics: "native LOAD_ERR"}, nativeError
		}
		if scenario == "cancel" {
			cancel()
		}
		return audit.Validation{Warnings: 1}, nil
	}
	running := enabled
	auditServiceState = func(context.Context, *container.Client, string) (string, error) {
		if running {
			return "active", nil
		}
		return "inactive", nil
	}
	sentinel := errors.New("service not acknowledged")
	auditServiceStop = func(context.Context, *container.Client, string) error {
		stops++
		events = append(events, "stop")
		if !held || !slices.Contains(events, "gate") {
			t.Fatal("stop preceded gate")
		}
		if scenario == "stop-uncertain" {
			return sentinel
		}
		running = false
		return nil
	}
	auditServiceStart = func(context.Context, *container.Client, vmservice.Unit) error {
		starts++
		events = append(events, "start")
		if !enabled {
			t.Fatal("disabled import enabled capture")
		}
		saved, err := spaceconfig.Load("test")
		if err != nil || saved.Audit.RulesGeneration == "" {
			t.Fatal("start preceded publication", err)
		}
		if scenario == "start-uncertain" {
			return sentinel
		}
		root, _ := layout.AuditDir()
		serviceRaw, err := safepath.ReadFile(root, "service.json", 4096)
		if err != nil {
			t.Fatal(err)
		}
		var service audit.ServiceConfig
		if err := json.Unmarshal(serviceRaw, &service); err != nil {
			t.Fatal(err)
		}
		health, _ := json.Marshal(audit.Health{Epoch: service.Epoch, Version: service.Version, RulesSHA256: service.RulesSHA256, RulesGeneration: service.RulesGeneration, EffectiveSHA256: service.EffectiveSHA256, State: "running", Loaded: true})
		running = true
		return safepath.WriteFile(auditRuntimeDir(), "health.json", health, 0600)
	}
	recorder := &completionRecorder{events: &events, locked: func() bool { return held }}
	ctx = controller.WithEffectRecorder(ctx, recorder)
	ctx = context.WithValue(ctx, workerCompletionKey{}, &workerCompletion{})
	ctx = controller.WithEffectGate(ctx, func(_ context.Context, intent controller.EffectIntent) error {
		if !held || calls != 1 || intent.Scope == nil || !slices.Equal(intent.Scope.Writes, []controller.Resource{{Kind: controller.ResourceAudit}}) {
			t.Fatal("wrong effects boundary")
		}
		events = append(events, "gate")
		if scenario == "gate-denied" {
			return sentinel
		}
		return nil
	})
	expected := snapshot.Revision
	if scenario == "stale" {
		expected = "old"
	}
	payload, _ := json.Marshal(auditRequest{Rules: &auditRulesRequest{Expected: expected, Name: "custom.yaml"}})
	var output bytes.Buffer
	handled, err := runControllerAudit(ctx, controller.Request{ID: "audit-rule-fixture-0123456789", Method: "audit.rules.import", Space: "test", Payload: payload}, strings.NewReader("- macro: opaque\n"), &output)
	if !handled {
		t.Fatal("not handled")
	}
	after, readErr := safepath.ReadFile(native.ConfigDir, "space.yaml", 1<<20)
	if readErr != nil {
		t.Fatal(readErr)
	}
	switch scenario {
	case "reject", "stale", "gate-denied", "cancel":
		if err == nil || !bytes.Equal(after, raw) || starts != 0 || stops != 0 {
			t.Fatalf("rejection changed authoritative state %v %v", events, err)
		}
		if scenario == "stale" && (calls != 0 || installs != 0) {
			t.Fatal("stale save consumed input/installed/validated")
		}
		if scenario == "reject" {
			var result auditRulesResult
			if decodeErr := json.Unmarshal(output.Bytes(), &result); decodeErr != nil || result.Validation.Diagnostics != "native LOAD_ERR" {
				t.Fatal("native error details lost", decodeErr)
			}
		}
	case "stop-uncertain", "start-uncertain":
		var uncertain *controller.EffectsUncertain
		if !errors.As(err, &uncertain) || bytes.Equal(after, raw) {
			t.Fatal("lost uncertain outcome or rolled desired state back", err)
		}
	default:
		if err != nil || calls != 1 || installs != 1 {
			t.Fatal(events, err)
		}
		var result auditRulesResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Changed || result.Rules.Warnings != 1 || result.Status.Desired.Enabled != enabled || result.Status.Desired.EffectiveSHA256 != result.Rules.Fingerprint {
			t.Fatal(result)
		}
		if enabled && (starts != 1 || stops != 1 || result.Status.Driver != "modern_ebpf") {
			t.Fatal("enabled apply did not match health", result, events)
		}
		if !enabled && (starts != 0 || stops != 0) {
			t.Fatal("disabled import changed service", events)
		}
		if enabled {
			root, _ := layout.AuditDir()
			raw, err := safepath.ReadFile(root, "service.json", 4096)
			if err != nil {
				t.Fatal(err)
			}
			var expected audit.ServiceConfig
			if err := json.Unmarshal(raw, &expected); err != nil {
				t.Fatal(err)
			}
			// A stale built-in-only load cannot credit this generation, even with the
			// exact epoch/version. Cancellation must retain the existing unknown result.
			oldHealth, _ := json.Marshal(audit.Health{Epoch: expected.Epoch, Version: expected.Version, RulesSHA256: expected.RulesSHA256, State: "running", Loaded: true})
			if err := safepath.WriteFile(auditRuntimeDir(), "health.json", oldHealth, 0600); err != nil {
				t.Fatal(err)
			}
			status, err := nativeAuditStatus(t.Context())
			if err != nil || status.Driver != "unloaded" {
				t.Fatal("old load credited to new generation", status, err)
			}
			waitCtx, stopWait := context.WithCancel(t.Context())
			defer stopWait()
			auditServiceState = func(context.Context, *container.Client, string) (string, error) { stopWait(); return "active", nil }
			err = waitAuditReadyFor(waitCtx, expected)
			if !workerEffectsUncertain(err) {
				t.Fatal("stale readiness became success or a certain failure", err)
			}
		}

	}
	complete, unlock := slices.Index(events, "complete"), slices.Index(events, "unlock")
	if scenario == "stop-uncertain" || scenario == "start-uncertain" || scenario == "cancel" {
		if complete >= 0 {
			t.Fatal("uncertain/cancelled outcome reported complete", events)
		}
	} else if complete < 0 || unlock < complete || slices.Contains(events, "complete-unlocked") {
		t.Fatal("completion after unlock", events)
	}
}
