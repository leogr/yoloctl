package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
)

func TestExternalImagePolicyUsesBuildKitEngineAndRejectsUnboundSources(t *testing.T) {
	r := recipe("sources")
	r.Dockerfile = []byte("ARG BASE=ubuntu:24.04\nFROM ${BASE} AS custom-stage\nRUN touch /fresh\nFROM yoloctl/agentbox\nCOPY --from=custom-stage /fresh /fresh\nCOPY --from=busybox:1 /bin/true /true\nRUN --mount=from=alpine:3,target=/source true\n")
	inputs, err := imagejob.DockerfileImages(r)
	if err != nil || len(inputs) != 4 {
		t.Fatal(inputs, err)
	}
	for i := range inputs {
		inputs[i].Digest = "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		inputs[i].Source = "yoloctl/prepared/unique-input-" + string(rune('a'+i))
	}
	r.Images = inputs
	policy, err := imageSourcePolicy(r)
	if err != nil {
		t.Fatal(err)
	}
	engine := sourcepolicy.NewEngine([]*spb.Policy{policy})
	for _, input := range inputs {
		op := &pb.SourceOp{Identifier: "docker-image://" + input.Reference}
		mutated, err := engine.Evaluate(t.Context(), op)
		if err != nil || !mutated || !strings.Contains(op.Identifier, input.Digest) || !strings.Contains(op.Identifier, input.Source) {
			t.Fatal(op, mutated, err)
		}
		if changed, err := engine.Evaluate(t.Context(), op); changed || err != nil {
			t.Fatal("immutable source not stable", op, err)
		}
	}
	for _, id := range []string{"docker-image://docker.io/library/unknown:latest", "docker-image://docker.io/library/ubuntu:24.04@sha256:" + strings.Repeat("f", 64)} {
		if _, err := engine.Evaluate(t.Context(), &pb.SourceOp{Identifier: id}); !errors.Is(err, sourcepolicy.ErrSourceDenied) {
			t.Fatal("unbound source accepted", id, err)
		}
	}
	r.Images = r.Images[1:]
	if _, err := imageSourcePolicy(r); err == nil {
		t.Fatal("missing dependency accepted")
	}
}

func TestSyntaxFrontendIsResolvedAndPinnedByTheProductionPolicy(t *testing.T) {
	r := recipe("syntax")
	r.Dockerfile = []byte("# syntax=docker/dockerfile:1\nFROM scratch\nRUN false\n")
	inputs, err := imagejob.DockerfileImages(r)
	if err != nil || len(inputs) != 1 {
		t.Fatal(inputs, err)
	}
	inputs[0].Digest = r.BaseDigest
	r.Images = inputs
	policy, err := imageSourcePolicy(r)
	if err != nil {
		t.Fatal(err)
	}
	op := &pb.SourceOp{Identifier: "docker-image://docker.io/docker/dockerfile:1"}
	if changed, err := sourcepolicy.NewEngine([]*spb.Policy{policy}).Evaluate(t.Context(), op); err != nil || !changed || !strings.Contains(op.Identifier, r.BaseDigest) {
		t.Fatal("syntax frontend denied or mutable", op, err)
	}
	r.Images = nil
	r.BaseReference = "scratch"
	// A frontend must never silently inherit a FROM/scratch digest.
	if _, err := imageSourcePolicy(r); err == nil {
		t.Fatal("unresolved syntax frontend inherited the base digest")
	}
}

func TestEmbeddedManagedDockerfileFrontendPassesPrivateSolvePolicy(t *testing.T) {
	prepared, err := agentboximage.PrepareManagedBase(false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := prepared.BindBase("sha256:"+strings.Repeat("a", 64), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	r.BaseReference = prepared.BaseRef
	r.Images, err = imagejob.DockerfileImages(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Images {
		r.Images[i].Digest = "sha256:" + strings.Repeat("b", 64)
	}
	id := strings.Repeat("e", 64)
	opt, err := privateSolveOptions(ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}, t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	source := &pb.SourceOp{Identifier: "docker-image://docker.io/docker/dockerfile:1"}
	if _, err := sourcepolicy.NewEngine([]*spb.Policy{opt.SourcePolicy}).Evaluate(t.Context(), source); err != nil || !strings.Contains(source.Identifier, "@sha256:") {
		t.Fatal("production frontend denied or left mutable", source, err)
	}
}

func TestPrivateSolveOptionsBindsConvertedImagesAsNamedContexts(t *testing.T) {
	rust := "docker.io/library/rust:1.99.0-bookworm@sha256:" + strings.Repeat("1", 64)
	base := "sha256:" + strings.Repeat("2", 64)
	id := strings.Repeat("f", 64)
	r := ImageRecipe{Builder: "dockerfile/1", Platform: "linux/arm64/v8", BaseReference: "yoloctl/agentbox:ubuntu-24.04", BaseDigest: base,
		Dockerfile: []byte("FROM " + rust + " AS airc\nRUN true\nFROM yoloctl/agentbox:ubuntu-24.04 AS base\nCOPY --from=airc /x /x\n"),
		Images: []imagejob.ImageInput{
			{Reference: rust, Source: rust, Platform: "linux/arm64/v8", Digest: "sha256:" + strings.Repeat("1", 64)},
			{Reference: "docker.io/yoloctl/agentbox:ubuntu-24.04", Source: "yoloctl/prepared/" + id + "-input-1", Platform: "linux/arm64/v8", Digest: base},
		}}
	opt, err := privateSolveOptions(ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}, t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	want := "docker-image://docker.io/yoloctl/prepared/" + id + "-input-1:latest@" + base
	if got := opt.FrontendAttrs["context:yoloctl/agentbox:ubuntu-24.04"]; got != want {
		t.Fatalf("base named context = %q, want %q", got, want)
	}
	for k := range opt.FrontendAttrs {
		if strings.HasPrefix(k, "context:") && strings.Contains(k, "rust") {
			t.Fatalf("an already pinned image needs no named context: %s", k)
		}
	}
	// The frontend resolves the bound locator directly; the policy admits it
	// without any conversion.
	source := &pb.SourceOp{Identifier: want}
	if mut, err := sourcepolicy.NewEngine([]*spb.Policy{opt.SourcePolicy}).Evaluate(t.Context(), source); err != nil || mut {
		t.Fatal("named context denied or converted", mut, err)
	}
}
