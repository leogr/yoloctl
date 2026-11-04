package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func box(name string) Resource          { return Resource{ResourceBox, name} }
func acct(name string) Resource         { return Resource{ResourceAccount, name} }
func writes(r ...Resource) *Scope       { return &Scope{Writes: r} }
func uses(w, r []Resource) *Scope       { return &Scope{Writes: w, Reads: r} }
func scopeIntent(s *Scope) EffectIntent { return EffectIntent{Scope: s} }

// seedReceipt durably records an outcome as an earlier execution left it.
func seedReceipt(t *testing.T, s *Server, id, state string, scope *Scope, mutate func(*Receipt)) Receipt {
	t.Helper()
	req := request("box.start")
	req.ID = id
	r, err := s.Receipts.begin(req, Principal{}, "generation-1", s.DiskID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.State, r.EffectPhase, r.Scope = state, EffectStarted, scope
	if state != "pending" {
		r.Finished = time.Now().UTC()
	}
	if mutate != nil {
		mutate(&r)
	}
	if err := s.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOutcomeInspectionRedactsProofsAndExplainsCurrentHolds(t *testing.T) {
	s := storeServer(t)
	r, postimage := provenReceipt(t, s, "secret: inspection-fixture")
	for _, method := range []string{"control.status", "control.receipts"} {
		req := request(method)
		req.Payload, _ = json.Marshal(map[string]string{"id": r.ID})
		var out, explanation bytes.Buffer
		if err := invoke(t, s, req, nil, &out, &explanation); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), r.ID) {
			t.Fatal("missing machine-readable outcome", out.String())
		}
		for _, private := range []string{"fingerprint", "key_id", r.Proofs[0].Fingerprint, "inspection-fixture"} {
			if strings.Contains(out.String()+explanation.String(), private) {
				t.Fatalf("%s exposed private verifier material", method)
			}
		}
		if !strings.Contains(explanation.String(), "account personal") {
			t.Fatal("inspection omitted the affected resource", explanation.String())
		}
	}
	// Rendering a public view must leave the durable evidence usable.
	if err := s.publishApplied(r, []DocumentIntent{postimage}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.Recovery.Disposition != DispositionApplied || len(got.Proofs) != 1 {
		t.Fatal(got, err)
	}
	// A foreign-disk footprint cannot explain a bounded hold, even when its
	// receipt is otherwise valid. Storage and maintenance remain separate.
	s.DiskID = "fedcba9876543210fedcba9876543210"
	if text := s.explainOutcome(r); !strings.Contains(text, "holds the whole space") {
		t.Fatal(text)
	}
	s.receiptFailure = errors.New("simulated storage failure")
	s.draining = true
	text := explainHolds([]Receipt{r}, s)
	for _, want := range []string{"Receipt storage is not reconciled", "maintenance", "without a bounded footprint"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q lacks %q", text, want)
		}
	}
}

// gatedServer runs each request as a worker that declares intents[method].
func gatedServer(t *testing.T, intent func(Request) EffectIntent, effects *[]string) *Server {
	t.Helper()
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, DiskID: testDisk,
		Operations: map[string]Operation{"box.start": {BoxAllowed: true, Mutates: true, EffectGated: true}},
	}
	s.Execute = func(ctx context.Context, _ Principal, req Request, streams Streams) (int, error) {
		if err := streams.BeginEffects(ctx, intent(req)); err != nil {
			return 2, err
		}
		*effects = append(*effects, req.ID)
		return 0, nil
	}
	return s
}

