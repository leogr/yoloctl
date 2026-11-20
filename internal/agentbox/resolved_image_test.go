package agentbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestEffectiveFeaturesStrict_UnionsProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "claude", Features: []string{"go"}}
	eff, err := EffectiveFeaturesStrict(c)
	if err != nil {
		t.Fatalf("EffectiveFeaturesStrict: %v", err)
	}
	// box feature first, then the profile's RequiresFeatures (claude, github
	// and its reporter).
	if got := strings.Join(eff, ","); got != "go,claude,github,yolo.agent.observe" {
		t.Errorf("eff = %q, want go,claude,github,yolo.agent.observe", got)
	}
}

func TestEffectiveFeaturesStrict_ErrorsOnMissingProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "nonexistent-agent", Features: []string{"go"}}
	if _, err := EffectiveFeaturesStrict(c); err == nil {
		t.Fatal("expected an error for an unresolvable agent profile")
	}
}

// The lenient view must NOT error on a missing profile (read-only paths).
func TestEffectiveFeatures_LenientOnMissingProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "nonexistent-agent", Features: []string{"go"}}
	if got := effectiveFeatures(c); strings.Join(got, ",") != "go" {
		t.Errorf("lenient eff = %v, want [go] (box features only)", got)
	}
}

// End-state model: a vanilla agent box composes its CLI feature onto the plain
// base — there is no prebuilt variant fast-path anymore.
func TestResolvedImage_VanillaAgentBoxComposes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "claude", Image: agentboximage.RefAgentbox}
	ref, composed, err := ResolvedImage(c)
	if err != nil {
		t.Fatal(err)
	}
	if !composed || !strings.HasPrefix(ref, agentboximage.ComposedImageRepo+":") {
		t.Errorf("got ref=%q composed=%v, want a composed ref (the claude CLI installs onto the base)", ref, composed)
	}
}

func TestResolvedImage_ComposesExtraFeature(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "claude", Image: agentboximage.RefAgentbox, Features: []string{"go"}}
	ref, composed, err := ResolvedImage(c)
	if err != nil {
		t.Fatal(err)
	}
	if !composed || !strings.HasPrefix(ref, agentboximage.ComposedImageRepo+":") {
		t.Errorf("claude+go: got ref=%q composed=%v, want a composed ref", ref, composed)
	}
}

// A generic-shell box whose only feature is policy-only stays on the base.
func TestResolvedImage_PolicyOnlyStaysOnBase(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Agent: "none", Image: agentboximage.RefAgentbox, Features: []string{"pypi"}}
	ref, composed, err := ResolvedImage(c)
	if err != nil {
		t.Fatal(err)
	}
	if composed || ref != agentboximage.RefAgentbox {
		t.Errorf("got ref=%q composed=%v, want the plain base as-is", ref, composed)
	}
}

// End-to-end Phase-2 wiring: adding a feature moves the resolved image, and the
// reconciler's Diff (fed that resolved image) recreates the box onto it.
func TestResolvedImage_FeatureChangeTriggersRecreate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	without := &Config{Name: "coder", Space: "default", Agent: "claude", Image: agentboximage.RefAgentbox}
	withGo := &Config{Name: "coder", Space: "default", Agent: "claude", Image: agentboximage.RefAgentbox, Features: []string{"go"}}

	imgWithout, _, err := ResolvedImage(without)
	if err != nil {
		t.Fatal(err)
	}
	imgWithGo, _, err := ResolvedImage(withGo)
	if err != nil {
		t.Fatal(err)
	}
	if imgWithout == imgWithGo {
		t.Fatal("adding --features go must change the resolved image")
	}

	// The container runs the OLD composed image; reconciling the new manifest
	// must recreate onto the new composed image.
	actual := ActualState{Exists: true, Image: imgWithout, PolicyContent: []byte("p")}
	plan := Diff(withGo, actual, imgWithGo, []byte("p"))
	if !plan.NeedsRecreate || !strings.Contains(plan.RecreateReason, "image") {
		t.Errorf("expected an image recreate, got %+v", plan)
	}
}

