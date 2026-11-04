package agentbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// TestCapabilitiesDoNotAffectMounts is the "capabilities cannot bypass space
// mount authority" guard. Egress/filesystem capabilities feed ONLY the
// openshell policy;
// the host bind set (buildMounts) depends solely on Workdir + Mounts.
// Adding network capabilities must not introduce a host mount.
func TestCapabilitiesDoNotAffectMounts(t *testing.T) {
	base := &Config{
		Name: "x", Space: "default", Features: []string{"claude"},
		Workdir: NewEphemeralWorkdir("x"),
	}
	more := &Config{
		Name: "x", Space: "default", Features: []string{"claude", "github", "npm"},
		Workdir: NewEphemeralWorkdir("x"),
	}

	if !sameMountSet(buildMounts(base), buildMounts(more)) {
		t.Errorf("adding network capabilities changed the mount set:\n base=%v\n more=%v",
			buildMounts(base), buildMounts(more))
	}
}

func sameMountSet(a, b []container.Mount) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEffectiveCapabilitiesFromConfig pins the manifest → capability mapping
// the composer uses: workdir-write is an explicit capability (what create/run
// add by default); features contribute network capabilities. A manifest with no
// workdir-write capability yields a read-only workdir; egress is independent.
func TestEffectiveCapabilitiesFromConfig(t *testing.T) {
	wd := capabilities.WorkdirWriteCapability

	rw, err := EffectiveCapabilities(&Config{Name: "x", Space: "default", Features: []string{"claude", "github"}, Capabilities: []string{wd}})
	if err != nil {
		t.Fatal(err)
	}
	if !rw.Has(wd) {
		t.Error("box with the fs.workdir.write capability should grant it")
	}
	if !rw.Has("network.anthropic") || !rw.Has("network.github") {
		t.Errorf("features did not contribute network caps: %v", rw.Effective())
	}

	ro, err := EffectiveCapabilities(&Config{Name: "x", Space: "default", Features: []string{"claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if ro.Has(wd) {
		t.Error("box without the fs.workdir.write capability must not grant it (read-only workdir)")
	}
	if !ro.Has("network.anthropic") {
		t.Error("workdir access must not affect network egress")
	}

	// A box with no network-bearing features/agent and only the workdir-write
	// capability has no egress (default-deny) but keeps workdir write.
	ne, err := EffectiveCapabilities(&Config{Name: "x", Space: "default", Capabilities: []string{wd}})
	if err != nil {
		t.Fatal(err)
	}
	if ne.Has("network.anthropic") {
		t.Error("a box with no network capabilities must not grant egress")
	}
	if !ne.Has(wd) {
		t.Error("workdir write must be independent of egress")
	}
}

// TestCustomEgressCapabilityComposesIntoPolicy is the round-trip guard for the
// high-level custom-capability authoring schema. A user-authored
// grants.network.egress capability, once placed on a
// box's Capabilities, must compose into a valid policy through the SAME
// composer the built-ins use, emitting its endpoint with a non-empty ports
// LIST (the rego matches endpoint.ports[_]).
func TestCustomEgressCapabilityComposesIntoPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network.mytools.yaml"), []byte(`schema_version: 1
name: network.mytools
grants:
  network:
    egress:
      - hosts: [a.example.com, b.example.com]
        ports: [443]
        binaries: [/usr/bin/mytool]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Name: "coder", Space: "default", Capabilities: []string{"network.mytools", capabilities.WorkdirWriteCapability}}
	got, err := ComposePolicy(cfg)
	if err != nil {
		t.Fatalf("ComposePolicy with a custom egress capability: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal composed policy: %v", err)
	}
	nps, ok := doc["network_policies"].(map[string]any)
	if !ok {
		t.Fatalf("composed policy has no network_policies: %#v", doc["network_policies"])
	}
	pol, ok := nps["mytools"].(map[string]any)
	if !ok {
		t.Fatalf("custom capability did not produce the derived policy key %q: %v", "mytools", nps)
	}
	eps, ok := pol["endpoints"].([]any)
	if !ok || len(eps) != 2 {
		t.Fatalf("want 2 endpoints, got %#v", pol["endpoints"])
	}
	for i, e := range eps {
		ep := e.(map[string]any)
		if _, scalar := ep["port"]; scalar {
			t.Errorf("endpoint %d emits scalar `port`; the rego matches endpoint.ports[_]", i)
		}
		ports, ok := ep["ports"].([]any)
		if !ok || len(ports) != 1 {
			t.Errorf("endpoint %d ports must be a list [443], got %#v", i, ep["ports"])
		}
	}
}

// TestEffectiveCapabilitiesExplicit: capabilities listed directly on the
// manifest are in the effective set with OriginExplicit.
func TestEffectiveCapabilitiesExplicit(t *testing.T) {
	eff, err := EffectiveCapabilities(&Config{Name: "x", Space: "default", Capabilities: []string{"network.github"}})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Has("network.github") {
		t.Fatal("explicit capability not in effective set")
	}
	for _, e := range eff.Effective() {
		if e.Capability.Name == "network.github" {
			if len(e.Origins) == 0 || e.Origins[0] != capabilities.OriginExplicit {
				t.Errorf("network.github origins = %v, want [explicit]", e.Origins)
			}
		}
	}
}

// TestEffectiveCapabilitiesViaAgentFeatures: a box that names an agent inherits
// the agent's egress through its REQUIRED FEATURES — not an agent-level
// capability. Selecting agent claude pulls in the claude feature (the profile's
// requires_features), which requires network.anthropic, so the capability lands
// in the effective set tagged OriginFeature. This is the single capability
// boundary in force: agent → features → capabilities.
func TestEffectiveCapabilitiesViaAgentFeatures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	eff, err := EffectiveCapabilities(&Config{Name: "x", Space: "default", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Has("network.anthropic") {
		t.Fatal("agent egress not inherited via the profile's required features")
	}
	gotFeature := false
	for _, e := range eff.Effective() {
		if e.Capability.Name == "network.anthropic" {
			for _, o := range e.Origins {
				if o == capabilities.OriginFeature {
					gotFeature = true
				}
			}
		}
	}
	if !gotFeature {
		t.Error("network.anthropic should carry OriginFeature: it comes from the claude feature pulled in by the profile's requires_features")
	}
}
