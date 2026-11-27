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
// validation's own context ends or the test releases the read, which then
// reaches the real service. The observer timer only fails a hung test; it
// cannot cancel the operation or manufacture a validation result.
type validationReadBarrier struct {
	armed   atomic.Bool
	entered chan time.Time
	release chan struct{}
	abort   chan struct{}
}

func newValidationReadBarrier() *validationReadBarrier {
	return &validationReadBarrier{entered: make(chan time.Time, 1), release: make(chan struct{}), abort: make(chan struct{})}
}

// wait reports the read's deadline (zero when unbounded) and then holds the
// read until its context ends or the test releases it to the real service.
func (b *validationReadBarrier) wait(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	select {
	case b.entered <- deadline:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return nil
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
		if err := s.barrier.wait(ctx); err != nil {
			return images.Image{}, err
		}
	}
	return s.Store.Get(ctx, name)
}

type validationLeaseReads struct {
	leases.Manager
	barrier *validationReadBarrier
}

func (s validationLeaseReads) ListResources(ctx context.Context, l leases.Lease) ([]leases.Resource, error) {
	if s.barrier.armed.Load() {
		if err := s.barrier.wait(ctx); err != nil {
			return nil, err
		}
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
		if err := s.barrier.wait(ctx); err != nil {
			return content.Info{}, err
		}
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

// localValidationFixture installs the read barrier in front of the phase's
// containerd service and returns the fixture, its barrier and its recipe.
func localValidationFixture(t *testing.T, phase string) (localRetainFixture, *validationReadBarrier, ImageRecipe) {
	t.Helper()
	f := newLocalRetainFixture(t, false, false)
	barrier := newValidationReadBarrier()
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
	return f, barrier, f.recipe(t)
}

// The job's detached post-build validation has no caller: the fail-safe at
// its call site bounds it, and its expiry abandons the job without completion.
func TestLocalPinPostBuildValidationHasFailSafe(t *testing.T) {
	old := imageValidateFailSafe
	imageValidateFailSafe = 300 * time.Millisecond
	t.Cleanup(func() { imageValidateFailSafe = old })
	f, barrier, recipe := localValidationFixture(t, "post-build-image")
	caller := context.WithoutCancel(t.Context())
	if _, bounded := caller.Deadline(); bounded {
		t.Fatal("fixture accidentally supplied an outer deadline")
	}
	f.backend.PreflightBuild = func(context.Context, ImageRecipe) error { return nil }
	jobs, err := NewImageJobs(t.Context(), storeServer(t), validationAfterBuild{NativeImageBackend: f.backend, barrier: barrier}, "local-validation-vm", 2)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		pin, failure := jobs.Acquire(caller, recipe)
		if pin != nil {
			pin.Release()
		}
		result <- failure
	}()
	watch := time.NewTimer(30 * time.Second)
	defer watch.Stop()
	select {
	case deadline := <-barrier.entered:
		if deadline.IsZero() || time.Until(deadline) > imageValidateFailSafe {
			t.Fatal("detached post-build validation lacks its fail-safe", deadline)
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
		t.Fatal("validation outlived its fail-safe")
	}
	all, readErr := jobs.server.Receipts.List()
	if readErr != nil || len(all) != 1 || all[0].Completion != nil || !all[0].abandonedImage() {
		t.Fatal("validation timeout became successful/replayed image completion", all, readErr)
	}
	f.leases.mu.Lock()
	leaseCreates := f.leases.creates
	f.leases.mu.Unlock()
	f.images.mu.Lock()
	imageCreates, imageUpdates := f.images.creates, f.images.updates
	f.images.mu.Unlock()
	if leaseCreates != 1 || imageCreates != 1 || imageUpdates != 0 {
		t.Fatal("validation refusal retried creation or published an image", leaseCreates, imageCreates, imageUpdates)
	}
}

// A consumer's validation follows its caller: no internal bound, the caller's
// deadline and cancellation end it, and a reply later than any old fixed
// bound is still a successful validation.
func TestLocalPinConsumerValidationFollowsCaller(t *testing.T) {
	for _, phase := range []string{"consumer-lease", "consumer-content"} {
		for _, mode := range []string{"no-deadline", "short-deadline", "long-deadline", "cancel"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				t.Parallel()
				f, barrier, recipe := localValidationFixture(t, phase)
				pin, buildErr := f.backend.Build(context.WithoutCancel(t.Context()), recipe, f.phase)
				if buildErr != nil {
					t.Fatal("fixture did not actually retain a valid image", buildErr)
				}
				caller, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
				defer cancel()
				switch mode {
				case "short-deadline":
					caller, cancel = context.WithTimeout(caller, 200*time.Millisecond)
					defer cancel()
				case "long-deadline":
					caller, cancel = context.WithTimeout(caller, time.Hour)
					defer cancel()
				}
				barrier.armed.Store(true)
				result := make(chan error, 1)
				go func() { result <- f.backend.Validate(caller, pin) }()
				watch := time.NewTimer(30 * time.Second)
				defer watch.Stop()
				var deadline time.Time
				select {
				case deadline = <-barrier.entered:
				case <-watch.C:
					t.Fatal("consumer validation never reached its read")
				}
				want, bounded := caller.Deadline()
				if bounded != !deadline.IsZero() || (bounded && !deadline.Equal(want)) {
					t.Fatal("validation read does not carry exactly the caller's deadline", deadline, want)
				}
				switch mode {
				case "no-deadline", "long-deadline":
					close(barrier.release)
				case "cancel":
					cancel()
				}
				var err error
				select {
				case err = <-result:
				case <-watch.C:
					t.Fatal("consumer validation did not end")
				}
				switch mode {
				case "no-deadline", "long-deadline":
					if err != nil {
						t.Fatal("a late but complete validation failed", err)
					}
				case "short-deadline":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("caller deadline lost its identity", err)
					}
				case "cancel":
					if !errors.Is(err, context.Canceled) {
						t.Fatal("caller cancellation lost its identity", err)
					}
				}
			})
		}
	}
}
