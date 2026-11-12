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
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
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

func TestAuditNativeToggleGateAndStopFailure(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_AUDIT_TOGGLE")
	if scenario == "" {
		scenarios := []string{"gate-denied", "stop-failed", "disabled", "microvm-disabled", "status-failure"}
		if os.Getenv("FALCO_TEST_ARCHIVES") != "" {
			scenarios = append(scenarios, "enabled", "start-failed", "microvm-enabled")
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
	microvm := strings.HasPrefix(scenario, "microvm-")
	scenario = strings.TrimPrefix(scenario, "microvm-")
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if microvm {
		c.ServiceManager = "microvm"
		auditBackendSupport = func(context.Context, *container.Client) error { return nil }
	}
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
	if scenario == "status-failure" {
		running = false
		root, err := layout.AuditDir()
		if err != nil {
			t.Fatal(err)
		}
		detail := "Falco modern eBPF preflight failed: exit status 1; stderr prefix: plugin schema mismatch\x1b[31m"
		service, _ := json.Marshal(audit.ServiceConfig{Epoch: requestID, Version: audit.Version, RulesSHA256: audit.RulesDigest()})
		health, _ := json.Marshal(audit.Health{Epoch: requestID, State: "failed", Error: detail})
		if err := safepath.WriteFile(root, "service.json", service, 0600); err != nil {
			t.Fatal(err)
		}
		if err := safepath.WriteFile(auditRuntimeDir(), "health.json", health, 0600); err != nil {
			t.Fatal(err)
		}
		status, err := nativeAuditStatus(t.Context())
		if err != nil || status.State != "failed" || status.Driver != "unloaded" || status.Error != audit.Clean(detail) || !status.Desired.Enabled {
			t.Fatal(status, err)
		}
		return
	}
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
	for _, method := range []string{"audit.status", "audit.enable", "audit.disable", "audit.logs", "audit.export", "audit.rules.list", "audit.rules.import", "audit.rules.remove", "audit.rules.edit"} {
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

// Native context is process-wide, so every case owns a private subprocess/disk.
func TestAuditRetentionInactiveCollector(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_AUDIT_RETENTION")
	if scenario == "" {
		for _, scenario := range []string{"enabled-failed", "enabled-failed-observer", "enabled-inactive", "disabled-failed", "active", "activating", "deactivating", "unknown", "observation-failed", "audit-held", "global-held", "lock-failed"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditRetentionInactiveCollector$")
				cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_RETENTION="+scenario)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			})
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: "test", DefaultAccount: "personal", DefaultWorkdir: "/projects", Audit: &spaceconfig.AuditSettings{Enabled: scenario != "disabled-failed", Version: audit.Version, RulesSHA256: audit.RulesDigest()}}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := layout.AuditDir()
	if err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(root, "logs")
	w, err := audit.OpenWriter(audit.Store{Root: logs})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(audit.Event{Time: time.Now().Add(-8 * 24 * time.Hour), Rule: "expired", Severity: "Notice"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if scenario == "audit-held" || scenario == "global-held" {
		receipt := controller.Receipt{ID: "audit-retention-held-0123456789", Method: "audit.enable", State: "uncertain", EffectPhase: controller.EffectStarted, Started: time.Now(), Generation: "fixture"}
		if scenario == "audit-held" {
			receipt.ProofVersion = controller.ProofVersion
			receipt.DiskID = c.DiskID
			receipt.Scope = &controller.Scope{Writes: []controller.Resource{{Kind: controller.ResourceAudit}}}
		}
		store := controller.Receipts{Dir: filepath.Join(disk, ".controller", "receipts")}
		if err := store.Save(receipt); err != nil {
			t.Fatal(err)
		}
	}
	held, observed, unlocked := false, false, false
	sentinel := errors.New("unavailable")
	auditAcquire = func(_ context.Context, space string, write bool) (func(), error) {
		if space != c.Space || (!write && scenario != "enabled-failed-observer") {
			t.Fatal("incorrect lock")
		}
		if scenario == "lock-failed" {
			return nil, sentinel
		}
		held = true
		return func() { held = false; unlocked = true }, nil
	}
	auditServiceState = func(context.Context, *container.Client, string) (string, error) {
		if !held {
			t.Fatal("observed service outside lock")
		}
		observed = true
		switch scenario {
		case "observation-failed":
			return "", sentinel
		case "active", "activating", "deactivating", "unknown":
			return scenario, nil
		case "enabled-inactive":
			return "inactive", nil
		default:
			return "failed", nil
		}
	}
	auditServiceStart = func(context.Context, *container.Client, vmservice.Unit) error {
		t.Fatal("pruning restarted service")
		return nil
	}
	auditServiceStop = func(context.Context, *container.Client, string) error { t.Fatal("pruning stopped service"); return nil }
	if scenario == "enabled-failed-observer" {
		auditObserveLabels = func(context.Context, string) ([]container.AuditLabelObservation, error) { return nil, sentinel }
		var checked bool
		checked, err = maintainNativeAudit(t.Context(), true)
		if !checked {
			t.Fatal("metadata failure skipped expiry")
		}
	} else {
		err = pruneInactiveAudit(t.Context())
	}
	wantErr := scenario == "enabled-failed-observer" || scenario == "observation-failed" || scenario == "audit-held" || scenario == "global-held" || scenario == "lock-failed"
	if (err != nil) != wantErr {
		t.Fatalf("error=%v want error=%t", err, wantErr)
	}
	if held || (scenario != "lock-failed" && !unlocked) {
		t.Fatal("lock not released")
	}
	if (scenario == "audit-held" || scenario == "global-held" || scenario == "lock-failed") && observed {
		t.Fatal("service observed before lock/hold clearance")
	}
	files, err := filepath.Glob(filepath.Join(logs, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	shouldPrune := scenario == "enabled-failed-observer" || scenario == "enabled-failed" || scenario == "enabled-inactive" || scenario == "disabled-failed"
	if shouldPrune {
		if len(files) != 0 {
			t.Fatalf("expired data remains: %v", files)
		}
	} else {
		if len(files) != 1 {
			t.Fatal("changed excluded writer", files)
		}
		body, err := os.ReadFile(files[0])
		if err != nil || !bytes.Contains(body, []byte("expired")) {
			t.Fatal("changed excluded events", err)
		}
	}
}

func TestAuditCompletionUsesNativeHostAuthority(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_AUDIT_COMPLETION") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditCompletionUsesNativeHostAuthority$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_COMPLETION=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture"}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // Runtime observation fails locally; no real guest/controller access.
	if err := os.MkdirAll(c.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{Name: "auditbox", ID: agentbox.NewIdentity(), Space: c.Space, Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("auditbox")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"logs", "export"} {
		payload, err := json.Marshal(completionRequest{Command: "audit " + action, Flag: "box", Prefix: "audit"})
		if err != nil {
			t.Fatal(err)
		}
		for _, operator := range []bool{false, true} {
			principal := controller.Principal{}
			if operator {
				principal = controller.Principal{Box: "caller", Incarnation: "fixture"}
			}
			var out bytes.Buffer
			err := runControllerCompletionInto(t.Context(), principal, controller.Request{Space: c.Space, Payload: payload}, &out)
			if operator {
				if err == nil || out.Len() != 0 {
					t.Fatal("operator completed host audit", out.String(), err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			var result completionResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || !slices.Equal(result.Values, []string{"auditbox"}) {
				t.Fatal("native audit completion", out.String(), err)
			}
		}
	}
}
