package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
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
	c := &Config{Agent: "none", Image: agentboximage.RefAgentbox, Features: []string{"github"}}
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
