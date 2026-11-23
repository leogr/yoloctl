package agentbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/catalogsnapshot"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/ocifeature"
)

func TestCatalogSnapshotParity(t *testing.T) {
	const capName = "network.catalog-first"
	const featureName = "catalog-install"
	const ociRef = "registry.example/features/tool:1"
	const metadata = `{"id":"tool","version":"1","containerEnv":{"TOOL_HOME":"/opt/tool","GOROOT":"/usr/local/go","PATH":"/opt/tool/bin:/usr/local/go/bin:${PATH}"}}`
	plan := ocifeature.Plan{Locks: []ocifeature.Lock{ociLock(ociRef, "c", metadata)}}
	npm := features.AgentNpmRuntime("@openai/codex")
	state := features.AgentStateParentsRuntime("/home/sandbox/outer/state")
	working := Config{
		Agent: "codex", Features: []string{featureName, ociRef, featureName},
		Capabilities: []string{capName, capabilities.AgentNpmReadCapability, capName},
	}
	mixedPlan := []string{featureName, ociRef, featureName, npm, state}
	for _, tc := range []struct {
		name        string
		cfg         Config
		installs    []string
		badCaps     bool
		badFeatures bool
		missing     bool
	}{
		{name: "order and duplicate origins", cfg: working, installs: mixedPlan},
		{name: "missing directories", missing: true, cfg: Config{Capabilities: []string{"network.go-modules"}}, installs: []string{"go", ociRef, state}},
		{name: "unknown with valid catalogs", cfg: Config{Capabilities: []string{"absent"}}, installs: []string{featureName, "absent"}},
		{name: "malformed capability", badCaps: true, cfg: working, installs: mixedPlan},
		{name: "retired explicit first", badCaps: true, cfg: Config{Capabilities: []string{capabilities.RetiredPrivilegedCapability, capName}}},
		{name: "malformed capability first", badCaps: true, cfg: Config{Capabilities: []string{capName, capabilities.RetiredPrivilegedCapability}}},
		{name: "retired feature requirement", badCaps: true, cfg: Config{Features: []string{"retired-required"}}},
		{name: "feature requirement error", badCaps: true, cfg: Config{Features: []string{featureName}}},
		{name: "malformed feature first", badFeatures: true, cfg: working, installs: []string{featureName, "yoloctl:agent-npm:%"}},
		{name: "internal error first", badFeatures: true, installs: []string{"yoloctl:agent-npm:%", featureName}},
		{name: "OCI and internal bypass", badFeatures: true, installs: []string{ociRef, npm, state, features.PrivilegedRuntime}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("YOLOCTL_HOME", root)
			t.Cleanup(func() { catalogsnapshot.Use("") })
			if !tc.missing {
				writeCatalogFile(t, root, "capabilities", capName, catalogCapabilityYAML("/opt/tool/z", "/opt/tool/a"))
				writeCatalogFile(t, root, "capabilities", "network.catalog-last", catalogCapabilityYAML("/opt/tool/earlier-name"))
				writeCatalogFile(t, root, "features", featureName, "name: "+featureName+"\nrequires_capabilities: ["+capName+", "+capName+"]\ninstall:\n  kind: dockerfile\n  dockerfile: 'RUN echo custom'\n")
				writeCatalogFile(t, root, "features", "retired-required", "name: retired-required\nrequires_capabilities: ["+capabilities.RetiredPrivilegedCapability+"]\n")
			}
			// The already-resolved grants are always checked, even when the
			// catalog enumeration itself contributes no binaries.
			eff := capabilities.NewEffectiveSet()
			if !tc.missing {
				if err := eff.Add(capName, capabilities.OriginExplicit); err != nil {
					t.Fatal(err)
				}
			}
			if tc.badCaps {
				writeCatalogFile(t, root, "capabilities", "broken", "grants: [")
			}
			if tc.badFeatures {
				writeCatalogFile(t, root, "features", "broken", "install: [")
			}
			var plans []ocifeature.Plan
			for _, name := range tc.installs {
				if name == ociRef {
					plans = []ocifeature.Plan{plan}
				}
			}
			type result struct {
				grants        []FeatureGrant
				inOrder       []capabilities.Capability
				origins       []capabilities.EffectiveCapability
				policy        []byte
				retired       bool
				installing    []string
				installRecipe string
				hasInstall    bool
				image, recipe string
				errs          []string
			}
			run := func() result {
				r := result{grants: featureGrants(plan, eff, nil)}
				set, err := EffectiveCapabilities(&tc.cfg)
				r.errs = append(r.errs, fmt.Sprint(err))
				r.retired = errors.Is(err, capabilities.ErrRetiredPrivileged)
				if err == nil {
					r.inOrder, r.origins = set.Capabilities(), set.Effective()
					r.policy, err = compilePolicy(set, nil, featureGrants(plan, set, nil)...)
					if err != nil {
						t.Fatal(err)
					}
				}
				r.installRecipe, r.installing, r.hasInstall, err = features.InstallPlan("ubuntu:24.04", tc.installs, plans...)
				r.errs = append(r.errs, fmt.Sprint(err))
				r.image, r.recipe, _, err = agentboximage.ComposedImageRef("ubuntu:24.04", tc.installs, plans...)
				r.errs = append(r.errs, fmt.Sprint(err))
				return r
			}
			old := run()
			catalogsnapshot.Use(root)
			for range 2 {
				got := run()
				if !reflect.DeepEqual(got, old) {
					t.Fatalf("private snapshot differs from live-read path\n got %#v\nwant %#v", got, old)
				}
			}
			for _, grant := range old.grants {
				switch grant.Source {
				case "TOOL_HOME":
					if !tc.missing && (grant.ReadWrite || grant.Note != "read-only: holds /opt/tool/z, which a network grant names") {
						t.Fatalf("lost first matching binary or effective grant: %+v", grant)
					}
				case "GOROOT":
					if grant.ReadWrite != tc.badCaps {
						t.Fatalf("unexpected built-in binary protection: %+v", grant)
					}
				}
			}
			if tc.name == "order and duplicate origins" || tc.name == "OCI and internal bypass" {
				for _, err := range old.errs {
					if err != "<nil>" {
						t.Fatalf("expected success, got %s", err)
					}
				}
			}
		})
	}
}

