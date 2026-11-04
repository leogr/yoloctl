package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestNativePhaseControlIsExactReadWithoutReceiptOrWorkerEffects(t *testing.T) {
	store := Receipts{t.TempDir()}
	id := "original-request-0123456789"
	saveOutcome(t, store, id, "box.restart", "uncertain", time.Minute)
	before, err := os.ReadFile(store.Dir + "/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Hello: hello(), Receipts: store, RecoveryRequired: true, NativePhases: NewNativePhaseStore(),
		Execute: func(context.Context, Principal, Request, Streams) (int, error) {
			t.Error("trace read launched a worker")
			return 2, nil
		},
	}
	for _, payload := range []string{`{"id":"` + id + `"}`, `{"id":"not-retained-0123456789"}`} {
		var output bytes.Buffer
		req := request("control.native-trace")
		req.Payload = json.RawMessage(payload)
		if err := invoke(t, s, req, nil, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		var got NativePhaseReadout
		if json.Unmarshal(output.Bytes(), &got) != nil || got.State != "unavailable" {
			t.Fatal(output.String())
		}
	}
	after, err := os.ReadFile(store.Dir + "/" + id + ".json")
	if err != nil || !bytes.Equal(before, after) || !s.RecoveryRequired {
		t.Fatal("diagnostics altered recovery/receipt", err)
	}
	for _, payload := range []string{`{}`, `{"id":"short"}`, `{"id":"` + id + `","id":"` + id + `"}`, `{"id":"` + id + `","events":[]}`, `{"id":"` + id + `"} {}`} {
		req := request("control.native-trace")
		req.Payload = json.RawMessage(payload)
		if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted malformed query %s", payload)
		}
	}
}

func TestNativePhaseControlDeniesOperatorAndBoundsStalledRead(t *testing.T) {
	for _, operator := range []bool{true, false} {
		t.Run(map[bool]string{true: "operator", false: "stalled-admin"}[operator], func(t *testing.T) {
			s := &Server{Hello: hello(), NativePhases: NewNativePhaseStore(),
				// A valid endpoint must reach the control method's own admin
				// check; a missing validator refuses at the handshake instead.
				CheckPrincipal: func(context.Context, Principal) error { return nil },
			}
			local, remote := net.Pipe()
			defer func() { _ = local.Close() }()
			principal := Principal{}
			if operator {
				principal = Principal{Box: "caller", Incarnation: "live"}
			}
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), remote, principal) }()
			wire := NewWire(local, local)
			if err := wire.Write(Frame{Type: "hello", Hello: &s.Hello}); err != nil {
				t.Fatal(err)
			}
			if frame, err := wire.Read(); err != nil || frame.Type != "hello" {
				t.Fatal(frame, err)
			}
			req := request("control.native-trace")
			req.Payload = json.RawMessage(`{"id":"original-request-0123456789"}`)
			if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
				t.Fatal(err)
			}
			if operator {
				frame, err := wire.Read()
				if err != nil || frame.Type != "exit" || frame.Status != 2 {
					t.Fatal(frame, err)
				}
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("trace read held a stalled connection")
			}
		})
	}
}

func TestNativePhaseControlHasNoInputStream(t *testing.T) {
	if methodHasInput("control.native-trace") {
		t.Fatal("trace read would send EOF/reset an administrative response")
	}
}
