package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	digest "github.com/opencontainers/go-digest"
)

// This is the actual Dockerfile frontend with synthetic image metadata, not a
// daemon or live image build. It exercises stage resolution, context lookup,
// policy conversion and instruction dispatch without registry or VM access.
type stageFrontendClient struct {
	client.Client
	options  client.BuildOpts
	policy   *sourcepolicy.Engine
	mu       sync.Mutex
	resolved []string
}

func (c *stageFrontendClient) BuildOpts() client.BuildOpts { return c.options }

func (c *stageFrontendClient) Inputs(context.Context) (map[string]llb.State, error) {
	return map[string]llb.State{"context": llb.Scratch()}, nil
}

func (c *stageFrontendClient) ResolveImageConfig(ctx context.Context, ref string, _ sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	op := &pb.SourceOp{Identifier: "docker-image://" + ref}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.policy.Evaluate(ctx, op); err != nil {
		return "", "", nil, err
	}
	named, err := reference.ParseNormalizedNamed(strings.TrimPrefix(op.Identifier, "docker-image://"))
	if err != nil {
		return "", "", nil, err
	}
	pinned, ok := named.(reference.Canonical)
	if !ok {
		return "", "", nil, fmt.Errorf("frontend resolved mutable source %q", ref)
	}
	c.resolved = append(c.resolved, pinned.String())
	// A nonempty rootfs retains the source vertex in the generated graph.
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` + strings.Repeat("9", 64) + `"]},"config":{"Env":["PATH=/usr/bin:/bin"]}}`)
	return pinned.String(), pinned.Digest(), config, nil
}

func TestImageNamedContextsPreserveDockerfileStageInstructions(t *testing.T) {
	for _, base := range []struct{ name, prefix, middle string }{
		{"latest", "FROM alpine:latest AS alpine\n", ""},
		{"normalized-case", "FROM docker.io/library/alpine:latest AS ALPINE\n", ""},
		{"global-arg", "ARG BASE=alpine:latest\nFROM ${BASE} AS alpine\n", ""},
		{"later-stage", "FROM alpine:latest AS build\n", "FROM scratch AS alpine\nCOPY --from=build /built /built\n"},
		{"noncolliding-tag", "FROM alpine:3 AS alpine\n", ""},
	} {
		t.Run(base.name, func(t *testing.T) {
			const marker = "printf preserved-stage > /built"
			r := ImageRecipe{Builder: "dockerfile/1", Platform: "linux/amd64", BaseReference: "yoloctl/agentbox:ubuntu-24.04",
				Dockerfile: []byte(base.prefix + "RUN " + marker + "\n" + base.middle + "FROM yoloctl/agentbox:ubuntu-24.04 AS final\nCOPY --from=alpine /built /built\n")}
			var err error
			r.Images, err = imagejob.DockerfileImages(r)
			if err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("e", 64)
			for i := range r.Images {
				r.Images[i].Digest = "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
				r.Images[i].Source = fmt.Sprintf("yoloctl/prepared/%s-input-%d", id, i)
			}
			opt, err := privateSolveOptions(ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}, t.TempDir(), r)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &stageFrontendClient{options: client.BuildOpts{Opts: opt.FrontendAttrs, LLBCaps: pb.Caps.CapSet(pb.Caps.All()), Caps: gatewaypb.Caps.CapSet(gatewaypb.Caps.All())}, policy: sourcepolicy.NewEngine([]*spb.Policy{opt.SourcePolicy})}
			frontend, err := dockerui.NewClient(gateway)
			if err != nil {
				t.Fatal(err)
			}
			result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), r.Dockerfile, dockerfile2llb.ConvertOpt{Config: frontend.Config, Client: frontend, MetaResolver: gateway})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := result.State.Marshal(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			runs, copies := 0, 0
			for _, raw := range definition.Def {
				var op pb.Op
				if err := op.Unmarshal(raw); err != nil {
					t.Fatal(err)
				}
				if exec := op.GetExec(); exec != nil && strings.Contains(strings.Join(exec.Meta.Args, " "), marker) {
					runs++
				}
				if file := op.GetFile(); file != nil {
					for _, action := range file.Actions {
						if copy := action.GetCopy(); copy != nil && copy.Src == "/built" {
							copies++
						}
					}
				}
			}
			if runs != 1 || copies == 0 {
				t.Fatalf("frontend discarded custom stage instructions: RUN=%d COPY=%d", runs, copies)
			}
			if _, ok := opt.FrontendAttrs["context:yoloctl/agentbox:ubuntu-24.04"]; !ok {
				t.Fatal("managed base metadata workaround was removed")
			}
			if base.name == "noncolliding-tag" {
				if _, ok := opt.FrontendAttrs["context:alpine:3"]; !ok {
					t.Fatal("noncolliding external image context missing")
				}
			}
			gateway.mu.Lock()
			resolved := append([]string(nil), gateway.resolved...)
			gateway.mu.Unlock()
			if len(resolved) != 2 {
				t.Fatal("unexpected external image inventory", resolved)
			}
			// Every actual LLB image source must already be immutable and
			// admitted unchanged by the exact production source policy.
			for _, raw := range definition.Def {
				var op pb.Op
				if err := op.Unmarshal(raw); err != nil {
					t.Fatal(err)
				}
				if source := op.GetSource(); source != nil && strings.HasPrefix(source.Identifier, "docker-image://") {
					if changed, err := sourcepolicy.NewEngine([]*spb.Policy{opt.SourcePolicy}).Evaluate(t.Context(), source); changed || err != nil {
						t.Fatal("frontend graph contains an unpinned or denied image", source, err)
					}
				}
			}
		})
	}
}
