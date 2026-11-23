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
	"github.com/leogr/yoloctl/internal/ocifeature"
)

func TestCatalogSnapshotConsumersLeaveCapabilitiesReadOnly(t *testing.T) {
	const name = "network.snapshot-reader"
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
	cfg := &agentbox.Config{
		Agent:        "none",
		Capabilities: []string{name},
		Features:     []string{ref},
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
	for _, reader := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"policy", func(t *testing.T) {
			policy, err := agentbox.ComposePolicy(cfg)
			if err != nil || !bytes.Contains(policy, []byte("z.example")) {
				t.Fatalf("policy did not include the custom egress rule: %v\n%s", err, policy)
			}
		}},
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
			if errText != "" || len(got) != 1 || got[0].Name != name || !slices.Equal(got[0].Origins, []string{"explicit"}) {
				t.Fatalf("inspect capability = %+v, %q", got, errText)
			}
		}},
		{"telemetry", func(t *testing.T) {
			got := telemetryBox(boxstate.Box{Config: cfg})
			if !slices.Equal(got.Capabilities, []string{name}) {
				t.Fatalf("telemetry capabilities = %v", got.Capabilities)
			}
		}},
		{"operator", func(t *testing.T) {
			enabled, err := agentbox.OperatorEnabled(cfg)
			if err != nil || enabled {
				t.Fatalf("operator enabled = %v, %v", enabled, err)
			}
		}},
	} {
		t.Run(reader.name, func(t *testing.T) {
			reader.run(t)
			if after := record(t); !bytes.Equal(before, after) {
				t.Fatalf("mutated cached capability\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}
