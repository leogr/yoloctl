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
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

func TestAuditNativeToggleGateAndStopFailure(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_AUDIT_TOGGLE")
	if scenario == "" {
		scenarios := []string{"gate-denied", "stop-failed", "disabled"}
		if os.Getenv("FALCO_TEST_ARCHIVES") != "" {
			scenarios = append(scenarios, "enabled", "start-failed")
		}
		for _, scenario := range scenarios {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditNativeToggleGateAndStopFailure$", "-test.v")
			cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_TOGGLE="+scenario)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %s %v", scenario, output, err)
			}
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: "test", DefaultAccount: "personal", DefaultWorkdir: "/projects", Audit: &spaceconfig.AuditSettings{Enabled: true, Version: audit.Version, RulesSHA256: audit.RulesDigest()}}
	enabling := scenario == "enabled" || scenario == "start-failed"
	if enabling {
		settings.Audit.Enabled = false
	}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	var events []string
	held := false
	auditAcquire = func(context.Context, string, bool) (func(), error) {
		held = true
		events = append(events, "lock")
		return func() { held = false; events = append(events, "unlock") }, nil
	}
	auditRuntimeDir = func() string { return filepath.Join(disk, "runtime") }
	running := !enabling
	auditKernelSupport = func() error { return nil } // No probe or live VM operation in this fixture.
	auditServiceState = func(context.Context, *container.Client, string) (string, error) {
		events = append(events, "state")
		if running {
			return "active", nil
		}
		return "inactive", nil
	}

	sentinel := errors.New("unacknowledged service action")
	const requestID = "audit-test-0123456789abcdef"
	auditServiceStart = func(_ context.Context, _ *container.Client, unit vmservice.Unit) error {
		if !enabling || !held || !slices.Contains(events, "gate") {
			t.Fatal("start preceded permission", events)
		}
		saved, err := spaceconfig.Load(c.Space)
		if err != nil || saved.Audit == nil || !saved.Audit.Enabled || saved.Audit.RulesSHA256 != audit.RulesDigest() {
			t.Fatal("service preceded durable desired state", saved, err)
		}
		if unit.Name != audit.ServiceName || !slices.Equal(unit.Args, auditUnit().Args) {
			t.Fatal(unit)
		}
		events = append(events, "start")
		if scenario == "start-failed" {
			return sentinel
		}
		running = true
		health, _ := json.Marshal(audit.Health{Epoch: requestID, State: "running", Loaded: true, Version: audit.Version, RulesSHA256: audit.RulesDigest()})
		return safepath.WriteFile(auditRuntimeDir(), "health.json", health, 0600)
	}

	auditServiceStop = func(context.Context, *container.Client, string) error {
		if !held {
			t.Fatal("stop outside native lock")
		}
		events = append(events, "stop")
		if scenario == "stop-failed" {
			return sentinel
		}
		running = false
		return nil
	}
	recorder := &completionRecorder{events: &events, locked: func() bool { return held }}
	ctx := controller.WithEffectRecorder(t.Context(), recorder)
	ctx = context.WithValue(ctx, workerCompletionKey{}, &workerCompletion{})
	ctx = controller.WithEffectGate(ctx, func(_ context.Context, intent controller.EffectIntent) error {
		if !held || intent.Scope == nil || intent.Scope.Global || !slices.Equal(intent.Scope.Writes, []controller.Resource{{Kind: controller.ResourceAudit}}) {
			t.Fatal("wrong pre-effect permission", intent)
		}
		events = append(events, "gate")
		if scenario == "gate-denied" {
			return sentinel
		}
		return nil
	})

	var output bytes.Buffer
	method := "audit.disable"
	var input auditRequest
	var archive []byte
	if enabling {
		method = "audit.enable"
		pin, err := audit.Pinned(runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		archive, err = os.ReadFile(filepath.Join(os.Getenv("FALCO_TEST_ARCHIVES"), "falco-"+audit.Version+"-"+pin.Name+".tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		input.ArchiveSHA256 = pin.SHA256
	}
	payload, _ := json.Marshal(input)
	handled, err := runControllerAudit(ctx, controller.Request{ID: requestID, Method: method, Space: "test", Dash: -1, Payload: payload}, bytes.NewReader(archive), &output)

	if !handled {
		t.Fatal("dispatcher did not handle audit")
	}
	cfg, loadErr := spaceconfig.Load("test")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if scenario == "disabled" || scenario == "enabled" {
		if err != nil || cfg.Audit.Enabled != enabling || !slices.Contains(events, "complete") {
			t.Fatal(events, cfg, err)
		}
		complete, unlock := slices.Index(events, "complete"), slices.Index(events, "unlock")
		if complete < 0 || unlock < complete || slices.Contains(events, "complete-unlocked") {
			t.Fatal("receipt completed after unlock", events)
		}
	} else {
		if !errors.Is(err, sentinel) || !cfg.Audit.Enabled {
			t.Fatal("failure changed desired state", events, cfg, err)
		}
		if scenario == "gate-denied" && slices.Contains(events, "stop") {
			t.Fatal("effect before denied gate", events)
		}
		if (scenario == "stop-failed" || scenario == "start-failed") && (!workerEffectsUncertain(err) || slices.Contains(events, "complete")) {
			t.Fatal("lost stop receipt was reported complete", events, err)
		}
	}
}

func TestAuditWorkerRejectsOperatorBeforeStorage(t *testing.T) {
	for _, method := range []string{"audit.status", "audit.enable", "audit.disable", "audit.logs", "audit.export"} {
		ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "operator"})
		handled, err := runControllerAudit(ctx, controller.Request{Method: method}, strings.NewReader("untrusted archive"), io.Discard)
		if !handled || err == nil || !strings.Contains(err.Error(), "host controller endpoint") {
			t.Fatal(method, handled, err)
		}
	}
}

func TestAuditQueryRejectsHostPaths(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"path": "/etc/shadow"})
	_, err := runControllerAudit(t.Context(), controller.Request{Method: "audit.logs", Payload: input}, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("accepted arbitrary guest log path")
	}
}
