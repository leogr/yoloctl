package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

func TestAcknowledgeCompatibilityLeavesAllRecordsAndMaintenanceUntouched(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, draining: true}
	for _, state := range []string{"uncertain", "command-uncertain", "interrupted", "complete", "pending", "acknowledged"} {
		method := "box.create"
		if state == "command-uncertain" {
			method = "box.exec"
		}
		saveOutcome(t, s.Receipts, "receipt-0123456789-"+state, method, state, time.Minute)
	}
	before, err := s.Receipts.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{true, false} {
		req := request("control.acknowledge-all")
		if !all {
			req.Method = "control.acknowledge"
			req.Payload, _ = json.Marshal(map[string]string{"id": "receipt-0123456789-interrupted"})
		}
		var out, explanation bytes.Buffer
		if err := invoke(t, s, req, nil, &out, &explanation); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(explanation.String(), "hold nothing") {
			t.Fatal(explanation.String())
		}
	}
	after, err := s.Receipts.List()
	if err != nil || len(before) != len(after) {
		t.Fatal(err)
	}
	for i := range before {
		if !sameReceipt(before[i], after[i]) {
			t.Fatal("acknowledgment changed history")
		}
	}
	if !s.draining {
		t.Fatal("acknowledgment ended maintenance")
	}
}

func TestAcknowledgeCompatibilityWorksWithoutReceiptStorage(t *testing.T) {
	s := storeServer(t)
	restore := breakStore(t, s.Receipts.Dir)
	defer restore()
	s.receiptFailure = errors.New("broken record store")
	if _, text, err := acknowledgeAllSummary(t, s); err != nil || !strings.Contains(text, "changes nothing") {
		t.Fatal(text, err)
	}
	req := request("control.acknowledge")
	req.Payload, _ = json.Marshal(map[string]string{"id": "absent-receipt-0123456789"})
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
