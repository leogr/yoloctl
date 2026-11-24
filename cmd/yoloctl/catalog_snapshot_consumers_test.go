package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/catalogsnapshot"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/ocifeature"
)

const (
	catalogSnapshotReaderCapability = "network.snapshot-reader"
	catalogSnapshotReaderFeature    = "snapshot-reader"
	catalogSnapshotReaderInstall    = "RUN echo snapshot-reader"
)

func catalogSnapshotReaderConfig(t *testing.T) *agentbox.Config {
	t.Helper()
	const name = catalogSnapshotReaderCapability
	const feature = catalogSnapshotReaderFeature
	const ref = "registry.example/features/reader:1"
	const definition = `grants:
  network:
    egress:
      - hosts: [z.example, a.example]
        ports: [8443, 443]
        binaries: [/opt/reader/z, /opt/reader/a]
      - hosts: [another.example]
        ports: [9443, 443]
        binaries: [/opt/other]
  pty:
    allocate: true
`
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", root)
	dir := filepath.Join(root, "capabilities")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, "features")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const featureDefinition = "name: " + feature + "\n" +
		"requires_capabilities: [" + name + "]\n" +
		"install:\n  kind: dockerfile\n  dockerfile: '" + catalogSnapshotReaderInstall + "'\n"
	if err := os.WriteFile(filepath.Join(dir, feature+".yaml"), []byte(featureDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		Agent:        "none",
		Capabilities: []string{name},
		Features:     []string{feature, ref},
		FeatureLocks: []ocifeature.Lock{{
			Ref:    ref,
			Digest: "sha256:" + strings.Repeat("a", 64),
			Layer: ocifeature.Descriptor{
				Digest:    "sha256:" + strings.Repeat("f", 64),
				MediaType: "application/vnd.devcontainers.layer.v1+tar",
				Size:      123,
			},
			Metadata: `{"id":"reader","version":"1","containerEnv":{"TOOL_HOME":"/opt/reader","PATH":"/opt/reader/bin:${PATH}"}}`,
		}},
	}
	catalogsnapshot.Use(root)
	t.Cleanup(func() { catalogsnapshot.Use("") })
	return cfg
}

func TestCatalogSnapshotConsumersLeaveCapabilitiesReadOnly(t *testing.T) {
	cfg := catalogSnapshotReaderConfig(t)
	set, err := agentbox.EffectiveCapabilities(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Encode the values before running the readers. Holding another view of
	// the same slices would not detect writes through a shared backing array.
	record := func(t *testing.T) []byte {
		t.Helper()
		raw, err := json.Marshal(set.Effective())
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := record(t)
	check := func(name string, run func(*testing.T)) {
		t.Run(name, func(t *testing.T) {
			run(t)
			if after := record(t); !bytes.Equal(before, after) {
				t.Fatalf("mutated cached capability\nbefore %s\nafter  %s", before, after)
			}
		})
	}
	check("policy", func(t *testing.T) { catalogSnapshotReaderPolicy(t, cfg) })
	for _, reader := range catalogSnapshotOtherReaders(cfg) {
		check(reader.name, reader.run)
	}
}

func TestCatalogSnapshotPolicyAndInstallPlanStableAcrossReaders(t *testing.T) {
	cfg := catalogSnapshotReaderConfig(t)
	plan, err := cfg.FeaturePlan()
	if err != nil {
		t.Fatal(err)
	}
	installPlan := func() (string, []string) {
		t.Helper()
		dockerfile, installing, hasInstall, err := features.InstallPlan("ubuntu:24.04", cfg.Features, plan)
		if err != nil || !hasInstall || !slices.Contains(installing, catalogSnapshotReaderFeature) ||
			!strings.Contains(dockerfile, catalogSnapshotReaderInstall) {
			t.Fatalf("install plan did not include the custom Feature: %v, %t, %v\n%s", err, hasInstall, installing, dockerfile)
		}
		return dockerfile, installing
	}
	beforePolicy := bytes.Clone(catalogSnapshotReaderPolicy(t, cfg))
	beforeDockerfile, beforeInstalling := installPlan()
	beforeInstalling = slices.Clone(beforeInstalling)
	for _, reader := range catalogSnapshotOtherReaders(cfg) {
		reader.run(t)
	}
	if afterPolicy := catalogSnapshotReaderPolicy(t, cfg); !bytes.Equal(beforePolicy, afterPolicy) {
		t.Fatalf("box policy changed in the same catalog snapshot\nbefore:\n%s\nafter:\n%s", beforePolicy, afterPolicy)
	}
	afterDockerfile, afterInstalling := installPlan()
	if beforeDockerfile != afterDockerfile || !slices.Equal(beforeInstalling, afterInstalling) {
		t.Fatalf("install plan changed in the same catalog snapshot\nbefore %v:\n%s\nafter %v:\n%s",
			beforeInstalling, beforeDockerfile, afterInstalling, afterDockerfile)
	}
}

func catalogSnapshotReaderPolicy(t *testing.T, cfg *agentbox.Config) []byte {
	t.Helper()
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil || !bytes.Contains(policy, []byte("z.example")) || !bytes.Contains(policy, []byte("/opt/reader")) {
		t.Fatalf("policy did not include the custom grant and OCI folder: %v\n%s", err, policy)
	}
	return policy
}

type catalogSnapshotReader struct {
	name string
	run  func(*testing.T)
}

func catalogSnapshotOtherReaders(cfg *agentbox.Config) []catalogSnapshotReader {
	return []catalogSnapshotReader{
		{"OCI folders", func(t *testing.T) {
			runtime, err := agentbox.FeatureRuntimeOf(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, grant := range runtime.Grants {
				if grant.Path == "/opt/reader" && !grant.ReadWrite && strings.Contains(grant.Note, "/opt/reader/z") {
					return
				}
			}
			t.Fatalf("OCI folders did not use the custom binary order: %+v", runtime.Grants)
		}},
		{"inspect", func(t *testing.T) {
			got, errText := buildInspectEffectiveCaps(cfg)
			if errText != "" || len(got) != 1 || got[0].Name != catalogSnapshotReaderCapability || !slices.Equal(got[0].Origins, []string{"explicit", "feature"}) {
				t.Fatalf("inspect capability = %+v, %q", got, errText)
			}
		}},
		{"telemetry", func(t *testing.T) {
			got := telemetryBox(boxstate.Box{Config: cfg})
			if !slices.Equal(got.Capabilities, []string{catalogSnapshotReaderCapability}) {
				t.Fatalf("telemetry capabilities = %v", got.Capabilities)
			}
		}},
		{"operator", func(t *testing.T) {
			enabled, err := agentbox.OperatorEnabled(cfg)
			if err != nil || enabled {
				t.Fatalf("operator enabled = %v, %v", enabled, err)
			}
		}},
	}
}