func TestCatalogSnapshotKeepsLoadsLocal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", root)
	t.Cleanup(func() { catalogsnapshot.Use("") })
	const capName = "network.local"
	const featureName = "local-install"
	writeCatalogFile(t, root, "capabilities", capName, catalogCapabilityYAML("/opt/tool/initial"))
	writeCatalogFile(t, root, "features", featureName, "name: "+featureName+"\n")
	cfg := Config{Capabilities: []string{capName}}
	catalogsnapshot.Use(root)
	if _, err := EffectiveCapabilities(&cfg); err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, root, "capabilities", capName, "grants: [")
	set, err := EffectiveCapabilities(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Capabilities()[0].Network[0].Binaries[0]; got != "/opt/tool/initial" {
		t.Fatalf("later calculation received %q", got)
	}

	if _, _, _, err := features.InstallPlan("base", []string{features.AgentNpmRuntime("codex")}); err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, root, "features", featureName, "install: [")
	if _, _, _, err := features.InstallPlan("base", []string{featureName}); err == nil {
		t.Fatal("ordinary lookup did not load its catalog")
	}
	writeCatalogFile(t, root, "features", featureName, "name: "+featureName+"\n")
	if _, _, _, err := features.InstallPlan("base", []string{featureName}); err == nil {
		t.Fatal("snapshot lost the original load error")
	}
	catalogsnapshot.Use(root)
	if _, err := EffectiveCapabilities(&cfg); err == nil {
		t.Fatal("new snapshot reused an old capability load")
	}
	if _, _, _, err := features.InstallPlan("base", []string{featureName}); err != nil {
		t.Fatal(err)
	}
}

func writeCatalogFile(t *testing.T, root, kind, name, data string) {
	t.Helper()
	dir := filepath.Join(root, kind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func catalogCapabilityYAML(first string, more ...string) string {
	return fmt.Sprintf("grants:\n  network:\n    egress:\n      - hosts: [fixture.invalid]\n        ports: [443]\n        binaries: [%s]\n", strings.Join(append([]string{first}, more...), ", "))
}
