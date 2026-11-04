package controller

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	digest "github.com/opencontainers/go-digest"
)

// These adapters withhold an actual containerd service reply until the
// validation's own context ends. Callers deliberately supply no deadline.
// The observer timer only fails a hung test; it cannot cancel the operation
// or manufacture a successful bounded-validation result.
type validationReadBarrier struct {
	armed   atomic.Bool
	entered chan time.Time
	abort   chan struct{}
}

func (b *validationReadBarrier) wait(ctx context.Context) error {
	deadline, bounded := ctx.Deadline()
	select {
	case b.entered <- deadline:
	default:
	}
	if !bounded {
		return errors.New("actual validation issued an unbounded containerd read")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.abort:
		return errors.New("validation fixture aborted after test failure")
	}
}

type validationImageReads struct {
	images.Store
	barrier *validationReadBarrier
}

func (s validationImageReads) Get(ctx context.Context, name string) (images.Image, error) {
	if s.barrier.armed.Load() && strings.HasPrefix(name, "docker.io/yoloctl/prepared/") {
		return images.Image{}, s.barrier.wait(ctx)
	}
	return s.Store.Get(ctx, name)
}

type validationLeaseReads struct {
	leases.Manager
	barrier *validationReadBarrier
}

func (s validationLeaseReads) ListResources(ctx context.Context, l leases.Lease) ([]leases.Resource, error) {
	if s.barrier.armed.Load() {
		return nil, s.barrier.wait(ctx)
	}
	return s.Manager.ListResources(ctx, l)
}

type validationContentReads struct {
	content.Store
	barrier *validationReadBarrier
	layer   digest.Digest
}

func (s validationContentReads) Info(ctx context.Context, id digest.Digest) (content.Info, error) {
	if s.barrier.armed.Load() && id == s.layer {
		return content.Info{}, s.barrier.wait(ctx)
	}
	return s.Store.Info(ctx, id)
}

// Only Build is wrapped, to withhold the subsequent post-build read. Validate
// remains the real NativeImageBackend method invoked by actual ImageJobs.
type validationAfterBuild struct {
	*NativeImageBackend
	barrier *validationReadBarrier
}

func (b validationAfterBuild) Build(ctx context.Context, r ImageRecipe, phase ImagePhase) (PinnedImage, error) {
	pin, err := b.NativeImageBackend.Build(ctx, r, phase)
	if err == nil {
		b.barrier.armed.Store(true)
	}
	return pin, err
}

func TestLocalPinValidationOwnsDeadlineThroughJobAndConsumer(t *testing.T) {
	for _, phase := range []string{"post-build-image", "consumer-lease", "consumer-content"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			if localImageTimeout <= 0 || localImageTimeout > 30*time.Second {
				t.Fatal("validation read bound is not finite within thirty seconds")
			}
			f := newLocalRetainFixture(t, false, false)
			barrier := &validationReadBarrier{entered: make(chan time.Time, 1), abort: make(chan struct{})}
			t.Cleanup(func() { close(barrier.abort) })
			imageStore := images.Store(f.images)
			leaseService := leases.Manager(f.leases)
			contentStore := content.Store(f.content)
			switch phase {
			case "post-build-image":
				imageStore = validationImageReads{Store: imageStore, barrier: barrier}
			case "consumer-lease":
				leaseService = validationLeaseReads{Manager: leaseService, barrier: barrier}
			case "consumer-content":
				contentStore = validationContentReads{Store: contentStore, barrier: barrier, layer: f.layer.Digest}
			}
			client, err := cd.New("", cd.WithServices(cd.WithImageStore(imageStore), cd.WithLeasesService(leaseService), cd.WithContentStore(contentStore)))
			if err != nil {
				t.Fatal(err)
			}
			oldClient := f.backend.Containerd
			t.Cleanup(func() { _ = oldClient.Close() })
			f.backend.Containerd = client
			// No caller deadline can hide the lack of an internal read bound.
			caller := context.WithoutCancel(t.Context())
			if _, bounded := caller.Deadline(); bounded {
				t.Fatal("fixture accidentally supplied an outer deadline")
			}
			result := make(chan error, 1)
			recipe := f.recipe(t)
			var jobs *ImageJobs
			if phase == "post-build-image" {
				f.backend.PreflightBuild = func(context.Context, ImageRecipe) error { return nil }
				jobs, err = NewImageJobs(t.Context(), storeServer(t), validationAfterBuild{NativeImageBackend: f.backend, barrier: barrier}, "local-validation-vm", 2)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					pin, failure := jobs.Acquire(caller, recipe)
					if pin != nil {
						pin.Release()
					}
					result <- failure
				}()
			} else {
				pin, buildErr := f.backend.Build(caller, recipe, f.phase)
				if buildErr != nil {
					t.Fatal("fixture did not actually retain a valid image", buildErr)
				}
				barrier.armed.Store(true)
				go func() { result <- f.backend.Validate(caller, pin) }()
			}
			watch := time.NewTimer(localImageTimeout + 5*time.Second)
			defer watch.Stop()
			select {
			case deadline := <-barrier.entered:
				if deadline.IsZero() || time.Until(deadline) > localImageTimeout {
					t.Fatal("actual composed validation lacks its own read bound", deadline)
				}
			case <-watch.C:
				t.Fatal("actual retained image never reached validation read")
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("withheld validation reply lost deadline/error identity", err)
				}
			case <-watch.C:
				t.Fatal("validation outlived its internal read deadline")
			}
			if jobs != nil {
				all, readErr := jobs.server.Receipts.List()
				if readErr != nil || len(all) != 1 || all[0].Completion != nil || !all[0].abandonedImage() {
					t.Fatal("validation timeout became successful/replayed image completion", all, readErr)
				}
			}
			if f.leases.creates != 1 || f.images.creates != 1 || f.images.updates != 0 {
				t.Fatal("validation refusal retried creation or published an image", f.leases.creates, f.images.creates, f.images.updates)
			}
		})
	}
}
