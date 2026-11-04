package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spacelock"
)

func TestWorkerEffectCheckpointOutcomes(t *testing.T) {
	for _, action := range []string{"normal", "child-fd", "kill", "panic", "uncertain", "setup-kill", "malformed", "malformed-wait", "malformed-started", "stdout"} {
		t.Run(action, func(t *testing.T) {
			method := "test.gated"
			if action == "setup-kill" {
				method = "chat.stream"
			}
			s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
				Operations: map[string]Operation{method: {Mutates: true, EffectGated: true}},
				Execute:    RunWorker,
			}
			path := filepath.Join(t.TempDir(), "effect")
			req := request(method)
			req.Args = []string{action, path}
			_ = invoke(t, s, req, nil, io.Discard, io.Discard)
			r, err := s.Receipts.Read(req.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "uncertain"
			switch action {
			case "normal", "child-fd", "malformed", "malformed-wait", "stdout":
				want = "complete"
			case "setup-kill":
				want = "setup-complete"
			}
			if r.State != want || s.RecoveryRequired != (want == "uncertain") {
				t.Fatalf("outcome=%+v recovery=%v", r, s.RecoveryRequired)
			}
			_, statErr := os.Stat(path)
			noEffect := action == "malformed" || action == "malformed-wait" || action == "stdout"
			if noEffect != errors.Is(statErr, os.ErrNotExist) || (r.EffectPhase == EffectWaiting) != noEffect {
				t.Fatalf("effect=%v phase=%s", statErr, r.EffectPhase)
			}
		})
	}
}

type checkpointWriter func([]byte) (int, error)

func (w checkpointWriter) Write(p []byte) (int, error) { return w(p) }

func TestCancelledWorkerWaitingForRealLockDoesNotFence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	lockPath, err := layout.SpaceLockPath("work")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"test.gated", "box.exec"} {
		t.Run(method, func(t *testing.T) {
			s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
				Operations: map[string]Operation{method: execPolicy},
				Execute:    RunWorker,
			}
			req := request(method)
			req.Args = []string{"waiting", lockPath}
			client, server := net.Pipe()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
			_ = Call(ctx, client, hello(), req, ClientIO{Out: checkpointWriter(func(p []byte) (int, error) {
				cancel() // worker is demonstrably at the lock wait, which stays held
				return len(p), nil
			})})
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled lock waiter did not terminate")
			}
			r, err := s.Receipts.Read(req.ID)
			if err != nil || r.State != "complete" || r.Status == 0 || r.EffectPhase != EffectWaiting || s.RecoveryRequired {
				t.Fatal(r, s.RecoveryRequired, err)
			}
			next := request(method)
			next.ID += "-next"
			next.Args = []string{"normal", filepath.Join(t.TempDir(), "next-effect")}
			_ = invoke(t, s, next, nil, io.Discard, io.Discard)
			r, err = s.Receipts.Read(next.ID)
			if err != nil || r.State != "complete" || r.EffectPhase != EffectStarted {
				t.Fatal("safe cancellation blocked a new mutation", r, err)
			}
		})
	}
}

func TestEffectCheckpointSaveFailureKeepsStorageFence(t *testing.T) {
	for _, method := range []string{"test.gated", "box.exec"} {
		t.Run(method, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "receipts")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			s := &Server{Hello: hello(), Receipts: Receipts{dir},
				Operations: map[string]Operation{method: execPolicy},
			}
			s.Execute = func(ctx context.Context, p Principal, req Request, streams Streams) (int, error) {
				// Pending is durable; replacement now fails even when tests run as root.
				if err := os.Rename(dir, dir+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				return RunWorker(ctx, p, req, streams)
			}
			req := request(method)
			path := filepath.Join(t.TempDir(), "effect")
			req.Args = []string{"normal", path}
			if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil {
				t.Fatal("failed checkpoint reported success")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || s.receiptFailure == nil {
				t.Fatal("gate permitted effect after failed durable save", err)
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dir+".saved", dir); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.admitOperation(req, Principal{}, Operation{Mutates: true, EffectGated: true}); err == nil {
				t.Fatal("storage repair alone cleared receipt fence")
			}
			r, err := s.Receipts.Read(req.ID)
			if err != nil || r.EffectPhase != EffectWaiting {
				t.Fatal(r, err)
			}
		})
	}
}

