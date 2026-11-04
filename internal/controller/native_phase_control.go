package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

// This control read does not start a worker, recover a receipt or allocate a
// trace. Its only input is one original execution ID; event input is forbidden.
func (s *Server) serveNativePhase(conn net.Conn, wire *Wire, principal Principal, req Request) {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	fail := func() {
		_ = wire.Write(Frame{Type: "exit", Status: 2, Error: "native trace requires host administration and one original execution ID"})
	}
	if !principal.Admin() || len(req.Args) != 0 || len(req.Flags) != 0 || req.View || req.Terminal || req.Session != nil || req.Cwd != "" || len(req.Payload) > 128 {
		fail()
		return
	}
	var query struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&query) != nil || decoder.Decode(new(any)) != io.EOF {
		fail()
		return
	}
	// Exact canonical encoding also rejects duplicate keys, which the standard
	// decoder otherwise silently accepts.
	canonical, _ := json.Marshal(query)
	if !bytes.Equal(canonical, req.Payload) || (Request{ID: query.ID, Method: "control.native-trace", Space: s.Hello.Space, Dash: -1}).Validate() != nil {
		fail()
		return
	}
	release, available := s.NativePhases.TryRead()
	if !available {
		_ = wire.Write(Frame{Type: "exit", Status: 2, Error: "native trace read capacity unavailable"})
		return
	}
	defer release()
	raw, err := json.Marshal(s.NativePhases.Read(query.ID))
	if err != nil || len(raw) > 128<<10 {
		_ = wire.Write(Frame{Type: "exit", Status: 2, Error: "native trace unavailable"})
		return
	}
	_ = wire.Write(Frame{Type: "stdout", Data: append(raw, '\n')})
	_ = wire.Write(Frame{Type: "exit"})
}

func (s *Server) reserveNativePhase(p Principal, req Request, receipt *Receipt, operation Operation) *WorkerObservation {
	if s.NativePhases == nil || receipt == nil || !operation.Mutates || !operation.EffectGated {
		return nil
	}
	s.mu.Lock()
	scope := cloneScope(receipt.Scope)
	s.mu.Unlock()
	return s.NativePhases.Reserve(NativePhaseParent{Hello: s.Hello, DiskID: s.DiskID, VMIncarnation: s.VMIncarnation, ExecutionID: req.ID, Method: req.Method, Principal: p, Scope: scope})
}

func nativePhaseResult(err error, status int) string {
	var interrupted *WorkerInterrupted
	var uncertain *EffectsUncertain
	switch {
	case errors.As(err, &interrupted), errors.As(err, &uncertain), errors.Is(err, errUncertainAfterCompletion):
		return "uncertain"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case err != nil || status != 0:
		return "failed"
	default:
		return "ok"
	}
}
