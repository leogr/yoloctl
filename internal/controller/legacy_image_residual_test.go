package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
)

// List is deliberately bounded and namespace checked like the other finite
// in-process containerd methods in image_local_retain_test.go.
func (s *localRetainLeases) List(ctx context.Context, _ ...string) ([]leases.Lease, error) {
	if err := localRetainCall(ctx); err != nil {
		return nil, err
	}
	var out []leases.Lease
	for id := range s.values {
		out = append(out, leases.Lease{ID: id})
	}
	return out, nil
}

func legacyResidualReceipt(t *testing.T, s *Server) Receipt {
	t.Helper()
	id := strings.Repeat("d", 64)
	phase := ImagePhase{JobID: id, SolveRef: id, PreparedRef: "yoloctl/prepared/" + id, StableInput: strings.Repeat("a", 64), Attempt: strings.Repeat("a", 64), Incarnation: "legacy"}
	r := Receipt{ID: id, Method: "image.job", State: "uncertain", Status: 2, Started: time.Now().Add(-time.Hour), Generation: s.Hello.Generation, ProofVersion: 2, DiskID: s.DiskID, EffectPhase: EffectStarted, Image: &phase, Scope: &Scope{Writes: []Resource{inputResource(phase.StableInput), {Kind: ResourceImages, Name: phase.PreparedRef}}}}
	if err := s.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLegacyImageResidualRetainsAliasesAndLeasesWithoutHoldingWork(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.State != "interrupted" || !got.Image.Residual || got.Image.OrphanRecorded || !got.Image.AbandonedAt.IsZero() || got.terminal() || got.reserving() || got.unresolved() {
		t.Fatal(got, err)
	}
	f := newLocalRetainFixture(t, false, false)
	alias := "docker.io/" + r.Image.PreparedRef + ":latest"
	lease := "yoloctl-image-" + r.ID
	f.images.values[alias] = images.Image{Name: alias, Target: f.target}
	f.leases.values[lease] = map[leases.Resource]bool{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.backend.Sweep(ctx, []Receipt{got}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.images.values[alias]; !ok {
		t.Fatal("deleted legacy alias")
	}
	if _, ok := f.leases.values[lease]; !ok {
		t.Fatal("deleted legacy lease")
	}
	for i := 0; i < receiptLimit-1; i++ {
		finished := Receipt{ID: fmt.Sprintf("completed-0123456789-%03d", i), Method: "box.start", State: "complete", Started: time.Now(), Generation: s.Hello.Generation}
		if err := s.Receipts.Save(finished); err != nil {
			t.Fatal(err)
		}
	}
	next := request("box.start")
	next.ID = "new-admission-0123456789"
	if _, err := s.Receipts.Begin(next, Principal{}, s.Hello.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receipts.Read(r.ID); err != nil {
		t.Fatal("evicted legacy working fact", err)
	}
}

func TestFailedLegacyResidualPublicationCannotAuthorizeCleanup(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	original := writeReceipt
	writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
		if name == r.ID+".json" {
			return errors.New("strict residual ENOSPC")
		}
		return original(dir, name, raw, mode)
	}
	t.Cleanup(func() { writeReceipt = original })
	if _, err := s.Receipts.Recover(); err == nil {
		t.Fatal("lost strict fact was ignored")
	}
	old, err := s.Receipts.Read(r.ID)
	if err != nil || old.State != "uncertain" || !old.legacyImageResidual() || old.terminal() {
		t.Fatal(old, err)
	}
	// Even an independently attempted sweep retains an unconverted old fact.
	f := newLocalRetainFixture(t, false, false)
	alias := "docker.io/" + r.Image.PreparedRef + ":latest"
	lease := "yoloctl-image-" + r.ID
	f.images.values[alias] = images.Image{Name: alias, Target: f.target}
	f.leases.values[lease] = map[leases.Resource]bool{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.backend.Sweep(ctx, []Receipt{old}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.images.values[alias]; !ok {
		t.Fatal("deleted alias after failed residual publication")
	}
	if _, ok := f.leases.values[lease]; !ok {
		t.Fatal("deleted lease after failed residual publication")
	}
}

func TestLegacyResidualDoesNotInventOwnershipForKnownOrV1Jobs(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	r.Completion = &Completion{At: time.Now(), Status: 0}
	r.State = "complete"
	if r.legacyImageResidual() {
		t.Fatal("known completion retained as legacy survivor")
	}
	r.Completion = nil
	r.State = "interrupted"
	r.Image.PrivateVersion = 1
	r.Image.Residual = true
	if r.legacyImageResidual() {
		t.Fatal("v1 orphan was reclassified as legacy")
	}
}

func TestLegacyBootObservationDoesNotTrustHistoricalIncarnationOrClocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := storeServer(t)
		r := legacyResidualReceipt(t, s)
		if _, err := s.Receipts.Recover(); err != nil {
			t.Fatal(err)
		}
		r, err := s.Receipts.Read(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		f := newLocalRetainFixture(t, false, false)
		boot := strings.Repeat("b", 64)
		f.backend.ConfigureLegacy(s, boot)
		// The fake containerd checks for a deadline. Virtual time keeps
		// that check independent of receipt I/O latency.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
			t.Fatal(err)
		}
		r, err = s.Receipts.Read(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Image.LegacyBoot != boot || !r.Image.Residual || r.Image.LegacyAliasPending {
			t.Fatalf("first observation did not retain ownership for boot %s: %+v", boot, r.Image)
		}
		for _, started := range []time.Time{time.Unix(1, 0), time.Now().Add(30 * 365 * 24 * time.Hour)} {
			r.Started = started
			if err := s.Receipts.Save(r); err != nil {
				t.Fatal(err)
			}
			if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
				t.Fatal(err)
			}
			got, err := s.Receipts.Read(r.ID)
			if err != nil || !sameReceipt(r, got) {
				t.Fatal("clock changed ownership", err)
			}
		}
	})
}

func TestLegacyBootPublicationFailureRetainsAliasAndLease(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Receipts.Read(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := newLocalRetainFixture(t, false, false)
	alias := "docker.io/" + r.Image.PreparedRef + ":latest"
	lease := "yoloctl-image-" + r.ID
	f.images.values[alias] = images.Image{Name: alias, Target: f.target}
	f.leases.values[lease] = map[leases.Resource]bool{}
	f.backend.ConfigureLegacy(s, strings.Repeat("b", 64))
	old := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error { return errors.New("boot fact unavailable") }
	t.Cleanup(func() { writeReceipt = old })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.backend.Sweep(ctx, []Receipt{r}); err == nil {
		t.Fatal("failed strict boot publication ignored")
	}
	if _, ok := f.images.values[alias]; !ok {
		t.Fatal("deleted alias")
	}
	if _, ok := f.leases.values[lease]; !ok {
		t.Fatal("deleted lease")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || !sameReceipt(r, got) {
		t.Fatal("failed write changed strict fact", err)
	}
}

func TestLegacyDifferentBootHandsAliasToGCAndClearsOnlyAfterAbsence(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	f := newLocalRetainFixture(t, false, false)
	alias := "docker.io/" + r.Image.PreparedRef + ":latest"
	lease := "yoloctl-image-" + r.ID
	f.images.values[alias] = images.Image{Name: alias, Target: f.target}
	f.leases.values[lease] = map[leases.Resource]bool{}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f.backend.ConfigureLegacy(s, strings.Repeat("b", 64))
	if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	f.backend.ConfigureLegacy(s, strings.Repeat("c", 64))
	if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Receipts.Read(r.ID)
	if err != nil || !pending.Image.LegacyAliasPending || pending.terminal() {
		t.Fatal(pending, err)
	}
	if _, present := f.leases.values[lease]; present {
		t.Fatal("old lease retained after proven new boot")
	}
	if _, present := f.images.values[alias]; !present {
		t.Fatal("sweep deleted alias before protected GC")
	}
	aliases, err := s.Receipts.LegacyImageAliases(s.DiskID, strings.Repeat("c", 64))
	if err != nil || len(aliases) != 1 || aliases[0] != r.Image.PreparedRef {
		t.Fatal(aliases, err)
	}
	if err := s.MaintainLegacyImages(ctx); err != nil {
		t.Fatal(err)
	}
	if _, present := f.images.values[alias]; !present {
		t.Fatal("repeated maintenance bypassed GC")
	}
	delete(f.images.values, alias) // the separately tested GC has removed its exact unused alias
	if err := s.MaintainLegacyImages(ctx); err != nil {
		t.Fatal(err)
	}
	done, err := s.Receipts.Read(r.ID)
	if err != nil || done.Image.Residual || !done.terminal() {
		t.Fatal(done, err)
	}
	aliases, err = s.Receipts.LegacyImageAliases(s.DiskID, strings.Repeat("c", 64))
	if err != nil || len(aliases) != 0 {
		t.Fatal(aliases, err)
	}
}

func TestLegacyNewBootStrictWriteFailureKeepsAliasAndReportsPartialLeaseDeletion(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	f := newLocalRetainFixture(t, false, false)
	alias := "docker.io/" + r.Image.PreparedRef + ":latest"
	lease := "yoloctl-image-" + r.ID
	f.images.values[alias] = images.Image{Name: alias, Target: f.target}
	f.leases.values[lease] = map[leases.Resource]bool{}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f.backend.ConfigureLegacy(s, strings.Repeat("b", 64))
	if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	f.backend.ConfigureLegacy(s, strings.Repeat("c", 64))
	old := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error {
		return errors.New("strict alias-pending write failed")
	}
	t.Cleanup(func() { writeReceipt = old })
	if err := f.backend.Sweep(ctx, []Receipt{r}); err == nil {
		t.Fatal("strict failure ignored")
	}
	if _, ok := f.leases.values[lease]; ok {
		t.Fatal("test did not reach partial deletion")
	}
	if _, ok := f.images.values[alias]; !ok {
		t.Fatal("alias deleted despite failed strict fact")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || !sameReceipt(got, r) {
		t.Fatal("changed failed fact", err)
	}
	candidates, err := s.Receipts.LegacyImageAliases(s.DiskID, strings.Repeat("c", 64))
	if err != nil || len(candidates) != 0 {
		t.Fatal("unpublished candidate escaped", candidates, err)
	}
	writeReceipt = old
	s.VerifyStorage = func(context.Context) error { return errors.New("ownership unavailable") }
	if err := f.backend.Sweep(ctx, []Receipt{r}); err == nil {
		t.Fatal("ownership failure ignored")
	}
	if _, ok := f.images.values[alias]; !ok {
		t.Fatal("failed ownership read became absence")
	}
	s.VerifyStorage = nil
	if err := f.backend.Sweep(ctx, []Receipt{r}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Receipts.Read(r.ID)
	if err != nil || !got.Image.LegacyAliasPending {
		t.Fatal(got, err)
	}
}