func TestEffectGateRecoveryDistinguishesLegacyAndWaiting(t *testing.T) {
	for _, method := range []string{"test.gated", "box.exec"} {
		for _, phase := range []string{"", EffectWaiting, EffectStarted} {
			t.Run(method+"-phase-"+phase, func(t *testing.T) {
				store := Receipts{t.TempDir()}
				req := request(method)
				r, err := store.Begin(req, Principal{}, "generation")
				if err != nil {
					t.Fatal(err)
				}
				r.EffectPhase = phase
				if err := store.Save(r); err != nil {
					t.Fatal(err)
				}
				// Only a durably permitted exec narrows a lost result to its
				// command; a legacy exec record proves nothing by method name.
				want := "uncertain"
				switch {
				case phase == EffectWaiting:
					want = "complete"
				case phase == EffectStarted && method == "box.exec":
					want = "command-uncertain"
				}
				recovery, err := store.Recover()
				if err != nil || recovery != (want == "uncertain") {
					t.Fatal(recovery, err)
				}
				r, err = store.Read(req.ID)
				if err != nil || r.State != want || r.Finished.IsZero() || (r.Status == 2) != (want == "complete") {
					t.Fatal(r, err)
				}
			})
		}
	}
}

// execPolicy is the production box.exec registration;
// cmd/yoloctl's TestBoxExecRegistrationGatesCommandEffects keeps them equal.
var execPolicy = Operation{BoxAllowed: true, Mutates: true, EffectGated: true}

func readEffects(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil || strings.ReplaceAll(string(raw), "effect\n", "") != "" {
		t.Fatalf("effects %q: %v", raw, err)
	}
	return strings.Count(string(raw), "effect\n")
}

func TestExecWorkerOutcomesKeepUnknownCommandsOutOfRecovery(t *testing.T) {
	for _, action := range []string{"normal", "kill", "panic", "uncertain", "setup-kill", "malformed", "malformed-wait", "malformed-started", "stdout"} {
		t.Run(action, func(t *testing.T) {
			unrelated := 0
			s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
				Operations: map[string]Operation{"box.exec": execPolicy, "box.create": {Mutates: true}},
			}
			s.Execute = func(ctx context.Context, p Principal, req Request, streams Streams) (int, error) {
				if req.Method == "box.create" {
					unrelated++
					return 0, nil
				}
				return RunWorker(ctx, p, req, streams)
			}
			path := filepath.Join(t.TempDir(), "effect")
			req := request("box.exec")
			req.Args = []string{action, path}
			_ = invoke(t, s, req, nil, io.Discard, io.Discard)
			r, err := s.Receipts.Read(req.ID)
			if err != nil {
				t.Fatal(err)
			}
			noEffect := action == "malformed" || action == "malformed-wait" || action == "stdout"
			want, effects := "command-uncertain", 1
			if noEffect {
				want, effects = "complete", 0
			} else if action == "normal" {
				want = "complete"
			}
			// Process loss after the permit, including after setup release, is
			// an unknown command result: never success, setup-complete or a fence.
			if r.State != want || (r.Status == 7) != (action == "normal") || r.Status == 0 || r.SetupComplete || s.RecoveryRequired {
				t.Fatalf("outcome=%+v recovery=%v", r, s.RecoveryRequired)
			}
			if readEffects(t, path) != effects || (r.EffectPhase == EffectWaiting) != noEffect {
				t.Fatalf("phase=%s", r.EffectPhase)
			}
			next := request("box.create")
			next.ID += "-next"
			if err := invoke(t, s, next, nil, io.Discard, io.Discard); err != nil || unrelated != 1 {
				t.Fatal("command outcome fenced an unrelated mutation", err)
			}
			if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil || readEffects(t, path) != effects {
				t.Fatal("recorded exec request ran again", err)
			}
		})
	}
}