func TestScopedHoldsRefuseOnlyIntersectingEffects(t *testing.T) {
	footprints := map[string]*Scope{
		"web-start":     uses([]Resource{box("web")}, []Resource{acct("personal")}),
		"api-start":     uses([]Resource{box("api")}, []Resource{acct("other")}),
		"api-personal":  uses([]Resource{box("api")}, []Resource{acct("personal")}),
		"edit-personal": writes(acct("personal")),
		"edit-other":    writes(acct("other")),
		"unbounded":     nil,
	}
	var effects []string
	s := gatedServer(t, func(req Request) EffectIntent {
		return scopeIntent(footprints[strings.TrimPrefix(req.ID, "test-request-")])
	}, &effects)
	// An interrupted recreate of web left web unresolved; an interrupted
	// native import left account personal unresolved.
	seedReceipt(t, s, "held-web-0123456789", "uncertain", writes(box("web")), nil)
	seedReceipt(t, s, "held-personal-0123456789", "uncertain", writes(acct("personal")), nil)
	for id, want := range map[string]bool{
		"web-start": false, "api-start": true, "api-personal": false,
		"edit-personal": false, "edit-other": true, "unbounded": false,
	} {
		t.Run(id, func(t *testing.T) {
			req := request("box.start")
			req.ID = "test-request-" + id
			err := invoke(t, s, req, nil, io.Discard, io.Discard)
			ran := false
			for _, e := range effects {
				ran = ran || e == req.ID
			}
			if ran != want || (err == nil) != want {
				t.Fatalf("ran=%v err=%v", ran, err)
			}
			r, readErr := s.Receipts.Read(req.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !want && (r.EffectPhase != EffectWaiting || r.State == "uncertain") {
				t.Fatalf("a refused gate left %+v", r)
			}
		})
	}
	if !s.RecoveryRequired {
		t.Fatal("compatibility summary cleared while holds remain")
	}
}

func TestUnboundedOutcomesHoldTheSpace(t *testing.T) {
	for name, mutate := range map[string]func(*Receipt){
		"legacy":       func(r *Receipt) { r.ProofVersion, r.DiskID, r.Scope = 0, "", nil },
		"no footprint": func(r *Receipt) { r.Scope = nil },
		"global":       func(r *Receipt) { r.Scope = &Scope{Global: true} },
		"other disk":   func(r *Receipt) { r.DiskID = "fedcba9876543210fedcba9876543210" },
	} {
		t.Run(name, func(t *testing.T) {
			var effects []string
			s := gatedServer(t, func(Request) EffectIntent { return scopeIntent(writes(box("api"))) }, &effects)
			seedReceipt(t, s, "held-0123456789abcdef", "uncertain", writes(box("web")), mutate)
			err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard)
			if err == nil || len(effects) != 0 || !strings.Contains(err.Error(), "space operation work") {
				t.Fatalf("err=%v effects=%v", err, effects)
			}
		})
	}
}

func TestQueuedWorkerRechecksHoldsAtItsGate(t *testing.T) {
	var effects []string
	s := gatedServer(t, func(Request) EffectIntent { return scopeIntent(writes(box("web"))) }, &effects)
	// Admitted first; the hold appears while the worker waits for its lock.
	s.Execute = func(ctx context.Context, _ Principal, req Request, streams Streams) (int, error) {
		seedReceipt(t, s, "late-0123456789abcdef", "uncertain", writes(box("web")), nil)
		if err := streams.BeginEffects(ctx, scopeIntent(writes(box("web")))); err != nil {
			return 2, err
		}
		effects = append(effects, req.ID)
		return 0, nil
	}
	if err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard); err == nil || len(effects) != 0 {
		t.Fatal("an admitted worker passed a hold created while it waited", err)
	}
}

func TestGateWaitsForRunningReservationNotForSessions(t *testing.T) {
	var effects []string
	s := gatedServer(t, func(Request) EffectIntent { return scopeIntent(writes(box("web"))) }, &effects)
	running := seedReceipt(t, s, "running-0123456789ab", "pending", writes(box("web")), nil)
	// A completed setup (a live session) and an authorized exec never reserve.
	seedReceipt(t, s, "session-0123456789ab", "pending", writes(box("web")), func(r *Receipt) { r.SetupComplete = true })
	seedReceipt(t, s, "exec-0123456789abcd", "pending", writes(box("web")), func(r *Receipt) { r.Method = "box.exec" })
	done := make(chan error, 1)
	go func() { done <- invoke(t, s, request("box.start"), nil, io.Discard, io.Discard) }()
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	waiting := len(effects) == 0
	running.State, running.Finished = "complete", time.Now().UTC()
	if err := s.Receipts.Save(running); err != nil {
		t.Fatal(err)
	}
	s.notifyReceipts()
	s.mu.Unlock()
	if err := <-done; err != nil || !waiting || len(effects) != 1 {
		t.Fatalf("waiting=%v effects=%v err=%v", waiting, effects, err)
	}
}

