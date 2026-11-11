package agentboximage

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/assets"
)

func TestPreparedCompositionKeepsDesiredRecipeSeparateFromBaseDigest(t *testing.T) {
	p, err := PrepareComposed(t.Context(), RefAgentbox, []string{"go"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ref, df, _, err := ComposedImageRef(RefAgentbox, []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	if p.DesiredRef != ref || p.BaseRef != RefAgentbox || string(p.Recipe.Dockerfile) != df+composedLabels(RefAgentbox) {
		t.Fatal("prepared and applied recipes disagree")
	}
	if len(p.Features) != 1 || p.Features[0] != "go" {
		t.Fatalf("progress feature names %v", p.Features)
	}
	a, err := p.BindBase("sha256:"+strings.Repeat("a", 64), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.BindBase("sha256:"+strings.Repeat("b", 64), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	x, _ := a.Identity()
	y, _ := b.Identity()
	if x == y {
		t.Fatal("moving base digest did not change immutable input identity")
	}
	a.Dockerfile[0] = 'X'
	if p.Recipe.Dockerfile[0] == 'X' {
		t.Fatal("bound recipe aliases prepared bytes")
	}
	if p.DesiredRef != ref {
		t.Fatal("base binding changed drift identity")
	}
}

func TestPrepareManagedBaseCopiesEmbeddedContextAndModes(t *testing.T) {
	p, err := PrepareManagedBase(true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := assets.AgentboxContexts.ReadFile(agentboxContext + "/yoloctl-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	if p.DesiredRef != RefAgentbox || p.BaseRef != "ubuntu:24.04" || !p.Recipe.Force || p.Recipe.NoCache || p.Recipe.FileModes["yoloctl-entrypoint.sh"] != 0644 || !bytes.Equal(raw, p.Recipe.Files["yoloctl-entrypoint.sh"]) {
		t.Fatal("managed context was not preserved")
	}
	r, err := p.BindBase("sha256:"+strings.Repeat("a", 64), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	r.Files["yoloctl-entrypoint.sh"][0] = 'X'
	if bytes.Equal(r.Files["yoloctl-entrypoint.sh"], p.Recipe.Files["yoloctl-entrypoint.sh"]) {
		t.Fatal("bound recipe aliases context files")
	}
	if _, err := p.BindBase("ubuntu:latest", "linux/amd64"); err == nil {
		t.Fatal("mutable FROM accepted as immutable")
	}
}

func TestPreparePolicyOnlyDoesNotInventBuild(t *testing.T) {
	p, err := PrepareComposed(t.Context(), RefAgentbox, []string{"github"}, Options{Force: true})
	if err != nil || p.DesiredRef != RefAgentbox || len(p.Recipe.Dockerfile) != 0 {
		t.Fatal(p, err)
	}
}