func TestExecClientLossAfterPermissionStaysQueryableWithoutFence(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
		Operations: map[string]Operation{"box.exec": execPolicy},
		Execute:    RunWorker,
	}
	path := filepath.Join(t.TempDir(), "effect")
	req := request("box.exec")
	req.Args = []string{"hold", path}
	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
	err := Call(ctx, client, hello(), req, ClientIO{Out: checkpointWriter(func(p []byte) (int, error) {
		cancel() // the released command is visibly running when its client closes
		return len(p), nil
	})})
	var unknown *OutcomeUnknown
	if !errors.As(err, &unknown) || unknown.ID != req.ID {
		t.Fatal("client loss reported a known result", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("abandoned command was not terminated")
	}
	r, err := s.Receipts.Read(req.ID)
	if err != nil || r.State != "command-uncertain" || r.EffectPhase != EffectStarted || s.RecoveryRequired || readEffects(t, path) != 1 {
		t.Fatal(r, s.RecoveryRequired, err)
	}
	query := request("control.status")
	query.ID = "status-request-0123456789"
	query.Payload, _ = json.Marshal(map[string]string{"id": req.ID})
	var out bytes.Buffer
	if err := invoke(t, s, query, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var queried Receipt
	if err := json.Unmarshal(out.Bytes(), &queried); err != nil || queried.State != "command-uncertain" {
		t.Fatal("unknown command result was not queryable", out.String(), err)
	}
	// A separate command runs without acknowledgment; the lost one never replays.
	next := request("box.exec")
	next.ID += "-next"
	next.Args = []string{"normal", path}
	var exit *ExitError
	if err := invoke(t, s, next, nil, io.Discard, io.Discard); !errors.As(err, &exit) || exit.Status != 7 {
		t.Fatal("unrelated exec was refused", err)
	}
	if r, err := s.Receipts.Read(next.ID); err != nil || r.State != "complete" || r.Status != 7 || readEffects(t, path) != 2 {
		t.Fatal(r, err)
	}
	ack := request("control.acknowledge")
	ack.ID = "ack-request-0123456789"
	ack.Payload = query.Payload
	if err := invoke(t, s, ack, nil, io.Discard, io.Discard); err != nil {
		t.Fatal("optional review of an unknown command result failed", err)
	}
	if r, err := s.Receipts.Read(req.ID); err != nil || r.State != "acknowledged" || readEffects(t, path) != 2 {
		t.Fatal(r, err)
	}
}

func TestGenuineUncertaintyStillFencesExec(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
		Operations: map[string]Operation{"box.exec": execPolicy, "test.gated": {Mutates: true, EffectGated: true}, "box.list": {}},
		Execute:    RunWorker,
	}
	structural := request("test.gated")
	structural.ID = "structural-request-0123456789"
	structural.Args = []string{"kill", filepath.Join(t.TempDir(), "structural")}
	_ = invoke(t, s, structural, nil, io.Discard, io.Discard)
	if r, err := s.Receipts.Read(structural.ID); err != nil || r.State != "uncertain" || !s.RecoveryRequired {
		t.Fatal(r, s.RecoveryRequired, err)
	}
	path := filepath.Join(t.TempDir(), "effect")
	req := request("box.exec")
	req.Args = []string{"normal", path}
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil || readEffects(t, path) != 0 {
		t.Fatal("exec ran behind an unreviewed controller outcome", err)
	}
	if _, err := s.Receipts.Read(req.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused exec recorded a receipt", err)
	}
	s.Execute = func(context.Context, Principal, Request, Streams) (int, error) { return 0, nil }
	if err := invoke(t, s, request("box.list"), nil, io.Discard, io.Discard); err != nil {
		t.Fatal("reads blocked by recovery", err)
	}
	s.Execute = RunWorker
	ack := request("control.acknowledge")
	ack.ID = "ack-request-0123456789"
	ack.Payload, _ = json.Marshal(map[string]string{"id": structural.ID})
	if err := invoke(t, s, ack, nil, io.Discard, io.Discard); err != nil || s.RecoveryRequired {
		t.Fatal(err)
	}
	var exit *ExitError
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); !errors.As(err, &exit) || exit.Status != 7 || readEffects(t, path) != 1 {
		t.Fatal("reviewed recovery did not reopen exec", err)
	}
}