func TestOperatorRefusalRedactsHostReceipts(t *testing.T) {
	err := heldError("work", false, false, []Resource{box("web")}, []Receipt{{ID: "secret-request-id", Method: "account.auth.import"}})
	if err == nil || strings.Contains(err.Error(), "secret-request-id") || strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "box web") {
		t.Fatal(err)
	}
	host := heldError("work", true, false, []Resource{box("web")}, []Receipt{{ID: "request-id", Method: "box.recreate", State: "uncertain"}})
	if !strings.Contains(host.Error(), "request-id") || !strings.Contains(host.Error(), "space operation work") {
		t.Fatal(host)
	}
}

func TestAcknowledgingOneOutcomeReleasesOnlyItsHolds(t *testing.T) {
	var effects []string
	s := gatedServer(t, func(req Request) EffectIntent {
		if strings.HasSuffix(req.ID, "web") {
			return scopeIntent(writes(box("web")))
		}
		return scopeIntent(writes(box("api")))
	}, &effects)
	seedReceipt(t, s, "held-web-0123456789", "uncertain", writes(box("web")), nil)
	seedReceipt(t, s, "held-api-0123456789", "uncertain", writes(box("api")), nil)
	ack := request("control.acknowledge")
	ack.Payload = []byte(`{"id":"held-web-0123456789"}`)
	if err := invoke(t, s, ack, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"test-request-web", true}, {"test-request-api", false}} {
		req := request("box.start")
		req.ID = tc.id
		if err := invoke(t, s, req, nil, io.Discard, io.Discard); (err == nil) != tc.want {
			t.Fatal(tc.id, err)
		}
	}
	if !s.RecoveryRequired {
		t.Fatal("acknowledging one outcome erased another's hold")
	}
}

func TestOutcomeExplanationsDistinguishEveryDisposition(t *testing.T) {
	now := time.Now().UTC()
	recovered := func(d, proof string, mutate func(*Receipt)) Receipt {
		r := Receipt{State: "recovered", ProofVersion: 1, Recovery: &Recovery{Disposition: d, Proof: proof, Version: 1, At: now}}
		if mutate != nil {
			mutate(&r)
		}
		return r
	}
	for _, tc := range []struct {
		r    Receipt
		want string
	}{
		{Receipt{State: "uncertain", ProofVersion: 1, Scope: writes(box("web"), Resource{Kind: ResourceChat})}, "holds box web, chat until a host reviews"},
		{Receipt{State: "uncertain"}, "holds the whole space"},
		{Receipt{State: "uncertain", ProofVersion: 1, Scope: writes(acct("personal")), Verifier: VerifierAccountDocument, Proofs: []Proof{{}}}, "Read-back has not established"},
		{recovered(DispositionNotApplied, ProofGateWaiting, nil), "not applied"},
		{recovered(DispositionRecordedCompletion, ProofEffectsDone, nil), "the operation succeeded"},
		{recovered(DispositionRecordedCompletion, ProofEffectsDone, func(r *Receipt) { r.Status = 1 }), "failed with exit status 1"},
		{recovered(DispositionApplied, ProofDocumentImage, func(r *Receipt) { r.Verifier = VerifierAccountDocument }), "keep their previous secrets"},
		{recovered(DispositionApplied, ProofDocumentImage, func(r *Receipt) { r.Verifier = VerifierChatRevoke }), "applied it is not established"},
		{Receipt{State: "acknowledged"}, "neither retried nor undone"},
		{Receipt{State: "command-uncertain"}, "holds nothing"},
	} {
		if got := (&Server{}).explainOutcome(tc.r); !strings.Contains(got, tc.want) {
			t.Errorf("%+v: %q lacks %q", tc.r, got, tc.want)
		}
	}
}
