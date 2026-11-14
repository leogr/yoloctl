package controller

import (
	"context"
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

func TestFinishedReceiptsNeverFenceIncludingLegacyAndForeignScope(t *testing.T) {
	for _, state := range []string{"interrupted", "uncertain", "acknowledged"} {
		for _, kind := range []string{"scoped", "legacy", "foreign", "global"} {
			t.Run(state+"/"+kind, func(t *testing.T) {
				var effects []string
				s := gatedServer(t, func(Request) EffectIntent { return scopeIntent(writes(box("web"))) }, &effects)
				seedReceipt(t, s, "closed-request-0123456789", state, writes(box("web")), func(r *Receipt) {
					switch kind {
					case "legacy":
						r.ProofVersion = 0
						r.Scope = nil
						r.DiskID = ""
					case "foreign":
						r.DiskID = strings.Repeat("f", 32)
					case "global":
						r.Scope = &Scope{Global: true}
					}
				})
				if err := invoke(t, s, request("box.start"), nil, io.Discard, io.Discard); err != nil || len(effects) != 1 || s.RecoveryRequired {
					t.Fatal(effects, err)
				}
			})
		}
	}
}

func TestRunningRefusalRedactsOtherOperatorsAndKeepsHostIDs(t *testing.T) {
	resource := []Resource{box("web")}
	receipts := []Receipt{{ID: "private-request-id", Method: "account.auth.import"}}
	if err := heldError("work", false, false, resource, receipts); err == nil || strings.Contains(err.Error(), "private-request-id") || strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "box web") {
		t.Fatal(err)
	}
	if err := heldError("work", true, false, resource, receipts); !strings.Contains(err.Error(), "private-request-id") {
		t.Fatal(err)
	}
}
