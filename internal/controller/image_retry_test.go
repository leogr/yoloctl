package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type imageServiceFixture struct {
	*jobBackend
	failPreflight bool
}

func (b *imageServiceFixture) Preflight(context.Context, ImageRecipe) error {
	if b.failPreflight {
		return errors.New("preflight refused")
	}
	return nil
}
func (b *imageServiceFixture) Resolve(context.Context, string) (string, string, error) {
	return "sha256:" + strings.Repeat("a", 64), "linux/amd64", nil
}
func (b *imageServiceFixture) ResolveRemote(ctx context.Context, ref string) (string, string, error) {
	return b.Resolve(ctx, ref)
}
func (b *imageServiceFixture) Publish(context.Context, PinnedImage, string) error {
	return errors.New("publication reply lost")
}

func TestImageRetryRequiresDurableRequestWaitingEvidence(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-permit", true: "after-publication"}[published], func(t *testing.T) {
			s := storeServer(t)
			s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
			backend := &imageServiceFixture{jobBackend: &jobBackend{started: make(chan ImagePhase, 1), release: make(chan struct{}, 1)}, failPreflight: false}
			backend.release <- struct{}{}
			var err error
			s.Images, err = NewImageJobs(t.Context(), s, backend, "vm", 2)
			if err != nil {
				t.Fatal(err)
			}
			target := Resource{Kind: ResourceImages, Name: "local/test:result"}
			s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
				if !published {
					s.mu.Lock()
					s.draining = true
					s.mu.Unlock()
				}
				reply, err := streams.Image(ctx, ImageRequest{Action: "acquire", Recipe: recipe("a")})
				if !published {
					s.mu.Lock()
					s.draining = false
					s.mu.Unlock()
				}
				if err != nil {
					return 2, nil
				}
				if err := streams.BeginEffects(ctx, EffectIntent{Scope: &Scope{Writes: []Resource{target}}}); err != nil {
					return 2, err
				}
				_, _ = streams.Image(ctx, ImageRequest{Action: "publish", JobID: reply.Pin.JobID, Reference: target.Name})
				// Even a worker that incorrectly records completion cannot erase the
				// controller's own observation of a lost public publication reply.
				_ = streams.RecordEvidence.CompleteEffects(ctx, 0)
				return 0, nil
			}
			server, client := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
			id := "image-retry-request-0001"
			err = Call(t.Context(), client, s.Hello, Request{ID: id, Method: "box.start", Space: s.Hello.Space, Dash: -1}, ClientIO{})
			_ = client.Close()
			take(t, done)
			var interrupted *ImageInterrupted
			if errors.As(err, &interrupted) == published {
				t.Fatal("wrong retry eligibility", err)
			}
			r, e := s.Receipts.Read(id)
			if e != nil {
				t.Fatal(e)
			}
			if published && (r.State != "uncertain" || r.Completion != nil) {
				t.Fatal("publication uncertainty was erased", r)
			}
			if !published && (r.EffectPhase != EffectWaiting || r.State != "complete") {
				t.Fatal("missing durable pre-apply proof", r)
			}
		})
	}
}

type successfulImageFixture struct{ *imageServiceFixture }

func (b *successfulImageFixture) Publish(context.Context, PinnedImage, string) error { return nil }

func TestIndependentWorkerProcessesShareOnePreparationAndApplySeparately(t *testing.T) {
	s := storeServer(t)
	s.Execute = RunWorker
	s.Operations["test.image-job"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
	backend := &successfulImageFixture{&imageServiceFixture{jobBackend: &jobBackend{started: make(chan ImagePhase, 2), release: make(chan struct{}, 2)}}}
	var err error
	s.Images, err = NewImageJobs(t.Context(), s, backend, "vm", 3)
	if err != nil {
		t.Fatal(err)
	}
	a, b := request("test.image-job"), request("test.image-job")
	b.ID = "other-request-0123456789"
	a.Args = []string{"shared", "local/test:a"}
	b.Args = []string{"shared", "local/test:b"}
	var outA, outB bytes.Buffer
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- invoke(t, s, a, nil, &outA, io.Discard) }()
	phase := take(t, backend.started)
	go func() { doneB <- invoke(t, s, b, nil, &outB, io.Discard) }()
	for {
		s.Images.mu.Lock()
		n := s.Images.jobs[phase.JobID].interests
		changed := s.Images.changed
		s.Images.mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatal("second process never joined")
		}
	}
	backend.release <- struct{}{}
	if err := take(t, doneA); err != nil {
		t.Fatal(err)
	}
	if err := take(t, doneB); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(outA.String()) != phase.JobID || outA.String() != outB.String() {
		t.Fatal("workers consumed different jobs", outA.String(), outB.String())
	}
	for _, id := range []string{a.ID, b.ID} {
		r, err := s.Receipts.Read(id)
		if err != nil || r.State != "complete" || r.Completion == nil {
			t.Fatal("missing independent apply completion", r, err)
		}
	}
	backend.mu.Lock()
	peak, released := backend.peak, backend.released
	backend.mu.Unlock()
	if peak != 1 || released != 1 {
		t.Fatal("shared work or final pin release was lost", peak, released)
	}
}

func TestDesiredImageHoldRefusesCacheHitBeforeSaveOrRuntimeEffects(t *testing.T) {
	for _, method := range []string{"box.create", "box.copy", "box.set", "box.edit.apply", "chat.member.recreate"} {
		t.Run(method, func(t *testing.T) {
			s := storeServer(t)
			s.Operations[method] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
			target := Resource{Kind: ResourceImages, Name: "local/cached:target"}
			seedReceipt(t, s, "held-cached-target-123", "uncertain", writes(target), func(r *Receipt) { r.ProofVersion = 2 })
			backend := &imageServiceFixture{jobBackend: &jobBackend{}}
			var err error
			s.Images, err = NewImageJobs(t.Context(), s, backend, "vm", 2)
			if err != nil {
				t.Fatal(err)
			}
			effects := 0
			s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
				if _, err := streams.Image(ctx, ImageRequest{Action: "check-desired", Reference: "local/unrelated:other"}); err != nil {
					t.Fatal("unrelated image blocked", err)
				}
				if _, err := streams.Image(ctx, ImageRequest{Action: "check-desired", Reference: target.Name}); err != nil {
					return 2, err
				}
				effects++
				return 0, nil
			}
			if err := invoke(t, s, Request{ID: "cache-hit-request-123", Method: method, Space: s.Hello.Space, Dash: -1}, nil, io.Discard, io.Discard); err == nil || effects != 0 {
				t.Fatal("held cache hit reached save/runtime", effects, err)
			}
		})
	}
}
