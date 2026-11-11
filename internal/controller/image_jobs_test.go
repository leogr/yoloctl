package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/imagejob"
)

type jobBackend struct {
	started                 chan ImagePhase
	release                 chan struct{}
	mu                      sync.Mutex
	running, peak, released int
	ignoreCancellation      bool
	preflights              int
}

func (b *jobBackend) Preflight(context.Context, ImageRecipe) error {
	b.mu.Lock()
	b.preflights++
	b.mu.Unlock()
	return nil
}
func (b *jobBackend) Build(ctx context.Context, _ ImageRecipe, p ImagePhase) (PinnedImage, error) {
	b.mu.Lock()
	b.running++
	b.peak = max(b.peak, b.running)
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.running--; b.mu.Unlock() }()
	b.started <- p
	if b.ignoreCancellation {
		<-b.release
	} else {
		select {
		case <-ctx.Done():
			return PinnedImage{}, ctx.Err()
		case <-b.release:
		}
	}
	return PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Digest: "sha256:" + strings.Repeat("a", 64), Lease: "lease-" + p.JobID}, nil
}
func (b *jobBackend) Validate(context.Context, PinnedImage) error { return nil }
func (b *jobBackend) Release(context.Context, PinnedImage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released++
	return nil
}
func recipe(name string) ImageRecipe {
	return ImageRecipe{Builder: "1", Platform: "linux/amd64", BaseDigest: "sha256:" + strings.Repeat("b", 64), Dockerfile: []byte("FROM scratch\nLABEL name=" + name)}
}
func newJobs(t *testing.T, cpus int) (*ImageJobs, *jobBackend) {
	t.Helper()
	s := storeServer(t)
	b := &jobBackend{started: make(chan ImagePhase, 16), release: make(chan struct{}, 16)}
	m, err := NewImageJobs(t.Context(), s, b, "vm-incarnation", cpus)
	if err != nil {
		t.Fatal(err)
	}
	return m, b
}

type acquireResult struct {
	image *RetainedImage
	err   error
}

func acquire(m *ImageJobs, ctx context.Context, r ImageRecipe) <-chan acquireResult {
	ch := make(chan acquireResult, 1)
	go func() { p, e := m.Acquire(ctx, r); ch <- acquireResult{p, e} }()
	return ch
}
func take[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deterministic job barrier")
		var zero T
		return zero
	}
}

func TestImageJobsCapAndRetainUntilConsumerReleases(t *testing.T) {
	m, b := newJobs(t, 3)
	a := acquire(m, t.Context(), recipe("a"))
	first := take(t, b.started)
	c := acquire(m, t.Context(), recipe("c"))
	take(t, b.started)
	d := acquire(m, t.Context(), recipe("d"))
	b.release <- struct{}{}
	b.release <- struct{}{}
	one := take(t, a)
	if one.err != nil {
		t.Fatal(one.err)
	}
	take(t, b.started)
	b.release <- struct{}{}
	two, three := take(t, c), take(t, d)
	if two.err != nil || three.err != nil {
		t.Fatal(two.err, three.err)
	}
	b.mu.Lock()
	peak, released := b.peak, b.released
	b.mu.Unlock()
	if peak != 2 || released != 0 {
		t.Fatalf("peak=%d released=%d", peak, released)
	}
	if r, err := m.server.Receipts.Read(first.JobID); err != nil || r.Completion == nil || r.Image.Output == "" {
		t.Fatal(r, err)
	}
	one.image.Release()
	one.image.Release()
	two.image.Release()
	three.image.Release()
	b.mu.Lock()
	released = b.released
	b.mu.Unlock()
	if released != 3 {
		t.Fatal(released)
	}
}

