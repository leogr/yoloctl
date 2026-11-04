package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveOutcome(t *testing.T, store Receipts, id, method, state string, age time.Duration) {
	t.Helper()
	r := Receipt{ID: id, Method: method, State: state, Started: time.Now().UTC().Add(-age), Generation: "generation"}
	if state == "command-uncertain" {
		r.EffectPhase = EffectStarted
	}
	if state == "setup-complete" {
		r.SetupComplete = true
	}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
}

func acknowledgeAllSummary(t *testing.T, s *Server) (acknowledgeAllResult, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	req := request("control.acknowledge-all")
	req.ID = "ack-all-request-0123456789"
	err := invoke(t, s, req, nil, &out, &stderr)
	var result acknowledgeAllResult
	if out.Len() > 0 {
		if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr, out.String())
		}
	}
	return result, stderr.String(), err
}

func outcomeIDs(outcomes []reviewedOutcome) []string {
	ids := []string{}
	for _, o := range outcomes {
		ids = append(ids, o.ID)
	}
	return ids
}

func TestAcknowledgeAllReviewsFinishedOutcomesInOneHostCall(t *testing.T) {
	store := Receipts{t.TempDir()}
	saveOutcome(t, store, "structural-request-0123456789", "box.create", "uncertain", 3*time.Minute)
	saveOutcome(t, store, "command-request-0123456789", "box.exec", "command-uncertain", 2*time.Minute)
	saveOutcome(t, store, "complete-request-0123456789", "box.create", "complete", time.Minute)
	saveOutcome(t, store, "attached-request-0123456789", "box.attach", "setup-complete", time.Minute)
	created := 0
	s := &Server{Hello: hello(), Receipts: store, RecoveryRequired: true,
		Operations: map[string]Operation{"box.create": {Mutates: true}},
		Execute:    func(context.Context, Principal, Request, Streams) (int, error) { created++; return 0, nil },
	}
	result, explained, err := acknowledgeAllSummary(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(outcomeIDs(result.Acknowledged), ","); got != "structural-request-0123456789,command-request-0123456789" || result.ChangesPaused || s.RecoveryRequired {
		t.Fatalf("summary %+v recovery=%v", result, s.RecoveryRequired)
	}
	if result.Acknowledged[0].Method != "box.create" || result.Acknowledged[0].State != "uncertain" || result.Acknowledged[1].State != "command-uncertain" {
		t.Fatalf("summary lost methods or prior states: %+v", result.Acknowledged)
	}
	if !strings.Contains(explained, "Acknowledged 2 unknown outcome(s) in space work; none was retried or undone") || !strings.Contains(explained, "are not paused") {
		t.Fatal(explained)
	}
	for id, want := range map[string]string{
		"structural-request-0123456789": "acknowledged", "command-request-0123456789": "acknowledged",
		"complete-request-0123456789": "complete", "attached-request-0123456789": "setup-complete",
	} {
		if r, err := store.Read(id); err != nil || r.State != want {
			t.Fatal(id, r, err)
		}
	}
	next := request("box.create")
	next.ID = "next-request-0123456789"
	if err := invoke(t, s, next, nil, io.Discard, io.Discard); err != nil || created != 1 {
		t.Fatal("acknowledge-all did not reopen changes", err)
	}
	old := request("box.create")
	old.ID = "structural-request-0123456789"
	if err := invoke(t, s, old, nil, io.Discard, io.Discard); err == nil || created != 1 {
		t.Fatal("acknowledged operation was replayed", err)
	}
	if required, err := store.Recover(); err != nil || required {
		t.Fatal("restart lost the review", required, err)
	}
	result, explained, err = acknowledgeAllSummary(t, s)
	if err != nil || len(result.Acknowledged) != 0 || !strings.Contains(explained, "No uncertain outcomes to acknowledge in space work") {
		t.Fatal("no-eligible acknowledgment was not an explicit success", result, explained, err)
	}
}

func TestAcknowledgeAllLeavesRunningOperationForLaterReview(t *testing.T) {
	store := Receipts{t.TempDir()}
	saveOutcome(t, store, "command-request-0123456789", "box.exec", "command-uncertain", time.Minute)
	started, finish := make(chan struct{}), make(chan struct{})
	s := &Server{Hello: hello(), Receipts: store, Operations: map[string]Operation{"box.create": {Mutates: true}},
		Execute: func(context.Context, Principal, Request, Streams) (int, error) {
			close(started)
			<-finish
			return 2, &WorkerInterrupted{Cause: errors.New("lifecycle worker lost after its effect")}
		},
	}
	live := request("box.create")
	live.ID = "live-request-0123456789"
	done := make(chan error, 1)
	go func() { done <- invoke(t, s, live, nil, io.Discard, io.Discard) }()
	<-started
	result, explained, err := acknowledgeAllSummary(t, s)
	if err != nil || strings.Join(outcomeIDs(result.Acknowledged), ",") != "command-request-0123456789" ||
		strings.Join(result.Pending, ",") != live.ID || !strings.Contains(explained, "1 running operation(s) were left untouched") {
		t.Fatal(result, explained, err)
	}
	close(finish)
	<-done
	// The later unknown result was not in the earlier decision and pauses changes again.
	if r, err := store.Read(live.ID); err != nil || r.State != "uncertain" || !s.RecoveryRequired {
		t.Fatal(r, s.RecoveryRequired, err)
	}
	result, _, err = acknowledgeAllSummary(t, s)
	if err != nil || strings.Join(outcomeIDs(result.Acknowledged), ",") != live.ID || s.RecoveryRequired {
		t.Fatal(result, err)
	}
}

func TestAcknowledgeAllFailsClosed(t *testing.T) {
	setup := func(t *testing.T) (*Server, Receipts) {
		store := Receipts{t.TempDir()}
		for i, id := range []string{"first-request-0123456789", "second-request-0123456789", "third-request-0123456789"} {
			saveOutcome(t, store, id, "box.create", "uncertain", time.Duration(3-i)*time.Minute)
		}
		return &Server{Hello: hello(), Receipts: store, RecoveryRequired: true, Operations: map[string]Operation{"box.create": {Mutates: true}}}, store
	}
	states := func(t *testing.T, store Receipts) string {
		t.Helper()
		var got []string
		for _, id := range []string{"first-request-0123456789", "second-request-0123456789", "third-request-0123456789"} {
			r, err := store.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, r.State)
		}
		return strings.Join(got, ",")
	}
	t.Run("operator", func(t *testing.T) {
		s, store := setup(t)
		left, right := net.Pipe()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { s.ServeConn(ctx, right, Principal{Box: "operator", Incarnation: "current"}); close(done) }()
		err := Call(ctx, left, hello(), request("control.acknowledge-all"), ClientIO{Out: io.Discard, Err: io.Discard})
		<-done
		if err == nil || states(t, store) != "uncertain,uncertain,uncertain" || !s.RecoveryRequired {
			t.Fatal("operator acknowledged host recovery", err)
		}
	})
	t.Run("inventory", func(t *testing.T) {
		s, store := setup(t)
		if err := os.WriteFile(filepath.Join(store.Dir, "broken-request-0123456789.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := acknowledgeAllSummary(t, s); err == nil || states(t, store) != "uncertain,uncertain,uncertain" || !s.RecoveryRequired {
			t.Fatal("unreadable inventory permitted writes", err)
		}
	})
	t.Run("storage-failed", func(t *testing.T) {
		s, store := setup(t)
		s.receiptFailure = errors.New("durability unknown")
		if _, _, err := acknowledgeAllSummary(t, s); err == nil || states(t, store) != "uncertain,uncertain,uncertain" {
			t.Fatal("acknowledged over failed receipt storage", err)
		}
	})
	t.Run("partial-save", func(t *testing.T) {
		s, store := setup(t)
		previous := saveReview
		t.Cleanup(func() { saveReview = previous })
		saves := 0
		saveReview = func(store Receipts, r Receipt) error {
			if saves++; saves == 2 {
				return errors.New("injected flush failure")
			}
			return previous(store, r)
		}
		result, explained, err := acknowledgeAllSummary(t, s)
		var exit *ExitError
		if !errors.As(err, &exit) || !strings.Contains(err.Error(), "injected flush failure") {
			t.Fatal("partial failure reported success", err)
		}
		if strings.Join(outcomeIDs(result.Acknowledged), ",") != "first-request-0123456789" || result.Failed == nil || result.Failed.ID != "second-request-0123456789" ||
			strings.Join(outcomeIDs(result.Unattempted), ",") != "third-request-0123456789" || !result.ChangesPaused {
			t.Fatalf("untruthful partial summary %+v", result)
		}
		if !strings.Contains(explained, "may or may not be acknowledged") || !strings.Contains(explained, "remain paused") {
			t.Fatal(explained)
		}
		if states(t, store) != "acknowledged,uncertain,uncertain" || s.receiptFailure == nil || !s.RecoveryRequired {
			t.Fatal("partial save was rolled back, retried or cleared the hold", states(t, store))
		}
		next := request("box.create")
		next.ID = "next-request-0123456789"
		if err := invoke(t, s, next, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "receipt storage failed") {
			t.Fatal("mutation admitted after a partial acknowledgment", err)
		}
		if _, _, err := acknowledgeAllSummary(t, s); err == nil || saves != 2 {
			t.Fatal("acknowledgment continued over failed storage", err)
		}
	})
}

func TestAcknowledgeAllLostReplyLeavesInspectableDecision(t *testing.T) {
	store := Receipts{t.TempDir()}
	saveOutcome(t, store, "structural-request-0123456789", "box.create", "uncertain", time.Minute)
	s := &Server{Hello: hello(), Receipts: store, RecoveryRequired: true}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { s.ServeConn(t.Context(), server, Principal{}); close(done) }()
	wire := NewWire(client, client)
	h := hello()
	if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Read(); err != nil {
		t.Fatal(err)
	}
	req := request("control.acknowledge-all")
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close() // the reply is lost
	<-done
	if r, err := store.Read("structural-request-0123456789"); err != nil || r.State != "acknowledged" || s.RecoveryRequired {
		t.Fatal("decision was not durable before its reply", r, err)
	}
	query := request("control.status")
	query.Payload, _ = json.Marshal(map[string]string{"id": "structural-request-0123456789"})
	var out bytes.Buffer
	if err := invoke(t, s, query, nil, &out, io.Discard); err != nil || !strings.Contains(out.String(), `"state":"acknowledged"`) {
		t.Fatal("lost acknowledgment was not inspectable", out.String(), err)
	}
}

func TestAcknowledgeAllKeepsMaintenanceInRemainingHold(t *testing.T) {
	store := Receipts{t.TempDir()}
	saveOutcome(t, store, "structural-request-0123456789", "box.create", "uncertain", time.Minute)
	s := &Server{Hello: hello(), Receipts: store, RecoveryRequired: true, Operations: map[string]Operation{"box.create": {Mutates: true}}}
	if err := s.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, explained, err := acknowledgeAllSummary(t, s)
	if err != nil || len(result.Acknowledged) != 1 || s.RecoveryRequired {
		t.Fatal(result, err)
	}
	if !result.Maintenance || !result.ChangesPaused || !strings.Contains(explained, "is in maintenance") || !strings.Contains(explained, "remain paused") {
		t.Fatalf("summary hid maintenance: %+v\n%s", result, explained)
	}
	next := request("box.create")
	next.ID = "next-request-0123456789"
	if err := invoke(t, s, next, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "maintenance") || !s.draining {
		t.Fatal("acknowledgment ended maintenance", err)
	}
}
