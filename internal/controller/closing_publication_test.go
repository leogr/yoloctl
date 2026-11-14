package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestLegacyResidualInventoryDoesNotConsumeAdmissionCapacity(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	for i := 1; i < receiptLimit; i++ {
		next := r
		next.ID = fmt.Sprintf("%064x", i)
		phase := *r.Image
		phase.JobID, phase.SolveRef, phase.PreparedRef = next.ID, next.ID, "yoloctl/prepared/"+next.ID
		next.Image = &phase
		next.Scope = &Scope{Writes: []Resource{inputResource(phase.StableInput), {Kind: ResourceImages, Name: phase.PreparedRef}}}
		if err := s.Receipts.Save(next); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"box.start", "image.job"} {
		req := request(method)
		req.ID = "fresh-" + strings.ReplaceAll(method, ".", "-") + "-0123456789"
		if _, err := s.Receipts.Begin(req, Principal{}, s.Hello.Generation); err != nil {
			t.Fatalf("%s refused by ended facts: %v", method, err)
		}
	}
	all, err := s.Receipts.List()
	if err != nil {
		t.Fatal(err)
	}
	residuals := 0
	for _, got := range all {
		if got.legacyImageResidual() {
			residuals++
		}
	}
	if residuals != receiptLimit {
		t.Fatal("admission discarded strict ownership", residuals)
	}
}

func TestClosingObservationPublicationReadFailureRetriesOnlyPublication(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "observer-read-loss-0123456789", "interrupted", writes(box("web")), nil)
	away := s.Receipts.Dir + "-temporarily-away"
	calls := 0
	s.Observe = func(context.Context, Receipt) ([]BoxObservation, error) {
		calls++
		if err := os.Rename(s.Receipts.Dir, away); err != nil {
			t.Fatal(err)
		}
		return []BoxObservation{{Name: "web", State: "running"}}, nil
	}
	retry := s.recoveryPass(t.Context())
	if err := os.Rename(away, s.Receipts.Dir); err != nil {
		t.Fatal(err)
	}
	if !retry || len(s.pendingChecks) != 1 {
		t.Fatal("observation was dropped", retry, len(s.pendingChecks))
	}
	if s.recoveryPass(t.Context()) {
		t.Fatal("publication did not settle")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.Observation == nil || calls != 1 {
		t.Fatal(got, err, calls)
	}
	if s.recoveryPass(t.Context()) || calls != 1 {
		t.Fatal("closing observation was run again", calls)
	}
}

func TestClosingObservationPreservesNewerLegacyFact(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	next, observed := s.checkInterrupted(t.Context(), r)
	current := r
	phase := *r.Image
	phase.LegacyBoot = strings.Repeat("b", 64)
	current.Image = &phase
	if err := s.Receipts.Save(current); err != nil {
		t.Fatal(err)
	}
	p := pendingClosingCheck{checked: r, next: next, observed: observed}
	s.pendingChecks = map[string]pendingClosingCheck{r.ID: p}
	if err := s.publishClosingCheck(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.Image.LegacyBoot != phase.LegacyBoot || got.Observation == nil {
		t.Fatal(got, err)
	}
	// A newer completion is never overwritten by a pending older observation.
	current = got
	current.Observation = nil
	current.State = "complete"
	finishedPhase := *current.Image
	finishedPhase.Residual = false
	finishedPhase.LegacyBoot = ""
	finishedPhase.LegacyAliasPending = false
	current.Image = &finishedPhase
	current.Status = 2
	current.Completion = &Completion{At: current.Started, Status: 2}
	if err := s.Receipts.Save(current); err != nil {
		t.Fatal(err)
	}
	s.pendingChecks[r.ID] = p
	if err := s.publishClosingCheck(p); err != nil {
		t.Fatal(err)
	}
	got, err = s.Receipts.Read(r.ID)
	if err != nil || !sameReceipt(got, current) || len(s.pendingChecks) != 0 {
		t.Fatal(got, err)
	}
}

func TestClosingObservationWriteFailureDoesNotRestoreOlderImageFact(t *testing.T) {
	s := storeServer(t)
	r := legacyResidualReceipt(t, s)
	if _, err := s.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Receipts.Read(r.ID)
	next, observed := s.checkInterrupted(t.Context(), r)
	p := pendingClosingCheck{checked: r, next: next, observed: observed}
	s.pendingChecks = map[string]pendingClosingCheck{r.ID: p}
	oldWrite := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error { return fmt.Errorf("closing observation write failed") }
	t.Cleanup(func() { writeReceipt = oldWrite })
	if err := s.publishClosingCheck(p); err == nil {
		t.Fatal("failed publication accepted")
	}
	writeReceipt = oldWrite
	current := r
	phase := *r.Image
	phase.LegacyBoot = strings.Repeat("b", 64)
	phase.LegacyAliasPending = true
	current.Image = &phase
	if err := s.Receipts.Save(current); err != nil {
		t.Fatal(err)
	}
	if s.recoveryPass(t.Context()) {
		t.Fatal("publication did not settle")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || !got.Image.LegacyAliasPending || got.Image.LegacyBoot != phase.LegacyBoot || got.Observation == nil {
		t.Fatal(got, err)
	}
}
