package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestInterruptedReceiptClosesWithoutHoldAndIsObservedOnce(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "observe-box-0123456789", "interrupted", writes(box("web")), nil)
	calls := 0
	s.Observe = func(ctx context.Context, got Receipt) ([]BoxObservation, error) {
		calls++
		if got.ID != r.ID {
			t.Fatal("wrong receipt")
		}
		return []BoxObservation{{Name: "web", ID: strings.Repeat("a", 64), State: "running"}}, nil
	}
	if s.recoveryPass(t.Context()) {
		t.Fatal("observation asked to retry")
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.Observation == nil || len(got.Observation.Boxes) != 1 || got.Observation.Boxes[0].State != "running" || got.State != "interrupted" || !got.terminal() || got.unresolved() {
		t.Fatalf("receipt=%+v error=%v", got, err)
	}
	s.recoveryPass(t.Context())
	if calls != 1 {
		t.Fatalf("observation repeated %d times", calls)
	}
}

func TestInterruptedObservationNeverSubstitutesSameNameOnWrongDiskOrUnknownScope(t *testing.T) {
	for _, kind := range []string{"foreign", "legacy", "global", "missing"} {
		t.Run(kind, func(t *testing.T) {
			s := storeServer(t)
			r := seedReceipt(t, s, "no-observe-0123456789", "interrupted", writes(box("web")), func(r *Receipt) {
				switch kind {
				case "foreign":
					r.DiskID = strings.Repeat("f", 32)
				case "legacy":
					r.DiskID = ""
					r.ProofVersion = 0
					r.Scope = nil
				case "global":
					r.Scope = &Scope{Global: true}
				case "missing":
					r.Scope = nil
				}
			})
			s.Observe = func(context.Context, Receipt) ([]BoxObservation, error) {
				t.Fatal("looked up same-named current box")
				return nil, nil
			}
			s.recoveryPass(t.Context())
			got, err := s.Receipts.Read(r.ID)
			if err != nil || got.Observation == nil || got.Observation.Reason == "" || len(got.Observation.Boxes) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestInterruptedUnavailableObservationAndProofAreFinal(t *testing.T) {
	s := storeServer(t)
	r, _ := provenReceipt(t, s, "postimage")
	r.State = "interrupted"
	if err := s.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Verify = func(context.Context, Receipt) ([]DocumentIntent, error) {
		calls++
		return nil, context.DeadlineExceeded
	}
	s.recoveryPass(t.Context())
	s.recoveryPass(t.Context())
	got, err := s.Receipts.Read(r.ID)
	if err != nil || got.Observation == nil || !strings.Contains(got.Observation.Proof, "not observed") || got.State != "interrupted" || calls != 1 {
		t.Fatalf("%+v calls=%d err=%v", got, calls, err)
	}
}

func TestRecoverLegacyOutcomesBecomesInterruptedWithoutReplay(t *testing.T) {
	for _, state := range []string{"uncertain", "command-uncertain"} {
		t.Run(state, func(t *testing.T) {
			s := storeServer(t)
			r := seedReceipt(t, s, "old-receipt-0123456789", state, writes(box("web")), func(r *Receipt) {
				if state == "command-uncertain" {
					r.Method = "box.exec"
				}
			})
			if _, err := s.Receipts.Recover(); err != nil {
				t.Fatal(err)
			}
			got, err := s.Receipts.Read(r.ID)
			if err != nil || got.State != "interrupted" || got.Status == 0 || !got.terminal() {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestAcknowledgmentCompatibilityDoesNotWrite(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "noop-receipt-0123456789", "interrupted", writes(box("web")), nil)
	before, _ := s.Receipts.Read(r.ID)
	old := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error { return errors.New("must not publish") }
	defer func() { writeReceipt = old }()
	if _, err := s.acknowledgeAll(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Receipts.Read(r.ID)
	if !sameReceipt(before, after) {
		t.Fatal("acknowledgment changed receipt")
	}
}

func TestInterruptedPublicInspectionRedactsProofs(t *testing.T) {
	s := storeServer(t)
	r, _ := provenReceipt(t, s, "private-postimage-never-public")
	for _, method := range []string{"control.status", "control.receipts"} {
		req := request(method)
		req.Payload, _ = json.Marshal(map[string]string{"id": r.ID})
		var out, diagnostics bytes.Buffer
		if err := invoke(t, s, req, nil, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), r.ID) {
			t.Fatal(out.String())
		}
		for _, private := range []string{r.Proofs[0].Fingerprint, "fingerprint", "key_id", "private-postimage-never-public"} {
			if strings.Contains(out.String()+diagnostics.String(), private) {
				t.Fatal("public proof material", private)
			}
		}
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || len(got.Proofs) != 1 {
		t.Fatal("public rendering mutated proof", err)
	}
}

func TestInterruptedSingleAcknowledgmentIsNoop(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "noop-one-0123456789", "interrupted", writes(box("web")), nil)
	req := request("control.acknowledge")
	req.Payload, _ = json.Marshal(map[string]string{"id": r.ID})
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := s.Receipts.Read(r.ID)
	if err != nil || !sameReceipt(r, got) {
		t.Fatal("ack changed receipt", err)
	}
}

func TestBoxObserverUsesPrivateVerifierDescriptors(t *testing.T) {
	r := Receipt{ID: "private-box-read-0123456789", DiskID: testDisk, ProofVersion: 1, Scope: writes(box("web"))}
	got, err := RunBoxObserver(t.Context(), "work", r)
	if err != nil || len(got) != 1 || got[0].Name != "web" || got[0].State != "absent" {
		t.Fatal(got, err)
	}
	for _, bad := range []VerifierInput{{Space: "work", Boxes: []string{"../escape"}, DiskID: testDisk}, {Space: "work", Boxes: []string{"web"}}, {Space: "work", Boxes: []string{"web"}, DiskID: testDisk, Documents: []DocumentRef{{Kind: DocumentAccount, Name: "personal"}}}} {
		if err := bad.Validate("work"); err == nil {
			t.Fatal("invalid observer request admitted", bad)
		}
	}
}

func TestRunningReservationsStillCoverAllDependencyKinds(t *testing.T) {
	for _, resource := range []Resource{box("web"), acct("personal"), {Kind: ResourceChat}, {Kind: ResourcePorts}, {Kind: ResourceAudit}, {Kind: ResourceImages}} {
		t.Run(resource.Kind, func(t *testing.T) {
			s := storeServer(t)
			r := seedReceipt(t, s, "running-dependency-0123456789", "pending", writes(resource), nil)
			h, err := s.loadHolds("")
			if err != nil {
				t.Fatal(err)
			}
			_, conflicts, _ := h.conflict(&Scope{Reads: []Resource{resource}})
			if len(conflicts) != 1 || conflicts[0].ID != r.ID {
				t.Fatal("running dependency lost its reservation", conflicts)
			}
			r.State = "interrupted"
			if err := s.Receipts.Save(r); err != nil {
				t.Fatal(err)
			}
			h, err = s.loadHolds("")
			if err != nil {
				t.Fatal(err)
			}
			_, conflicts, _ = h.conflict(&Scope{Reads: []Resource{resource}})
			if len(conflicts) != 0 {
				t.Fatal("ended dependency remained reserved", conflicts)
			}
		})
	}
}

// startStopPolicy is the registration shape of box.start and box.stop.
var startStopPolicy = Operation{BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: FootprintBox}

func boxWrite(name string) *Scope {
	return &Scope{Writes: []Resource{{Kind: ResourceBox, Name: name}}}
}

// A start or stop still running keeps reserving its footprint.
func TestPendingStartStopStillReserves(t *testing.T) {
	r := Receipt{Method: "box.stop", State: "pending", EffectPhase: EffectStarted, Scope: boxWrite("coder")}
	if !r.reserving() {
		t.Fatal("a running box stop released its footprint")
	}
}
