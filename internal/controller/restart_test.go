package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/safepath"
)

// Recreate the server over the same durable store, as ordinary startup does
// after either guest supervisor restarts it. This is a storage/admission unit
// harness; automatic Firecracker restart itself needs the separate live gate.
func TestServiceRestartKeepsReceiptsMaintenanceAndDegradedAdmission(t *testing.T) {
	meta := t.TempDir()
	dir := filepath.Join(meta, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store := Receipts{dir}
	for _, tc := range []struct{ id, method, phase string }{
		{"waiting-request-123456", "box.create", EffectWaiting},
		{"permitted-request-123456", "box.create", EffectStarted},
		{"command-request-123456", "box.exec", EffectStarted},
	} {
		req := request(tc.method)
		req.ID = tc.id
		r, err := store.Begin(req, Principal{}, "generation")
		if err != nil {
			t.Fatal(err)
		}
		r.EffectPhase = tc.phase
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	// Persist the same marker used by serveController's drain callback.
	if err := safepath.WriteFile(meta, "maintenance", []byte("generation"), 0600); err != nil {
		t.Fatal(err)
	}
	executes := 0
	for restart := range 2 {
		s := &Server{Hello: hello(), Receipts: Receipts{dir},
			Maintenance: func() error { return safepath.WriteFile(meta, "maintenance", []byte("generation"), 0600) },
			Operations:  map[string]Operation{"box.create": {Mutates: true}, "box.list": {}},
			Execute: func(_ context.Context, _ Principal, r Request, _ Streams) (int, error) {
				if r.Method != "box.list" {
					executes++
				}
				return 0, nil
			},
		}
		if restart == 0 {
			// Explicit storage-failure simulation in a running replacement.
			repairs := 0
			prepare := func(context.Context) error {
				repairs++
				if repairs == 1 {
					return errors.New("simulated receipt storage failure")
				}
				required, err := s.Receipts.Recover()
				s.RecoveryRequired = required
				return err
			}
			s.Degrade(errors.New("simulated startup failure"), prepare)
			if err := invoke(t, s, request("box.list"), nil, io.Discard, io.Discard); err != nil {
				t.Fatal("degraded read unavailable", err)
			}
			if !s.recoveryPass(t.Context()) || s.receiptFailure == nil {
				t.Fatal("failed repair opened admission")
			}
			if s.recoveryPass(t.Context()) || s.receiptFailure != nil {
				t.Fatal("healthy repair did not finish")
			}
		} else {
			// Ordinary startup prepares receipts before honoring maintenance.
			required, err := s.Receipts.Recover()
			if err != nil {
				t.Fatal(err)
			}
			s.RecoveryRequired = required
		}
		if _, err := safepath.ReadFile(meta, "maintenance", 4096); err != nil {
			t.Fatal(err)
		}
		if err := s.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := invoke(t, s, request("box.create"), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "maintenance") {
			t.Fatal("repair cleared maintenance", err)
		}
		for id, want := range map[string]string{"waiting-request-123456": "complete", "permitted-request-123456": "uncertain", "command-request-123456": "command-uncertain"} {
			r, err := store.Read(id)
			if err != nil || r.State != want {
				t.Fatal(id, r.State, err)
			}
		}
		if raw, err := safepath.ReadFile(meta, "maintenance", 4096); err != nil || string(raw) != "generation" {
			t.Fatal("marker changed", err)
		}
		if !s.RecoveryRequired || executes != 0 {
			t.Fatal("outcome cleared or mutation replayed", s.RecoveryRequired, executes)
		}
	}
}
