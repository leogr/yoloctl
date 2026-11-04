package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
)

const inspectSecretLiteral = "topsecret-literal-value"

func inspectTestConfig() *agentbox.Config {
	return &agentbox.Config{
		SchemaVersion:  1,
		Name:           "coder",
		Space:          "default",
		Agent:          "claude",
		Image:          "yoloctl/agentbox:ubuntu-24.04",
		AgentExtraArgs: []string{"--model", "opus"},
		Workdir:        agentbox.Workdir{Mode: "host", Source: "/home/me/code/myrepo", Canonical: "/home/me/code/myrepo"},
		Features:       []string{"claude", "github"},
		Capabilities:   []string{capabilities.WorkdirWriteCapability},
		Ports:          []agentbox.Port{{Target: 3000, Published: 3000}},
		Env: map[string]agentbox.EnvVar{
			"MY_SECRET": {Literal: inspectSecretLiteral},
			"API_KEY":   {Literal: "another-value"},
		},
	}
}

// TestEnvKeysOnly is the runtime-env redaction guard: only KEYS are ever
// extracted from a container's "KEY=VALUE" env list — never the values.
func TestEnvKeysOnly(t *testing.T) {
	got := envKeysOnly([]string{"FOO=bar", "HOME=/root", "MY_SECRET=topsecret", "BARE", "=novalue"})
	want := []string{"BARE", "FOO", "HOME", "MY_SECRET"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("envKeysOnly = %v, want %v", got, want)
	}
	joined := strings.Join(got, " ")
	for _, leak := range []string{"bar", "/root", "topsecret", "novalue"} {
		if strings.Contains(joined, leak) {
			t.Errorf("envKeysOnly leaked a value %q in %v", leak, got)
		}
	}
}

// TestBuildInspectManifestRedaction verifies the manifest view exposes env
// KEYS only, never values. The inspectEnv struct has no value field at all, so
// leakage is structurally impossible — this test pins the key-only mapping.
func TestBuildInspectManifestRedaction(t *testing.T) {
	m := buildInspectManifest(inspectTestConfig())
	if len(m.Env) != 2 {
		t.Fatalf("expected 2 env entries, got %d: %+v", len(m.Env), m.Env)
	}
	// Sorted by key: API_KEY before MY_SECRET.
	if m.Env[0].Key != "API_KEY" {
		t.Errorf("Env[0].Key = %q, want API_KEY", m.Env[0].Key)
	}
	if m.Env[1].Key != "MY_SECRET" {
		t.Errorf("Env[1].Key = %q, want MY_SECRET", m.Env[1].Key)
	}
	if m.Workdir.Mode != "host" || m.Workdir.ContainerPath != "/workdir/home/me/code/myrepo" {
		t.Errorf("workdir = %+v, want host mirrored under /workdir", m.Workdir)
	}
	if strings.Join(m.AgentExtraArgs, " ") != "--model opus" {
		t.Errorf("AgentExtraArgs = %v, want [--model opus]", m.AgentExtraArgs)
	}
	// The manifest's port (declared without host_ip/protocol) is shown in the
	// RESOLVED full form: host_ip 127.0.0.1 and protocol tcp folded in.
	if len(m.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d: %+v", len(m.Ports), m.Ports)
	}
	if got := m.Ports[0]; got.HostIP != "127.0.0.1" || got.Published != 3000 || got.Target != 3000 || got.Protocol != "tcp" {
		t.Errorf("port = %+v, want resolved {127.0.0.1 published:3000 target:3000 tcp}", got)
	}
}

// TestBuildInspectEffectiveCaps verifies the effective-capability section
// resolves each grant with its origin. Under the single capability boundary
// (agent → features → capabilities) the origins are: explicit
// (fs.workdir.write on the manifest), feature (network.anthropic from the
// claude feature), and feature (network.github from the github feature) — there
// is no profile origin.
func TestBuildInspectEffectiveCaps(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // so the claude profile installs + resolves
	caps, errStr := buildInspectEffectiveCaps(inspectTestConfig())
	if errStr != "" {
		t.Fatalf("buildInspectEffectiveCaps: %s", errStr)
	}
	origins := map[string]string{}
	for _, c := range caps {
		origins[c.Name] = strings.Join(c.Origins, ",")
	}
	for name, wantOrigin := range map[string]string{
		capabilities.WorkdirWriteCapability: "explicit",
		"network.anthropic":                 "feature",
		"network.github":                    "feature",
	} {
		got, ok := origins[name]
		if !ok {
			t.Errorf("effective set missing %q; got %v", name, origins)
			continue
		}
		if !strings.Contains(got, wantOrigin) {
			t.Errorf("capability %q origins = %q, want to include %q", name, got, wantOrigin)
		}
	}
}

