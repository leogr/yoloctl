package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/leogr/yoloctl/internal/imagejob"
)

type localResolverFixture struct {
	read func(context.Context, string) (imagejob.LocalTarget, string, error)
}

func (f localResolverFixture) ResolveLocal(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
	return f.read(ctx, ref)
}

func TestLocalImageCheckpointDistinguishesAbsenceFromFailedRead(t *testing.T) {
	want := errors.New("local graph unavailable")
	for _, cause := range []error{errdefs.ErrNotFound, want, context.Canceled} {
		calls := 0
		backend := localResolverFixture{read: func(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
			calls++
			deadline, bounded := ctx.Deadline()
			if !bounded || time.Until(deadline) > 10*time.Second || ref != "custom/image:one" {
				t.Fatal("local read lost its bound/source")
			}
			return imagejob.LocalTarget{}, "", cause
		}}
		reply, err := resolveLocalImageCheckpoint(t.Context(), backend, ImageRequest{Action: "resolve-local", Reference: "custom/image:one"})
		if cause == errdefs.ErrNotFound {
			if err != nil || !reply.LocalMissing || reply.LocalTarget != nil {
				t.Fatal("genuine source absence was not preserved", reply, err)
			}
		} else if !errors.Is(err, cause) || reply.LocalMissing {
			t.Fatal("failed/incomplete read became cold-fetch permission", reply, err)
		}
		if calls != 1 {
			t.Fatal("failed local read was retried")
		}
	}
}

func TestLocalImageCheckpointKeepsExactDescriptorAndRejectsRemoteFallback(t *testing.T) {
	want := imagejob.LocalTarget{Digest: "sha256:" + strings.Repeat("a", 64), Size: 123, MediaType: "application/vnd.oci.image.manifest.v1+json"}
	calls := 0
	backend := localResolverFixture{read: func(context.Context, string) (imagejob.LocalTarget, string, error) {
		calls++
		return want, "linux/amd64", nil
	}}
	req := ImageRequest{Action: "resolve-local", Reference: "custom/image:one"}
	reply, err := resolveLocalImageCheckpoint(t.Context(), backend, req)
	if err != nil || reply.LocalTarget == nil || *reply.LocalTarget != want || reply.Platform != "linux/amd64" || reply.Digest != want.Digest || reply.LocalMissing {
		t.Fatal("local target was replaced by a config ID/tag", reply, err)
	}
	req.Remote = true
	if _, err := resolveLocalImageCheckpoint(t.Context(), backend, req); err == nil || calls != 1 {
		t.Fatal("contradictory request reached a remote/local resolver", err, calls)
	}
	if _, err := resolveLocalImageCheckpoint(t.Context(), struct{}{}, ImageRequest{}); err == nil {
		t.Fatal("unsupported backend fell back to registry resolution")
	}
}