// leogr's rule end to end (archyAstra): two boxes with the same features,
// Codex as the agent in only one. The agent's own package goes to the managed
// prefix and the image names differ; as a feature tool it stays in the user's.
func TestResolvedImage_AgentNpmPackageSelectsTheManagedPrefix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	features := []string{"codex", "github"}
	tool := &Config{Agent: "none", Image: agentboximage.RefAgentbox, Features: features}
	agent := &Config{Agent: "codex", Image: agentboximage.RefAgentbox, Features: features}
	recipe := func(c *Config) (string, string) {
		t.Helper()
		names, err := RuntimeFeatures(c)
		if err != nil {
			t.Fatal(err)
		}
		ref, dockerfile, _, err := agentboximage.ComposedImageRef(c.Image, names)
		if err != nil {
			t.Fatal(err)
		}
		if resolved, _, err := ResolvedImage(c); err != nil || resolved != ref {
			t.Fatalf("drift ref %q (%v) differs from the build ref %q", resolved, err, ref)
		}
		return ref, dockerfile
	}
	toolRef, toolRecipe := recipe(tool)
	agentRef, agentRecipe := recipe(agent)
	if toolRef == agentRef {
		t.Fatal("the same features with and without the Codex agent share an image")
	}
	if !strings.Contains(toolRecipe, "@openai/codex@latest --prefix=/home/sandbox/.npm-global") || strings.Contains(toolRecipe, "/opt/yoloctl/npm") {
		t.Fatalf("Codex as a tool left the user's prefix:\n%s", toolRecipe)
	}
	if !strings.Contains(agentRecipe, "--prefer-online -- '@openai/codex@latest'") || strings.Contains(agentRecipe, "@openai/codex@latest --prefix=/home/sandbox/.npm-global") {
		t.Fatalf("Codex as the agent is not in the managed prefix:\n%s", agentRecipe)
	}
	// A native agent and an agentless box get no agent npm runtime feature.
	for _, c := range []*Config{{Agent: "agy", Image: agentboximage.RefAgentbox}, tool} {
		names, err := RuntimeFeatures(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "yoloctl:agent-npm:") {
				t.Fatalf("agent %q got %s", c.Agent, n)
			}
		}
	}
}

// A custom agent whose package no catalog feature installs gets the agent
// install after the OCI Feature installs, which may supply Node.js and npm
// on a custom base (archyAstra r1).
func TestResolvedImage_UnsuppliedAgentNpmInstallFollowsOCIFeatures(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir, err := layout.AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my-agent.yaml"), []byte("name: my-agent\nimage: custom/base:1\ncommand: my-cli\nnpm_package: my-cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{Agent: "my-agent", Image: "custom/base:1", Features: []string{ociGoRef}, FeatureLocks: ociGoRustLocks()[:1]}
	plan, err := c.FeaturePlan()
	if err != nil {
		t.Fatal(err)
	}
	names, err := RuntimeFeatures(c)
	if err != nil {
		t.Fatal(err)
	}
	_, dockerfile, installs, err := agentboximage.ComposedImageRef(c.Image, names, plan)
	if err != nil || !installs {
		t.Fatalf("installs=%v err=%v", installs, err)
	}
	oci := strings.Index(dockerfile, "# OCI Feature: "+ociGoRef)
	agent := strings.Index(dockerfile, "npm install -g --prefix=/opt/yoloctl/npm --cache=\"/var/cache/yoloctl/npm/$(id -u)\" --prefer-online -- 'my-cli@latest'")
	if oci < 0 || agent < oci || strings.Count(dockerfile, "npm install -g ") != 1 {
		t.Fatalf("OCI Feature at %d, agent install at %d:\n%s", oci, agent, dockerfile)
	}
	if !strings.HasSuffix(strings.TrimRight(dockerfile[:strings.Index(dockerfile, "\nRUN find /home/sandbox -xdev")], "\n"), "ENV PATH=/opt/yoloctl/npm/bin:$PATH") {
		t.Fatalf("the agent folder is not first on PATH after every install:\n%s", dockerfile)
	}
}

func TestResolvedImage_NestedStateParents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, agent := range []string{"agy", "claude", "codex", "gemini", "opencode", "pi", "none"} {
		c := &Config{Agent: agent, Account: "account", Image: agentboximage.RefAgentbox}
		if agent == "none" {
			c.Account = "none"
		}
		names, err := RuntimeFeatures(c)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, name := range names {
			found = found || features.IsAgentStateParentsRuntime(name)
		}
		if found != (agent == "opencode") {
			t.Errorf("agent %s has state setup=%v: %v", agent, found, names)
		}
	}

	dir, err := layout.AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := []byte("name: my-agent\nimage: custom/base:1\ncommand: my-cli\nstate_container_path: /home/sandbox/config/tool/data\n")
	if err := os.WriteFile(filepath.Join(dir, "my-agent.yaml"), profile, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{Agent: "my-agent", Account: "account", Image: "custom/base:1"}
	names, err := RuntimeFeatures(c)
	if err != nil {
		t.Fatal(err)
	}
	ref, recipe, installs, err := agentboximage.ComposedImageRef(c.Image, names)
	if err != nil || !installs {
		t.Fatalf("state-only composition = %q, %v, %v", ref, installs, err)
	}
	const prepared = "FROM custom/base:1\nARG YOLOCTL_IMAGE_REFRESH\nUSER root\n"
	if !strings.HasPrefix(recipe, prepared) || !strings.Contains(recipe, `"/home/sandbox/config/tool"`) ||
		!strings.Contains(recipe, "chown -h --") || strings.Contains(recipe, "find /home/sandbox") || strings.Contains(recipe, "yoloctl-entrypoint") {
		t.Fatalf("state-only recipe must own only the new parents:\n%s", recipe)
	}
	if got, _, err := ResolvedImage(c); err != nil || got != ref {
		t.Fatalf("resolved image = %q, %v, want %q", got, err, ref)
	}
}