// TestBuildInspectPolicy checks the computed-policy section carries the
// generated YAML + a sha256, and is the "composed" source for a feature box.
func TestBuildInspectPolicy(t *testing.T) {
	p, errStr := buildInspectPolicy(inspectTestConfig())
	if errStr != "" {
		t.Fatalf("buildInspectPolicy error: %s", errStr)
	}
	if p.Source != "composed" {
		t.Errorf("policy source = %q, want composed", p.Source)
	}
	if !strings.HasPrefix(p.SHA256, "sha256:") || len(p.SHA256) != len("sha256:")+64 {
		t.Errorf("policy sha %q is not a sha256:<64hex>", p.SHA256)
	}
	for _, want := range []string{"network_policies", "anthropic", "github", "filesystem_policy"} {
		if !strings.Contains(p.YAML, want) {
			t.Errorf("computed policy YAML missing %q", want)
		}
	}
}

// TestInspectReportJSONNoSecretLeak is the end-to-end redaction guarantee:
// a report assembled from a config with a literal env value must marshal to
// JSON that contains the env KEYS but never any env value.
func TestInspectReportJSONNoSecretLeak(t *testing.T) {
	cfg := inspectTestConfig()
	policy, _ := buildInspectPolicy(cfg)
	report := inspectReport{
		Identity: inspectIdentity{Space: "default", Name: "coder", Agent: "claude", State: "configured"},
		Manifest: buildInspectManifest(cfg),
		Policy:   policy,
		Runtime: &inspectRuntime{
			EnvKeys: envKeysOnly([]string{"API_KEY=" + inspectSecretLiteral, "PATH=/usr/bin"}),
		},
		Drift: inspectDrift{State: "in_sync"},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, inspectSecretLiteral) {
		t.Fatalf("inspect JSON leaked the secret literal value:\n%s", s)
	}
	for _, want := range []string{`"MY_SECRET"`, `"API_KEY"`} {
		if !strings.Contains(s, want) {
			t.Errorf("inspect JSON missing expected (non-secret) env key %s", want)
		}
	}
}

// TestWriteInspectHumanSmoke renders the human view and checks the section
// scaffolding is present and no secret leaked.
func TestWriteInspectHumanSmoke(t *testing.T) {
	cfg := inspectTestConfig()
	policy, _ := buildInspectPolicy(cfg)
	report := inspectReport{
		Identity: inspectIdentity{
			Space: "default", Name: "coder", Agent: "claude", State: "running",
			Declaration: "declared", RuntimeState: "running", SessionState: "running", DriftState: "in_sync",
			SchemaVersion: 1, ConfigPath: "/cfg/coder.yaml", ManifestHash: "sha256:abc",
		},
		Manifest: buildInspectManifest(cfg),
		Policy:   policy,
		Drift:    inspectDrift{State: "in_sync", Strategy: "none (in sync)"},
	}
	var buf bytes.Buffer
	if err := writeInspectHuman(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Box:      coder", "Manifest (declared desired state)",
		"Computed OpenShell policy", "Runtime container", "Drift",
		"Agent extra args: --model opus",
		"MY_SECRET = (redacted)", "API_KEY = (redacted)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human inspect output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, inspectSecretLiteral) {
		t.Fatalf("human inspect output leaked secret literal:\n%s", out)
	}
}

// box inspect's computed policy lists a declared read-only extra mount under
// read_only (the stress-test box: --mount SRC:/mnt/x:ro).
func TestBuildInspectPolicyListsExtraMount(t *testing.T) {
	cfg := inspectTestConfig()
	cfg.Mounts = append(cfg.Mounts, agentbox.Mount{Source: "/workdir/home/me/docs", Target: "/mnt/x", ReadOnly: true})
	p, errStr := buildInspectPolicy(cfg)
	if errStr != "" {
		t.Fatalf("buildInspectPolicy error: %s", errStr)
	}
	ro := p.YAML[strings.Index(p.YAML, "read_only:"):strings.Index(p.YAML, "read_write:")]
	if !strings.Contains(ro, "- /mnt/x\n") {
		t.Fatalf("computed policy does not grant the read-only mount:\n%s", p.YAML)
	}
}