func TestStartupRecoveryFencesOnlyStructuralUncertainty(t *testing.T) {
	store := Receipts{t.TempDir()}
	for _, method := range []string{"box.exec", "box.create"} {
		req := request(method)
		req.ID = method[4:] + "-request-0123456789"
		r, err := store.Begin(req, Principal{}, "generation")
		if err != nil {
			t.Fatal(err)
		}
		if method == "box.exec" {
			r.EffectPhase = EffectStarted
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if required, err := store.Recover(); err != nil || !required {
		t.Fatal(required, err)
	}
	s := &Server{Hello: hello(), Receipts: store, RecoveryRequired: true}
	ack := request("control.acknowledge")
	ack.ID = "ack-request-0123456789"
	ack.Payload, _ = json.Marshal(map[string]string{"id": "create-request-0123456789"})
	if err := invoke(t, s, ack, nil, io.Discard, io.Discard); err != nil || s.RecoveryRequired {
		t.Fatal("unknown command result kept the fence after structural review", err)
	}
	if required, err := store.Recover(); err != nil || required {
		t.Fatal(required, err)
	}
	if r, err := store.Read("exec-request-0123456789"); err != nil || r.State != "command-uncertain" {
		t.Fatal(r, err)
	}
}

func TestCommandOutcomeRequiresAuthorizedExecProof(t *testing.T) {
	valid := Receipt{ID: "command-request-0123456789", Method: "box.exec", State: "command-uncertain", Status: 2,
		Started: time.Now().UTC(), Generation: "generation", EffectPhase: EffectStarted}
	for name, mutate := range map[string]func(*Receipt){
		"valid":         func(*Receipt) {},
		"other-method":  func(r *Receipt) { r.Method = "box.create" },
		"legacy-phase":  func(r *Receipt) { r.EffectPhase = "" },
		"never-permit":  func(r *Receipt) { r.EffectPhase = EffectWaiting },
		"setup-claimed": func(r *Receipt) { r.SetupComplete = true },
	} {
		t.Run(name, func(t *testing.T) {
			store := Receipts{t.TempDir()}
			r := valid
			mutate(&r)
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(r.ID); (err == nil) != (name == "valid") {
				t.Fatal(err)
			}
			if _, err := store.List(); (err == nil) != (name == "valid") {
				t.Fatal("listing accepted a forged command outcome", err)
			}
		})
	}
}

func TestUnknownCommandResultsCannotExhaustReceiptRetention(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	save := func(store Receipts, i int, method, state string) {
		r := Receipt{ID: fmt.Sprintf("old-%s-%04d-request", state, i), Method: method, State: state, Status: 2,
			Started: base.Add(time.Duration(i) * time.Second), Generation: "generation", EffectPhase: EffectStarted}
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest records are protected; every other slot holds an unknown command result.
	store := Receipts{t.TempDir()}
	save(store, 0, "box.create", "uncertain")
	save(store, 1, "box.create", "pending")
	for i := 2; i < receiptLimit; i++ {
		save(store, i, "box.exec", "command-uncertain")
	}
	for i := range receiptLimit + 2 {
		req := request("box.exec")
		req.ID = fmt.Sprintf("new-request-%04d", i)
		r, err := store.begin(req, Principal{}, "generation", "", true)
		if err != nil {
			t.Fatal("unknown command results exhausted retention", i, err)
		}
		r.State, r.EffectPhase, r.Finished = "command-uncertain", EffectStarted, time.Now().UTC()
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List()
	if err != nil || len(all) != receiptLimit {
		t.Fatal(len(all), err)
	}
	for _, id := range []string{"old-uncertain-0000-request", "old-pending-0001-request"} {
		if _, err := store.Read(id); err != nil {
			t.Fatal("protected record evicted", id, err)
		}
	}
	// Protected records alone still refuse more mutations instead of evicting.
	full := Receipts{t.TempDir()}
	for i := range receiptLimit {
		save(full, i, "box.create", "uncertain")
	}
	if _, err := full.Begin(request("box.exec"), Principal{}, "generation"); err == nil {
		t.Fatal("evicted an unreviewed controller outcome")
	}
}

func TestEffectGateFailureNeverPermitsWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect")
	for _, action := range []string{"normal", "kill"} {
		_, err := RunWorker(t.Context(), Principal{}, Request{Method: "test.gated", Args: []string{action, path}},
			Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, BeginEffects: func(context.Context, EffectIntent) error {
				return errors.New("receipt save failed")
			}})
		if err == nil {
			t.Fatal("missing gate failure")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("effect escaped denied gate", err)
		}
	}
}

func TestDurableEffectCheckpointWithLostPermitRemainsUncertain(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
		Operations: map[string]Operation{"test.gated": {Mutates: true, EffectGated: true}},
	}
	s.Execute = func(ctx context.Context, p Principal, req Request, streams Streams) (int, error) {
		begin := streams.BeginEffects
		streams.BeginEffects = func(ctx context.Context, intent EffectIntent) error {
			if err := begin(ctx, intent); err != nil {
				return err
			}
			return errors.New("permit lost after durable started checkpoint")
		}
		return RunWorker(ctx, p, req, streams)
	}
	req := request("test.gated")
	path := filepath.Join(t.TempDir(), "effect")
	req.Args = []string{"normal", path}
	_ = invoke(t, s, req, nil, io.Discard, io.Discard)
	r, err := s.Receipts.Read(req.ID)
	if err != nil || r.State != "uncertain" || r.EffectPhase != EffectStarted || !s.RecoveryRequired {
		t.Fatal(r, s.RecoveryRequired, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied worker escaped effect gate", err)
	}
}

func TestBeginEffectsRequiresTrustedContextAndRunsOnce(t *testing.T) {
	if BeginEffects(t.Context()) == nil {
		t.Fatal("missing gate authorized effects")
	}
	calls := 0
	ctx := WithEffectGate(t.Context(), func(context.Context, EffectIntent) error { calls++; return nil })
	for range 2 {
		if err := BeginEffects(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
