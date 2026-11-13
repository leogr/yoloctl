package controller

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// startStopPolicy is the registration shape of box.start and box.stop.
var startStopPolicy = Operation{BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: FootprintBox}

func boxWrite(name string) *Scope {
	return &Scope{Writes: []Resource{{Kind: ResourceBox, Name: name}}}
}

// A box start or stop that ends without a recorded result is interrupted: a
// plain failure that holds nothing. box.restart keeps today's uncertain.
func TestStartStopWithoutResultIsInterruptedAndHoldsNothing(t *testing.T) {
	for _, method := range []string{"box.stop", "box.start", "box.restart"} {
		t.Run(method, func(t *testing.T) {
			ran := 0
			s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, DiskID: testDisk,
				Operations: map[string]Operation{method: startStopPolicy, "box.create": {Mutates: true, EffectGated: true, Footprint: FootprintBox}},
			}
			s.Execute = func(ctx context.Context, _ Principal, req Request, streams Streams) (int, error) {
				if err := streams.BeginEffects(ctx, EffectIntent{Scope: boxWrite("coder")}); err != nil {
					return 2, err
				}
				if req.Method == "box.create" {
					ran++
					return 0, nil
				}
				return 2, &EffectsUncertain{Cause: errors.New("worker lost")}
			}
			req := request(method)
			err := invoke(t, s, req, nil, io.Discard, io.Discard)
			r, readErr := s.Receipts.Read(req.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if method == "box.restart" {
				if r.State != "uncertain" || !s.RecoveryRequired {
					t.Fatalf("restart: %+v recovery %v", r, s.RecoveryRequired)
				}
				return
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.State != "interrupted" || exit.Status == 0 ||
				exit.Message != strings.ReplaceAll(method, ".", " ")+` did not finish; run "box list" to read the box's state` {
				t.Fatalf("caller result %#v", err)
			}
			if r.State != "interrupted" || s.RecoveryRequired || !r.terminal() || r.unresolved() {
				t.Fatalf("receipt %+v recovery %v", r, s.RecoveryRequired)
			}
			next := request("box.create")
			next.ID += "-next"
			if err := invoke(t, s, next, nil, io.Discard, io.Discard); err != nil || ran != 1 {
				t.Fatal("an interrupted start or stop fenced the box", err)
			}
			if !strings.Contains(s.explainOutcome(r), "holds nothing") {
				t.Fatalf("render %q", s.explainOutcome(r))
			}
		})
	}
}

func TestRecoverClassifiesInterruptedStartStop(t *testing.T) {
	store := Receipts{t.TempDir()}
	cases := map[string]struct {
		method string
		phase  string
		scope  *Scope
		want   string
	}{
		"stop-request-0123456789":     {"box.stop", EffectStarted, boxWrite("coder"), "interrupted"},
		"start-request-0123456789":    {"box.start", EffectStarted, boxWrite("coder"), "interrupted"},
		"legacy-request-0123456789":   {"box.stop", "", boxWrite("coder"), "uncertain"},
		"waiting-request-0123456789":  {"box.start", EffectWaiting, boxWrite("coder"), "recovered"},
		"image-request-0123456789":    {"box.start", EffectStarted, &Scope{Writes: []Resource{{Kind: ResourceImages, Name: "local/x:1"}}}, "uncertain"},
		"restart-request-0123456789":  {"box.restart", EffectStarted, boxWrite("coder"), "uncertain"},
		"recreate-request-0123456789": {"box.recreate", EffectStarted, boxWrite("coder"), "uncertain"},
		"noscope-request-0123456789":  {"box.stop", EffectStarted, nil, "uncertain"},
	}
	for id, c := range cases {
		req := request(c.method)
		req.ID = id
		r, err := store.Begin(req, Principal{}, "generation")
		if err != nil {
			t.Fatal(err)
		}
		r.EffectPhase, r.Scope = c.phase, c.scope
		if c.scope != nil {
			r.ProofVersion, r.DiskID = c.scope.proofVersion(), testDisk
		}
		if err := store.Save(r); err != nil {
			t.Fatal(id, err)
		}
	}
	required, err := store.Recover()
	if err != nil || !required {
		t.Fatal("restart/recreate/legacy outcomes stay unresolved", required, err)
	}
	for id, c := range cases {
		r, err := store.Read(id)
		if err != nil || r.State != c.want {
			t.Fatalf("%s: %q %v, want %q", id, r.State, err, c.want)
		}
	}
	// Only an authorized start or stop can carry the state.
	bad, _ := store.Read("restart-request-0123456789")
	bad.State = "interrupted"
	if err := store.Save(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(bad.ID); err == nil {
		t.Fatal("an interrupted restart was accepted")
	}
}

// A start or stop still running keeps reserving its footprint.
func TestPendingStartStopStillReserves(t *testing.T) {
	r := Receipt{Method: "box.stop", State: "pending", EffectPhase: EffectStarted, Scope: boxWrite("coder")}
	if !r.reserving() {
		t.Fatal("a running box stop released its footprint")
	}
}
