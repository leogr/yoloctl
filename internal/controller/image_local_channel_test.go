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
			if _, bounded := ctx.Deadline(); bounded || ref != "custom/image:one" {
				t.Fatal("local read gained a fixed bound or lost its source")
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

// The checkpoint's read follows the worker context: the caller's deadline
// reaches it unchanged and the caller's cancellation ends it.
func TestLocalImageCheckpointFollowsCallerContext(t *testing.T) {
	caller, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	want, _ := caller.Deadline()
	backend := localResolverFixture{read: func(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
		if got, bounded := ctx.Deadline(); !bounded || !got.Equal(want) {
			t.Fatal("local read does not carry exactly the caller's deadline", got, want)
		}
		return imagejob.LocalTarget{}, "", errdefs.ErrNotFound
	}}
	if reply, err := resolveLocalImageCheckpoint(caller, backend, ImageRequest{Action: "resolve-local", Reference: "custom/image:one"}); err != nil || !reply.LocalMissing {
		t.Fatal("absence under a caller deadline was not preserved", reply, err)
	}
	stopped, stop := context.WithCancel(t.Context())
	started := make(chan struct{})
	blocked := localResolverFixture{read: func(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
		close(started)
		<-ctx.Done()
		return imagejob.LocalTarget{}, "", ctx.Err()
	}}
	result := make(chan error, 1)
	go func() {
		_, err := resolveLocalImageCheckpoint(stopped, blocked, ImageRequest{Action: "resolve-local", Reference: "custom/image:one"})
		result <- err
	}()
	<-started
	stop()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("caller cancellation did not end the local read", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled local read did not return")
	}
}

// In an owned setup the resolve-local read has one fail-safe (the setup
// outlives its viewer); otherwise it follows the worker context unchanged.
func TestLocalImageCheckpointOwnedSetupHasFailSafe(t *testing.T) {
	old := controllerFailSafe
	controllerFailSafe = 200 * time.Millisecond
	t.Cleanup(func() { controllerFailSafe = old })
	blocked := localResolverFixture{read: func(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
		<-ctx.Done()
		return imagejob.LocalTarget{}, "", ctx.Err()
	}}
	read, done := localImageCheckpointContext(t.Context(), true)
	start := time.Now()
	_, err := resolveLocalImageCheckpoint(read, blocked, ImageRequest{Action: "resolve-local", Reference: "custom/image:one"})
	done()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Fatal("owned-setup local read did not end at its fail-safe", err, time.Since(start))
	}
	plain, release := localImageCheckpointContext(t.Context(), false)
	defer release()
	if _, bounded := plain.Deadline(); bounded {
		t.Fatal("an ordinary local read gained a fixed bound")
	}
}

// ownershipBackend is the execution's image backend in the ownership tests:
// ResolveLocal answers after a delay, Validate never answers until its
// context ends.
type ownershipBackend struct {
	ImageBackend
	resolveDelay time.Duration
	deadlines    chan bool
}

func (b ownershipBackend) ResolveLocal(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
	_, bounded := ctx.Deadline()
	b.deadlines <- bounded
	select {
	case <-time.After(b.resolveDelay):
		return imagejob.LocalTarget{}, "", errdefs.ErrNotFound
	case <-ctx.Done():
		return imagejob.LocalTarget{}, "", ctx.Err()
	}
}

func (b ownershipBackend) Validate(ctx context.Context, _ PinnedImage) error {
	_, bounded := ctx.Deadline()
	b.deadlines <- bounded
	<-ctx.Done()
	return ctx.Err()
}

// Through the server's dispatch, the local image reads follow actual setup
// ownership: before effect permission a read slower than the fail-safe still
// succeeds under its caller; once the setup is owned and its viewer has left,
// a stalled validation ends at the fail-safe.
func TestLocalImageReadsFollowSetupOwnership(t *testing.T) {
	old := controllerFailSafe
	controllerFailSafe = 150 * time.Millisecond
	t.Cleanup(func() { controllerFailSafe = old })
	backend := ownershipBackend{resolveDelay: 400 * time.Millisecond, deadlines: make(chan bool, 4)}
	s := &Server{Images: &ImageJobs{backend: backend}}
	conn, cancelConn := context.WithCancel(t.Context())
	defer cancelConn()
	owner := newSetupOwner(true, conn, func() {}, &clientOutput{})
	reply, err := s.localImageRead(t.Context(), owner, ImageRequest{Action: "resolve-local", Reference: "custom/image:one"}, nil)
	if err != nil || !reply.LocalMissing || <-backend.deadlines {
		t.Fatal("a slow read before effect permission did not follow its caller", reply, err)
	}
	if err := owner.authorize(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	owner.detach() // the viewer leaves: the owned setup continues
	result := make(chan error, 1)
	go func() {
		_, err := s.localImageRead(t.Context(), owner, ImageRequest{Action: "validate", JobID: "job"}, &RetainedImage{})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || !<-backend.deadlines {
			t.Fatal("a stalled validation in the owned setup did not end at its fail-safe", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled validation after the viewer left was never bounded")
	}
}
