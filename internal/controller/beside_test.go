package controller

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// stopPolicy is box.stop's registration: a stop beside other operations.
var stopPolicy = Operation{BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: FootprintBox, Beside: true}

// saveReceipt stores a receipt of method on box coder in the given state.
func saveReceipt(t *testing.T, s *Server, id, method, state string) {
	t.Helper()
	req := request(method)
	req.ID = id
	s.mu.Lock()
	r, err := s.beginExecution(req, Principal{}, true)
	if err == nil {
		r.EffectPhase, r.Scope = EffectStarted, boxWrite("coder")
		r.ProofVersion, r.DiskID = r.Scope.proofVersion(), testDisk
		r.State = state
		if state == "interrupted" {
			r.Status = 2
		}
		err = s.persistExecution(*r, state != "pending")
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

}

func besideServer(t *testing.T) (*Server, *int) {
	ran := 0
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, DiskID: testDisk,
		Operations: map[string]Operation{"box.stop": stopPolicy, "box.restart": startStopPolicy}}
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		if err := streams.BeginEffects(ctx, EffectIntent{Scope: boxWrite("coder")}); err != nil {
			return 2, err
		}
		ran++
		return 0, nil
	}
	return s, &ran
}

// A running execution on the box: box stop is admitted and its gate passes
// at once; another method still waits for the reservation.
func TestBoxStopGateDoesNotWaitForRunningExecutions(t *testing.T) {
	s, ran := besideServer(t)
	saveReceipt(t, s, "restart-running-0123456789", "box.restart", "pending")
	if err := invoke(t, s, request("box.stop"), nil, io.Discard, io.Discard); err != nil || *ran != 1 {
		t.Fatalf("stop beside a running restart: %v ran %d", err, *ran)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := s.awaitReservations(ctx, "", boxWrite("coder")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("control: another gate did not wait: %v", err)
	}
}

// An interrupted start or stop on the box holds nothing: the stop runs.
func TestBoxStopRunsBesideAnInterruptedRecord(t *testing.T) {
	s, ran := besideServer(t)
	saveReceipt(t, s, "stop-ended-0123456789", "box.stop", "interrupted")
	if err := invoke(t, s, request("box.stop"), nil, io.Discard, io.Discard); err != nil || *ran != 1 {
		t.Fatalf("stop beside an interrupted record: %v ran %d", err, *ran)
	}
}

// Old ended outcomes do not fence the stop; running policy is separate.
func TestBoxStopIgnoresLegacyEndedOutcome(t *testing.T) {
	s, ran := besideServer(t)
	saveReceipt(t, s, "restart-unknown-0123456789", "box.restart", "uncertain")
	err := invoke(t, s, request("box.stop"), nil, io.Discard, io.Discard)
	if err != nil || *ran != 1 {
		t.Fatalf("legacy ended outcome: %v ran %d", err, *ran)
	}
}

// A draining controller admits box stop and runs it; other mutations stay
// refused.
func TestDrainingControllerAdmitsBoxStopOnly(t *testing.T) {
	s, ran := besideServer(t)
	if err := s.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, s, request("box.stop"), nil, io.Discard, io.Discard); err != nil || *ran != 1 {
		t.Fatalf("stop during drain: %v ran %d", err, *ran)
	}
	next := request("box.restart")
	next.ID += "-restart"
	if err := invoke(t, s, next, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("restart during drain: %v", err)
	}
}

// A stop admitted at zero active after the drain's snapshot creates a new
// idle channel; the drain that was acknowledged before it is neither
// delayed nor refused, and the stop runs to its end.
func TestStopAdmittedAfterTheDrainSnapshotDoesNotDelayIt(t *testing.T) {
	s, _ := besideServer(t)
	drained := make(chan error, 1)
	go func() { drained <- s.Drain(t.Context()) }()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain at zero active waited")
	}
	before := s.idle
	release, receipt, err := s.admitOperation(request("box.stop"), Principal{}, stopPolicy)
	if err != nil || receipt == nil {
		t.Fatalf("late stop refused: %v", err)
	}
	s.mu.Lock()
	active, after := s.active, s.idle
	s.mu.Unlock()
	if active != 1 || after == nil || after == before {
		t.Fatalf("late stop did not open its own idle channel: active %d", active)
	}
	select {
	case <-after:
		t.Fatal("idle closed while the stop runs")
	default:
	}
	release()
	select {
	case <-after:
	case <-time.After(time.Second):
		t.Fatal("the stop's end did not close its idle channel")
	}
	if !s.draining {
		t.Fatal("the late stop reopened admission")
	}
}
