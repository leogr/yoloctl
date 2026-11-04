package controller

import (
	"context"
	"strings"
	"testing"
)

func TestAdvisoryTracksAcquiredHolderDeathAndTokenHandoff(t *testing.T) {
	s := storeServer(t)
	s.Hello.Space = "work"
	query := func() *BlockerSummary {
		r, err := s.advisory(t.Context(), Request{}, AdvisoryNotice{Action: "blocker"})
		if err != nil {
			t.Fatal(err)
		}
		return r.Holder
	}
	if query() != nil {
		t.Fatal("invented unknown holder")
	}
	ctx, cancel := context.WithCancel(t.Context())
	req := Request{ID: "a", Method: "box.recreate", Args: []string{"dev", "SECRET-EXEC"}, Flags: map[string][]string{"env": {"TOKEN=SECRET-ENV"}}}
	_, _ = s.advisory(ctx, req, AdvisoryNotice{Action: "acquired", Space: "work", Exclusive: true, Token: "old"})
	got := query()
	if got == nil || got.Box != "dev" || strings.Contains(got.String(), "SECRET") {
		t.Fatal(got)
	}
	_, _ = s.advisory(t.Context(), Request{ID: "queued", Method: "box.set"}, AdvisoryNotice{Action: "blocker"})
	if query().Method != "box.recreate" {
		t.Fatal("queued request replaced holder")
	}
	cancel()
	if query() != nil {
		t.Fatal("dead worker retained holder")
	}
	_, _ = s.advisory(t.Context(), Request{ID: "b", Method: "box.start", Args: []string{"next"}}, AdvisoryNotice{Action: "acquired", Space: "work", Exclusive: true, Token: "new"})
	_, _ = s.advisory(ctx, req, AdvisoryNotice{Action: "released", Token: "old"})
	s.clearAdvisory("a")
	if query() == nil || query().Box != "next" {
		t.Fatal("stale release removed current holder")
	}
	_, _ = s.advisory(t.Context(), Request{ID: "b"}, AdvisoryNotice{Action: "released", Token: "new"})
	if query() != nil {
		t.Fatal("released holder retained")
	}
}
func TestCapacityAndSharedJobReportActualOwners(t *testing.T) {
	m, b := newJobs(t, 3)
	pending := make([]chan acquireResult, 2)
	for i, name := range []string{"first", "second"} {
		pending[i] = make(chan acquireResult, 1)
		go func() {
			p, e := m.AcquireWithStatus(t.Context(), recipe(name), BlockerSummary{Method: "box.create", Box: name}, nil)
			pending[i] <- acquireResult{p, e}
		}()
		take(t, b.started)
	}
	notices := make(chan ImageWait, 4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan acquireResult, 1)
	go func() {
		p, e := m.AcquireWithStatus(ctx, recipe("third"), BlockerSummary{Method: "box.start", Box: "third"}, func(w ImageWait) { notices <- w })
		done <- acquireResult{p, e}
	}()
	wait := take(t, notices)
	if wait.Kind != "build capacity" || len(wait.Blockers) != 2 {
		t.Fatal(wait)
	}
	for _, owner := range wait.Blockers {
		if owner.Box != "first" && owner.Box != "second" {
			t.Fatal(owner)
		}
	}
	cancel()
	take(t, done)
	ctx, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	go func() {
		p, e := m.AcquireWithStatus(ctx, recipe("first"), BlockerSummary{}, func(w ImageWait) { notices <- w })
		done <- acquireResult{p, e}
	}()
	wait = take(t, notices)
	if wait.Kind != "image job" || len(wait.Blockers) != 1 || wait.Blockers[0].Box != "first" {
		t.Fatal(wait)
	}
	cancel2()
	take(t, done)
	for range 2 {
		b.release <- struct{}{}
	}
	for _, ch := range pending {
		r := take(t, ch)
		if r.err != nil {
			t.Fatal(r.err)
		}
		r.image.Release()
	}
}