func TestCompletedImageReleaseDoesNotClaimAbandonment(t *testing.T) {
	m, b := newJobs(t, 2)
	b.release <- struct{}{}
	result, err := m.Acquire(t.Context(), recipe("completed-release"))
	if err != nil {
		t.Fatal(err)
	}
	result.Release()
	result.Release()
	r, err := m.server.Receipts.Read(result.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Completion == nil || r.Completion.Status != 0 || r.Image.ReleasedAt.IsZero() || !r.Image.AbandonedAt.IsZero() || r.Image.Residual || r.abandonedImage() || r.unresolved() {
		t.Fatalf("normal release reported abandonment: %+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"abandoned_at"`) || !strings.Contains(string(raw), `"released_at"`) {
		t.Fatalf("misleading receipt: %s", raw)
	}
	orphans, err := m.server.Receipts.ImageOrphans()
	if err != nil || len(orphans) != 0 {
		t.Fatalf("normal completion produced orphan evidence: %v %v", orphans, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released != 1 {
		t.Fatalf("released pin %d times", b.released)
	}
}

// A builder that ignores cancellation must not retain admission capacity.
func TestImageJobsCancellationReleasesImmediately(t *testing.T) {
	m, b := newJobs(t, 2)
	b.ignoreCancellation = true
	ctx, cancel := context.WithCancel(t.Context())
	first := acquire(m, ctx, recipe("a"))
	old := take(t, b.started)
	cancel()
	if got := take(t, first); !errors.Is(got.err, context.Canceled) {
		t.Fatal(got.err)
	}
	retry := acquire(m, t.Context(), recipe("a"))
	fresh := take(t, b.started)
	if fresh.JobID == old.JobID || fresh.PreparedRef == old.PreparedRef {
		t.Fatal("reused abandoned attempt")
	}
	r, err := m.server.Receipts.Read(old.JobID)
	if err != nil || !r.abandonedImage() || r.unresolved() || r.reserving() || r.Completion != nil || !r.Image.Residual || !r.terminal() {
		t.Fatal(r, err)
	}
	b.release <- struct{}{}
	b.release <- struct{}{}
	got := take(t, retry)
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.image.Release()
}
func TestImageRecoveryAbandonsOnlyQualifiedPrivateAttempts(t *testing.T) {
	m, b := newJobs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	pending := acquire(m, ctx, recipe("a"))
	p := take(t, b.started)
	// Snapshot the durable in-flight receipt, simulating controller loss before
	// cancellation could be recorded. No builder history service is consulted.
	r, err := m.server.Receipts.Read(p.JobID)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	take(t, pending)
	m.mu.Lock()
	oldJob := m.jobs[p.JobID]
	m.mu.Unlock()
	_ = oldJob
	m.server.mu.Lock()
	err = m.server.Receipts.Save(r)
	if err == nil {
		_, err = m.server.Receipts.Recover()
	}
	m.server.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := m.server.Receipts.Read(p.JobID)
	if err != nil || !recovered.abandonedImage() || recovered.Completion != nil {
		t.Fatal(recovered, err)
	}
	next, err := NewImageJobs(t.Context(), m.server, b, "same-vm", 2)
	if err != nil {
		t.Fatal(err)
	}
	retry := acquire(next, t.Context(), recipe("a"))
	take(t, b.started)
	b.release <- struct{}{}
	got := take(t, retry)
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.image.Release()
}

func TestImageRecipeIdentityIncludesContextAndExcludesForcePolicy(t *testing.T) {
	a := recipe("a")
	a.Files = map[string][]byte{"b": []byte("b"), "a": []byte("a")}
	x, err := a.Identity()
	if err != nil {
		t.Fatal(err)
	}
	b := imagejob.Copy(a)
	b.Files = map[string][]byte{"a": []byte("a"), "b": []byte("b")}
	b.Force = true
	y, _ := b.Identity()
	if x != y {
		t.Fatal("map order/force changed safety identity")
	}
	b.Files["a"][0] = 'z'
	z, _ := b.Identity()
	if x == z {
		t.Fatal("context bytes absent from identity")
	}
	b = imagejob.Copy(a)
	b.BaseDigest = "sha256:" + strings.Repeat("c", 64)
	z, _ = b.Identity()
	if x == z {
		t.Fatal("base digest absent from identity")
	}
}

func TestAbandonedImageAccountingSurvivesReceiptEviction(t *testing.T) {
	m, b := newJobs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	pending := acquire(m, ctx, recipe("a"))
	phase := take(t, b.started)
	m.mu.Lock()
	job := m.jobs[phase.JobID]
	m.mu.Unlock()
	cancel()
	take(t, pending)
	take(t, job.done)
	abandoned, err := m.server.Receipts.Read(phase.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < receiptLimit+1; i++ {
		id := fmt.Sprintf("eviction-%020d", i)
		r, err := m.server.Receipts.Begin(Request{ID: id, Method: "test", Dash: -1}, Principal{}, "generation")
		if err != nil {
			t.Fatal(err)
		}
		// Eviction orders by Started, not insertion. Make this fixture newer
		// than the abandoned receipt even if the wall clock moves backwards.
		r.Started = abandoned.Started.Add(time.Duration(i+1) * time.Second)
		r.State = "complete"
		r.Finished = r.Started
		if err := m.server.Receipts.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.server.Receipts.Read(phase.JobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan pinned requester history", err)
	}
	facts, err := m.server.Receipts.ImageOrphans()
	if err != nil || len(facts) != 1 || facts[0].Phase.JobID != phase.JobID {
		t.Fatal(facts, err)
	}
}

func TestForceAfterBuildStartsUsesNewPrivateAttemptWithoutSharedInputReservation(t *testing.T) {
	for _, firstForce := range []bool{false, true} {
		t.Run(fmt.Sprint(firstForce), func(t *testing.T) {
			m, b := newJobs(t, 3)
			first := recipe("same")
			first.Force = firstForce
			a := acquire(m, t.Context(), first)
			old := take(t, b.started)
			forced := first
			forced.Force = true
			next := acquire(m, t.Context(), forced)
			fresh := take(t, b.started)
			if old.JobID == fresh.JobID || old.Attempt == fresh.Attempt {
				t.Fatal("force joined started attempt")
			}
			b.release <- struct{}{}
			b.release <- struct{}{}
			one, two := take(t, a), take(t, next)
			if one.err != nil || two.err != nil {
				t.Fatal(one.err, two.err)
			}
			one.image.Release()
			two.image.Release()
		})
	}
}

func TestHeldBasePublicationRefusesWarmEffectsAndStoreCleanup(t *testing.T) {
	m, b := newJobs(t, 2)
	base := Resource{Kind: ResourceImages, Name: "local/base:pending"}
	r, err := m.server.Receipts.begin(Request{ID: "held-base-publication-001", Method: "image.build", Dash: -1}, Principal{}, "generation", m.server.DiskID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.ProofVersion, r.State, r.EffectPhase, r.Scope = 2, "uncertain", EffectStarted, &Scope{Writes: []Resource{base}}
	if err := m.server.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	input := recipe("dependent")
	input.BaseReference = base.Name
	if result, err := m.Acquire(t.Context(), input); err == nil {
		result.Release()
		t.Fatal("bypassed held base publication")
	}
	b.mu.Lock()
	calls := b.preflights
	b.mu.Unlock()
	if calls != 0 {
		t.Fatal("cleanup ran before dependency refusal")
	}
	if err := m.server.Receipts.CheckImageCleanup(m.server.Hello.Space, m.server.DiskID); err == nil {
		t.Fatal("store-wide cleanup bypassed named hold")
	}
	other := recipe("independent")
	other.BaseReference = "local/other:base"
	pending := acquire(m, t.Context(), other)
	take(t, b.started)
	b.release <- struct{}{}
	result := take(t, pending)
	if result.err != nil {
		t.Fatal(result.err)
	}
	result.image.Release()
}
