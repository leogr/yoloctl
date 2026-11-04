package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
)

type Size struct{ Cols, Rows uint16 }

type Streams struct {
	NativePhase    *WorkerObservation // optional diagnostic custody
	WorkerHandoff  workerHandoffOwner // SAME original execution owner
	PIDAnchor      WorkerPIDAnchor
	PIDAnchorCheck func(context.Context, string) error
	// NativeNonce is the original effect-gated execution's private allocator.
	// Neither a public frame nor possession of its returned identity grants it.
	NativeNonce NativeNonceCall
	// OperatorRetire retires a native operator listener for the original
	// effect-gated removal execution; see retireOperatorListener.
	OperatorRetire OperatorRetireCall
	Advisory       AdvisoryCall
	Image          ImageCall
	In             io.Reader
	Out            io.Writer
	Err            io.Writer
	Resize         <-chan Size
	Signals        <-chan int
	View           io.ReadWriteCloser
	// Release marks runtime setup complete before a long-lived attachment.
	// It must be called only after the native mutation lock is released.
	// ErrClientDetached reports a completed setup whose client already left.
	Release func() error
	// BeginEffects durably records the worker's intent and permits its
	// first effect.
	BeginEffects func(context.Context, EffectIntent) error
	// RecordEvidence durably extends the receipt after permission.
	RecordEvidence EffectRecorder
}

type Handler func(context.Context, Principal, Request, Streams) (int, error)

type Server struct {
	NativePhases         *NativePhaseStore
	VMIncarnation        string
	operatorPublications operatorPublicationClaims
	// Set only from actual launch admission BEFORE workers are exposed, never
	// a process-view constructor. Immutable for this controller lifetime.
	PIDAnchor  WorkerPIDAnchor
	lockHolder *lockAdvisory
	Images     *ImageJobs
	Hello      Hello
	Operations map[string]Operation
	Execute    Handler
	// CheckPrincipal revalidates the endpoint's runtime grant/incarnation.
	CheckPrincipal func(context.Context, Principal) error
	Receipts       Receipts
	// Maintenance persists admission closure before drain is acknowledged.
	Maintenance func() error
	// ResumeAdmission durably clears maintenance after an explicit host export.
	ResumeAdmission func() error
	// Set on startup when a worker died before recording its setup/result.
	// Reads remain available so a host operator can inspect and acknowledge.
	RecoveryRequired bool
	// MutationFinished schedules disposable-storage maintenance after setup.
	MutationFinished func(string)
	// ReadFinished is told the method of each finished read, so background
	// observation can run while someone looks. It must not block.
	ReadFinished func(string)
	// DiskID binds new receipts' recovery evidence to the verified native
	// disk; empty keeps the legacy unscoped schema (tests only).
	DiskID string
	// RecoveryKey fingerprints document proofs. Without it no proof is
	// recorded, which leaves an interrupted outcome unresolved.
	RecoveryKey *RecoveryKey
	// Verify reads back the documents an unresolved receipt's proofs name,
	// through the read-only verifier worker. Nil disables proof checks.
	Verify func(context.Context, Receipt) ([]DocumentIntent, error)
	// VerifyStorage revalidates disk identity and the private receipt store
	// before failed receipt storage can be reconciled.
	VerifyStorage func(context.Context) error

	mu       sync.Mutex
	draining bool
	active   int
	idle     chan struct{}
	// A failed receipt flush cannot be cleared by an in-memory acknowledgment.
	// Restart must inspect the durable records after storage has been repaired.
	receiptFailure   error
	backgroundCancel context.CancelFunc
	// changed is closed by a receipt transition that can end a reservation.
	changed chan struct{}

	// Recovery: the failure ledger, a degraded-startup repair, and the single
	// checker's wake, cancellation and progress.
	ledger         map[string]ledgerEntry
	startupRepair  func(context.Context) error
	recoveryWake   chan struct{}
	recoveryCancel context.CancelFunc
	recoveryIdle   chan struct{}
	recoveryCursor string
	mismatched     map[string]bool
}

func (s *Server) Drain(ctx context.Context) error {
	s.mu.Lock()
	if !s.draining {
		if s.Maintenance != nil {
			if err := s.Maintenance(); err != nil {
				s.mu.Unlock()
				return err
			}
		}
		s.draining = true
	}
	if s.backgroundCancel != nil {
		s.backgroundCancel()
	}
	// A running recovery check ends before drain is acknowledged; no pass
	// starts while draining, and none reopens maintenance.
	var checking <-chan struct{}
	if s.recoveryCancel != nil {
		s.recoveryCancel()
		checking = s.recoveryIdle
	}
	idle := s.idle
	active := s.active != 0
	s.mu.Unlock()
	if checking != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checking:
		}
	}
	if !active {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idle:
		return nil
	}
}

// Resume reopens admission without restarting workers or clearing uncertain
// receipts. It cannot override active setup or a receipt-storage failure.
func (s *Server) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.draining {
		return nil
	}
	if s.active != 0 || s.receiptFailure != nil {
		return errors.New("controller cannot resume before setup and receipt storage are healthy")
	}
	if s.ResumeAdmission != nil {
		if err := s.ResumeAdmission(); err != nil {
			return err
		}
	}
	s.draining = false
	return nil
}

func (s *Server) admit(req Request, principal Principal, mutates bool) (func(), *Receipt, error) {
	return s.admitOperation(req, principal, Operation{Mutates: mutates})
}

func (s *Server) admitOperation(req Request, principal Principal, operation Operation) (func(), *Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return nil, nil, errors.New("space controller is in maintenance; start the space to reopen it")
	}
	if !operation.Mutates {
		if s.ReadFinished == nil {
			return func() {}, nil, nil
		}
		var once sync.Once
		return func() { once.Do(func() { s.ReadFinished(req.Method) }) }, nil, nil
	}
	if s.receiptFailure != nil {
		return nil, nil, fmt.Errorf("operation receipt storage failed, so changes are paused; reads still work and the controller reopens changes by itself once storage is healthy and every receipt is reconciled: %w", s.receiptFailure)
	}
	// Only a space-wide unresolved outcome refuses admission; a scoped hold
	// is checked against the footprint the worker declares at its gate.
	current, err := s.loadHolds("")
	if err != nil {
		return nil, nil, fmt.Errorf("operation receipts cannot be inventoried, so changes are paused: %w", err)
	}
	if len(current.unresolvedGlobal()) != 0 {
		return nil, nil, recoveryRequiredError(s.Hello.Space)
	}
	r, err := s.Receipts.begin(req, principal, s.Hello.Generation, s.DiskID, operation.EffectGated)
	var writeErr *receiptWriteError
	if errors.As(err, &writeErr) {
		// No worker runs; its possibly-published pending record is
		// reconciled before the storage hold can drop.
		s.record(r, err, true, false, "")
	}
	if err != nil {
		return nil, nil, err
	}
	if s.backgroundCancel != nil {
		s.backgroundCancel() // admitted mutations take priority over GC
	}
	if s.active == 0 {
		s.idle = make(chan struct{})
	}
	s.active++
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			if s.active == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
			if s.MutationFinished != nil {
				s.MutationFinished(req.Method)
			}
		})
	}
	return release, &r, nil
}

// recoveryRequiredError names the host review path for an unknown outcome
// without a bounded footprint, which holds the whole space.
func recoveryRequiredError(space string) error {
	return fmt.Errorf("an interrupted operation without a trustworthy footprint may have changed space %[1]s, so changes are paused until a host reviews it; reads still work. "+
		"On the host, `yoloctl space operation %[1]s --acknowledge-all` accepts every finished unknown outcome at once; "+
		"`yoloctl space operation %[1]s [ID]` lists or inspects them, and `yoloctl space operation %[1]s ID --acknowledge` accepts one. "+
		"Acknowledging neither retries nor undoes an operation; a pending operation may still be running and is never acknowledged", space)
}

// saveReview persists one acknowledgment; tests inject a partial failure.
var saveReview = Receipts.Save

// reviewedOutcome identifies a receipt in an acknowledge-all summary.
type reviewedOutcome struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	State  string `json:"state"`
}

type acknowledgeAllResult struct {
	Acknowledged []reviewedOutcome `json:"acknowledged"`
	// Failed may or may not have been saved; Unattempted were not touched.
	Failed        *reviewedOutcome  `json:"failed,omitempty"`
	Unattempted   []reviewedOutcome `json:"unattempted,omitempty"`
	Pending       []string          `json:"pending,omitempty"`
	Maintenance   bool              `json:"maintenance,omitempty"`
	ChangesPaused bool              `json:"changes_paused"`
}

// acknowledgeAll records one explicit host decision over the finished
// unknown outcomes in the current inventory. Running work is untouched; a
// later outcome that needs controller recovery may pause changes again.
// Maintenance is independent and stays in effect. A save failure stops with
// the saved prefix; nothing is retried, rolled back or reported as success.
func (s *Server) acknowledgeAll() (*acknowledgeAllResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receiptFailure != nil {
		return nil, errors.New("receipt storage must be healthy and reconciled before acknowledging outcomes")
	}
	all, err := s.Receipts.List()
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Started.Before(all[j].Started) })
	result := &acknowledgeAllResult{Acknowledged: []reviewedOutcome{}, Maintenance: s.draining}
	var selected []Receipt
	for _, r := range all {
		switch r.State {
		case "uncertain", "command-uncertain":
			selected = append(selected, r)
		case "pending":
			result.Pending = append(result.Pending, r.ID)
		}
	}
	for i, r := range selected {
		outcome := reviewedOutcome{r.ID, r.Method, r.State}
		prior := r.State
		r.State = "acknowledged"
		if err := saveReview(s.Receipts, r); err != nil {
			s.record(r, err, true, true, prior)
			result.Failed = &outcome
			hold := outcome.State == "uncertain"
			for _, rest := range selected[i+1:] {
				result.Unattempted = append(result.Unattempted, reviewedOutcome{rest.ID, rest.Method, rest.State})
				hold = hold || rest.State == "uncertain"
			}
			s.RecoveryRequired = hold
			result.ChangesPaused = true
			return result, fmt.Errorf("receipt storage failed while acknowledging %s in space %s; the decision is read back, never repeated, once storage is healthy: %w", r.ID, s.Hello.Space, err)
		}
		result.Acknowledged = append(result.Acknowledged, outcome)
	}
	// The snapshot is the complete inventory under the mutex: no uncertain
	// record remains, and a later one will set the hold again.
	_ = s.refreshRecovery()
	result.ChangesPaused = s.draining
	return result, nil
}

func (r *acknowledgeAllResult) explain(space string) string {
	var b strings.Builder
	if len(r.Acknowledged) == 0 && r.Failed == nil {
		fmt.Fprintf(&b, "No uncertain outcomes to acknowledge in space %s.\n", space)
	} else {
		fmt.Fprintf(&b, "Acknowledged %d unknown outcome(s) in space %s; none was retried or undone.\n", len(r.Acknowledged), space)
	}
	if r.Failed != nil {
		fmt.Fprintf(&b, "Receipt storage failed on %s, which may or may not be acknowledged; %d other outcome(s) were not attempted.\n", r.Failed.ID, len(r.Unattempted))
	}
	if len(r.Pending) > 0 {
		fmt.Fprintf(&b, "%d running operation(s) were left untouched; a later outcome that needs controller recovery may pause changes again.\n", len(r.Pending))
	}
	if r.Maintenance {
		fmt.Fprintf(&b, "Space %[1]s is in maintenance; start it with yoloctl space start %[1]s to reopen changes.\n", space)
	}
	if r.ChangesPaused {
		fmt.Fprintf(&b, "Changes in space %s remain paused.\n", space)
	} else {
		fmt.Fprintf(&b, "Changes in space %s are not paused.\n", space)
	}
	return b.String()
}

func (s *Server) check(ctx context.Context, p Principal) error {
	if s.CheckPrincipal != nil {
		return s.CheckPrincipal(ctx, p)
	}
	if !p.Admin() {
		return errors.New("operator endpoint validation is unavailable")
	}
	return nil
}

// ServeConn accepts one request per connection. The principal comes from the
// listener provisioned by infrastructure, never from wire input.
func (s *Server) ServeConn(parent context.Context, conn net.Conn, principal Principal) {
	defer func() { _ = conn.Close() }()
	// connCtx is the public client's lifetime. An admitted worker runs under
	// a separate execution context so an owned setup can outlive its client.
	connCtx, cancelConn := context.WithCancel(parent)
	defer cancelConn()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-connCtx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	wire := NewWire(conn, conn)
	fail := func(err error) { _ = wire.Write(Frame{Type: "exit", Status: 2, Error: err.Error()}) }
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	frame, err := wire.Read()
	if err != nil {
		return
	}
	if frame.Type != "hello" || frame.Hello == nil {
		fail(errors.New("controller handshake required"))
		return
	}
	if err := MatchHello(*frame.Hello, s.Hello); err != nil {
		fail(err)
		return
	}
	if err := s.check(connCtx, principal); err != nil {
		fail(err)
		return
	}
	if err := wire.Write(Frame{Type: "hello", Hello: &s.Hello}); err != nil {
		return
	}
	frame, err = wire.Read()
	if err != nil {
		return
	}
	if frame.Type != "request" || frame.Request == nil {
		fail(errors.New("controller request required"))
		return
	}
	req := *frame.Request
	if err := req.Validate(); err != nil {
		fail(err)
		return
	}
	if req.Space != s.Hello.Space {
		fail(errors.New("request targets a different space"))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	// Control methods cannot execute arbitrary workers.
	switch req.Method {
	case "control.native-trace":
		s.serveNativePhase(conn, wire, principal, req)
		return
	case "control.ping":
		if !principal.Admin() {
			fail(errors.New("box operator cannot query controller administration"))
			return
		}
		s.mu.Lock()
		drained := s.draining
		receiptFailure := s.receiptFailure
		s.mu.Unlock()
		// A read-only controller is serving: reads work and it reconciles
		// its receipt storage by itself. Restarting it is never required.
		if receiptFailure != nil && !drained {
			_ = wire.Write(Frame{Type: "stderr", Data: []byte(fmt.Sprintf("Space %s controller is read-only until receipt storage recovers: %v\n", s.Hello.Space, receiptFailure))})
		}
		if drained {
			fail(errors.New("controller is in maintenance"))
			return
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	case "control.resume":
		if !principal.Admin() {
			fail(errors.New("box operator cannot resume a space"))
			return
		}
		if err := s.Resume(); err != nil {
			fail(err)
			return
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	case "control.drain":
		if !principal.Admin() {
			fail(errors.New("box operator cannot drain a space"))
			return
		}
		drainCtx, stop := context.WithTimeout(connCtx, 2*time.Minute)
		defer stop()
		if err := s.Drain(drainCtx); err != nil {
			fail(err)
			return
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	case "control.receipts":
		if !principal.Admin() {
			fail(errors.New("only host administration can list operation receipts"))
			return
		}
		s.mu.Lock()
		all, err := s.Receipts.List()
		summary := ""
		if err == nil {
			summary = explainHolds(all, s)
			orphans, e := s.Receipts.ImageOrphans()
			if e != nil {
				err = e
			} else if len(orphans) != 0 {
				summary += fmt.Sprintf("%d abandoned image attempt(s) may still use CPU, memory or disk; their slots are free and no acknowledgment is required.\n", len(orphans))
			}
		}
		s.mu.Unlock()
		if err != nil {
			fail(err)
			return
		}
		for i := range all {
			all[i] = publicReceipt(all[i])
		}
		raw, _ := json.Marshal(all)
		_ = wire.Write(Frame{Type: "stdout", Data: append(raw, '\n')})
		if summary != "" {
			_ = wire.Write(Frame{Type: "stderr", Data: []byte(summary)})
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	case "control.status", "control.acknowledge":
		var query struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Payload, &query); err != nil {
			fail(errors.New("invalid outcome query"))
			return
		}
		s.mu.Lock()
		r, err := s.Receipts.Read(query.ID)
		s.mu.Unlock()
		if err != nil || !principal.Admin() && r.Principal != principal {
			fail(errors.New("operation receipt not available"))
			return
		}
		if req.Method == "control.acknowledge" {
			if !principal.Admin() {
				fail(errors.New("only host administration can acknowledge uncertain outcomes"))
				return
			}
			s.mu.Lock()
			// Pending operations are still active. Acknowledgment records a
			// human recovery decision; it never turns uncertainty into success.
			// An unknown command result may be marked reviewed but never needs it.
			r, err = s.Receipts.Read(query.ID)
			if s.receiptFailure != nil {
				err = errors.New("receipt storage must be healthy and reconciled before acknowledging outcomes")
			}
			if err == nil && r.State != "uncertain" && r.State != "command-uncertain" {
				err = errors.New("only an uncertain or command-uncertain receipt can be acknowledged")
			}
			if err == nil {
				prior := r.State
				r.State = "acknowledged"
				err = s.Receipts.Save(r)
				s.record(r, err, true, true, prior)
			}
			if err == nil {
				// Releases only this receipt's holds; others remain derived.
				err = s.refreshRecovery()
			}
			s.mu.Unlock()
			if err != nil {
				fail(err)
				return
			}
		}
		raw, _ := json.Marshal(publicReceipt(r))
		_ = wire.Write(Frame{Type: "stdout", Data: append(raw, '\n')})
		if principal.Admin() {
			if text := s.explainOutcome(r); text != "" {
				_ = wire.Write(Frame{Type: "stderr", Data: []byte(text)})
			}
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	case "control.acknowledge-all":
		if !principal.Admin() {
			fail(errors.New("only host administration can acknowledge uncertain outcomes"))
			return
		}
		result, err := s.acknowledgeAll()
		if result != nil {
			raw, _ := json.Marshal(result)
			_ = wire.Write(Frame{Type: "stdout", Data: append(raw, '\n')})
			_ = wire.Write(Frame{Type: "stderr", Data: []byte(result.explain(s.Hello.Space))})
		}
		if err != nil {
			fail(err)
			return
		}
		_ = wire.Write(Frame{Type: "exit"})
		return
	}
	if err := Authorize(principal, req, s.Hello.Space, s.Operations); err != nil {
		fail(err)
		return
	}
	operation := s.Operations[req.Method]
	release, receipt, err := s.admitOperation(req, principal, operation)
	if err != nil {
		fail(err)
		return
	}
	defer release()
	// The worker follows execCtx only. Before an owned setup is authorized,
	// and for every other operation, the client's departure cancels it.
	execCtx, cancelExec := context.WithCancel(parent)
	defer cancelExec()
	output := &clientOutput{onDetach: cancelConn}
	owner := newSetupOwner(operation.OwnedSetup, connCtx, cancelExec, output)
	go func() {
		select {
		case <-connCtx.Done():
			owner.detach()
		case <-closed:
		}
	}()
	input, inputWriter := io.Pipe()
	defer func() { _ = input.Close() }()
	defer func() { _ = inputWriter.Close() }()
	var view, viewWorker *os.File
	var viewIncoming chan Frame
	if req.View {
		view, viewWorker, err = termio.ViewPair()
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = view.Close() }()
		defer func() { _ = viewWorker.Close() }()
		viewIncoming = make(chan Frame, 8)
		go func() { <-execCtx.Done(); _ = view.Close(); _ = viewWorker.Close() }()
		go func() {
			// An owned setup holds client input until handoff; a detached
			// client's queued bytes are discarded.
			select {
			case <-owner.released:
			case <-connCtx.Done():
				return
			}
			for {
				select {
				case <-connCtx.Done():
					return
				case f := <-viewIncoming:
					if err := writeAll(view, f.Data); err != nil {
						cancelConn()
						return
					}
					if err := wire.Write(Frame{Type: "view-ready"}); err != nil {
						cancelConn()
						return
					}
				}
			}
		}()
	}
	sizes := make(chan Size, 1)
	signals := make(chan int, 4)
	incoming := make(chan Frame, 9) // eight credited data frames plus EOF
	go func() {
		<-connCtx.Done()
		_ = inputWriter.CloseWithError(connCtx.Err())
	}()
	go func() {
		select {
		case <-owner.released:
		case <-connCtx.Done():
			return
		}
		for {
			select {
			case <-connCtx.Done():
				return
			case f := <-incoming:
				if f.Type == "eof" {
					_ = inputWriter.Close()
					return
				}
				if _, err := inputWriter.Write(f.Data); err != nil {
					return
				}
				if err := wire.Write(Frame{Type: "input-ready"}); err != nil {
					cancelConn()
					return
				}
			}
		}
	}()
	// Bound unread stdin without allowing it to block cancellation or resize.
	// A client flooding input to a command that never reads it is disconnected.
	// Malformed frames detach the client; only the owner decides the worker.
	go func() {
		defer cancelConn()
		for {
			f, err := wire.Read()
			if err != nil {
				return
			}
			switch f.Type {
			case "view-input":
				if !req.View || len(f.Data) > 32768 {
					return
				}
				select {
				case viewIncoming <- f:
				default:
					return
				}
			case "stdin", "eof":
				if len(f.Data) > 65536 {
					return
				}
				select {
				case incoming <- f:
				default:
					return
				}
			case "resize":
				select {
				case <-sizes:
				default:
				}
				select {
				case sizes <- Size{f.Cols, f.Rows}:
				default:
				}
			case "signal":
				if f.Signal != 1 && f.Signal != 2 && f.Signal != 3 && f.Signal != 15 {
					return
				}
				if forward, owned := owner.signal(f.Signal); !forward {
					// Before handoff a signal ends the client, never the setup.
					_ = wire.Write(Frame{Type: "detached", Signal: f.Signal, Owned: owned})
					return
				}
				select {
				case signals <- f.Signal:
				default:
				}
			case "cancel":
				return
			default:
				return
			}
		}
	}()
	var selfRetired atomic.Bool
	if !principal.Admin() {
		// Authority is rechecked without a client; failure is a controller
		// decision that ends the execution, owned or not.
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-execCtx.Done():
					return
				case <-ticker.C:
					probe, stop := context.WithTimeout(execCtx, 5*time.Second)
					err := s.check(probe, principal)
					stop()
					if err != nil && selfRetired.Load() {
						// This execution retired its own box's listener: losing
						// the grant is its expected self-retirement, not an
						// unrelated revocation. Nothing new is admitted on it.
						return
					}
					if err != nil {
						cancelExec()
						cancelConn()
						return
					}
				}
			}
		}()
	}
	setupDone := func() error {
		attached, err := owner.handoff(func() error {
			if receipt == nil || !setupThenAttach(req.Method) {
				return nil
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			receipt.SetupComplete = true
			err := s.Receipts.Save(*receipt)
			s.record(*receipt, err, false, false, "")
			if err != nil {
				return err
			}
			s.notifyReceipts()
			return nil
		})
		if err != nil {
			cancelExec()
		}
		release()
		if err == nil && !attached {
			err = ErrClientDetached
		}
		return err
	}
	var viewDone chan struct{}
	if req.View {
		viewDone = make(chan struct{})
		go func() {
			defer close(viewDone)
			_, _ = io.Copy(clientSink{frameWriter{wire, "view"}, output}, view)
		}()
	}
	var imageProgressOutput *imageProgress
	defer func() { imageProgressOutput.finish() }()
	var beginEffects func(context.Context, EffectIntent) error
	if receipt != nil && operation.EffectGated {
		beginEffects = func(effectCtx context.Context, intent EffectIntent) error {
			beforeEffectGate(connCtx)
			scope := intent.Scope
			if receipt.ProofVersion == 0 {
				scope = nil // a legacy receipt's footprint is the whole space
			}
			// Waiting happens outside the setup owner, so a departing client
			// still cancels a gate that has not been granted.
			if err := s.awaitReservations(effectCtx, receipt.ID, scope); err != nil {
				return err
			}
			err := owner.authorize(effectCtx, func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.receiptFailure != nil {
					return errors.New("operation effects require healthy receipt storage")
				}
				if receipt.EffectPhase != EffectWaiting {
					return errors.New("operation effects were already authorized")
				}
				if intent.Scope != nil && !intent.Scope.Global {
					if err := operation.Footprint.allows(intent.Scope.Writes); err != nil {
						return err
					}
				}
				// The second, authoritative check: a hold created while this
				// worker waited for its lock still refuses an intersecting
				// effect.
				current, err := s.loadHolds(receipt.ID)
				if err != nil {
					return err
				}
				if resources, held, global := current.conflict(scope); len(held) != 0 {
					return heldError(s.Hello.Space, principal.Admin(), global, resources, held)
				}
				next := *receipt
				// Scope and verifier are durable in the same update that
				// grants permission; a legacy receipt records neither.
				if next.ProofVersion > 0 {
					next.Scope, next.Verifier = intent.Scope, intent.Verifier
					next.ProofVersion = intent.Scope.proofVersion()
				}
				next.EffectPhase = EffectStarted
				if err := s.Receipts.Save(next); err != nil {
					// Permission is denied; the durable record may read
					// either way and is reconciled before reopening.
					s.record(*receipt, err, false, false, "")
					return err
				}
				*receipt = next
				return nil
			})
			if err == nil {
				beforeEffectPermit(connCtx)
			}
			return err
		}
	}
	var evidence EffectRecorder
	if receipt != nil {
		evidence = &receiptEvidence{server: s, receipt: receipt, footprint: operation.Footprint, admin: principal.Admin()}
	}
	// Each execution owns its interests until its apply/worker ends.
	pins := map[string]*RetainedImage{}
	lastImageDigest := ""
	defer func() {
		for _, p := range pins {
			p.Release()
		}
	}()
	defer s.clearAdvisory(req.ID)
	advisoryCall := func(ctx context.Context, n AdvisoryNotice) (AdvisoryReply, error) {
		reply, err := s.advisory(ctx, req, n)
		if n.Action == "acquired" && lastImageDigest != "" {
			s.mu.Lock()
			if s.lockHolder != nil && s.lockHolder.owner == req.ID {
				s.lockHolder.summary = boundedImageSummary(s.lockHolder.summary, PinnedImage{Digest: lastImageDigest})
			}
			s.mu.Unlock()
		}
		return reply, err
	}
	var imageInterrupted *ImageInterrupted
	var imagePublicationError error
	imageCall := func(ctx context.Context, r ImageRequest) (ImageReply, error) {
		if s.Images == nil {
			return ImageReply{}, errors.New("controller image jobs unavailable")
		}
		service, ok := s.Images.backend.(ImageService)
		if !ok {
			return ImageReply{}, errors.New("controller image service unavailable")
		}
		switch r.Action {
		case "prepared":
			// The driver calls this only outside native apply guards. Draining here
			// must never migrate into BeginEffects, which runs while guards are held.
			imageProgressOutput.finish()
			imageProgressOutput = nil
			return ImageReply{}, nil
		case "preparing":
			if err := ValidatePublicImageReference(r.Reference); err != nil {
				return ImageReply{}, err
			}
			if r.Box != "" {
				if err := layout.ValidateName("box", r.Box); err != nil {
					return ImageReply{}, err
				}
			}
			if imageProgressOutput == nil {
				imageProgressOutput = newImageProgress(connCtx, clientSink{frameWriter{wire, "stderr"}, output})
			}
			description := r.Reference
			if r.Box != "" {
				description += " for box " + r.Box
			}
			imageProgressOutput.preparing(description)
			return ImageReply{}, nil
		case "check-desired":
			resource := Resource{Kind: ResourceImages, Name: r.Reference}
			if err := resource.validate(); err != nil {
				return ImageReply{}, err
			}
			scope := &Scope{Reads: []Resource{resource}}
			if req.Method == "image.pull" || req.Method == "image.refresh" {
				scope = &Scope{Writes: []Resource{{Kind: ResourceImages}}}
			}
			s.mu.Lock()
			h, err := s.loadHolds(req.ID)
			if err == nil {
				if resources, held, global := h.conflict(scope); len(held) != 0 {
					err = heldError(s.Hello.Space, principal.Admin(), global, resources, held)
				}
			}
			s.mu.Unlock()
			return ImageReply{}, err
		case "resolve-local":
			return resolveLocalImageCheckpoint(ctx, s.Images.backend, r)
		case "resolve":
			resolve := service.Resolve
			if r.Remote {
				resolve = service.ResolveRemote
			}
			d, p, e := resolve(ctx, r.Reference)
			return ImageReply{Digest: d, Platform: p}, e
		case "acquire":
			s.mu.Lock()
			canPrepare := receipt != nil && receipt.EffectPhase == EffectWaiting
			s.mu.Unlock()
			if !canPrepare {
				return ImageReply{}, errors.New("image preparation must precede apply")
			}
			progress := imageProgressOutput
			p, e := s.Images.acquireWithProgress(ctx, r.Recipe, summaryForRequest(req), func(wait ImageWait) {
				if progress != nil {
					_, _ = io.WriteString(progress, wait.String())
				}
			}, progress)
			if e != nil {
				var interrupted *ImageInterrupted
				if errors.As(e, &interrupted) && interrupted.Retryable {
					imageInterrupted = interrupted
				}
				return ImageReply{}, e
			}
			pins[p.JobID] = p
			lastImageDigest = p.Digest
			return ImageReply{Pin: p.PinnedImage}, nil
		case "validate":
			p := pins[r.JobID]
			if p == nil {
				return ImageReply{}, errors.New("execution does not own this image")
			}
			return ImageReply{}, s.Images.backend.Validate(ctx, p.PinnedImage)
		case "publish":
			p := pins[r.JobID]
			if p == nil {
				return ImageReply{}, errors.New("execution does not own this image")
			}
			s.mu.Lock()
			allowed := receipt != nil && receipt.EffectPhase == EffectStarted && receipt.Completion == nil
			if allowed {
				allowed = receipt.Scope == nil || receipt.Scope.Global || containsResource(receipt.Scope.Writes, Resource{Kind: ResourceImages, Name: r.Reference})
			}
			s.mu.Unlock()
			if !allowed {
				return ImageReply{}, errors.New("image publication lacks apply permission")
			}
			if err := service.Publish(ctx, p.PinnedImage, r.Reference); err != nil {
				imagePublicationError = &EffectsUncertain{Cause: err}
				return ImageReply{}, imagePublicationError
			}
			return ImageReply{}, nil
		default:
			return ImageReply{}, errors.New("unsupported private image operation")
		}
	}
	nativeNonces := s.newNativeNonceOwner(execCtx, principal, req, receipt, operation)
	defer nativeNonces.close()
	var nativeNonceCall NativeNonceCall
	var operatorRetire OperatorRetireCall
	var pidAnchor WorkerPIDAnchor
	var pidAnchorCheck func(context.Context, string) error
	var workerHandoff workerHandoffOwner
	if nativeNonces != nil {
		nativeNonceCall = nativeNonces.allocate
		operatorRetire = func(ctx context.Context, box, incarnation string) error {
			return nativeNonces.retireOperatorListener(ctx, box, incarnation, conn, func() { selfRetired.Store(true) })
		}
		pidAnchor = nativeNonces.pidAnchor
		workerHandoff = newNativeWorkerHandoffOwner(nativeNonces)
		if workerHandoff != nil {
			defer func() { _ = workerHandoff.Close() }()
			pidAnchorCheck = workerHandoff.CheckPIDAnchor
		} else {
			pidAnchorCheck = func(context.Context, string) error { return errors.New("original worker handoff unavailable") }
		}
	}
	phase := s.reserveNativePhase(principal, req, receipt, operation)
	status, runErr := s.Execute(execCtx, principal, req, Streams{
		NativePhase: phase, WorkerHandoff: workerHandoff,
		In: input, Out: clientSink{frameWriter{wire, "stdout"}, output}, Err: clientSink{frameWriter{wire, "stderr"}, output},
		Resize: sizes, Signals: signals, Release: setupDone, View: viewWorker,
		BeginEffects: beginEffects, RecordEvidence: evidence, Image: imageCall, Advisory: advisoryCall, NativeNonce: nativeNonceCall, OperatorRetire: operatorRetire,
		PIDAnchor: pidAnchor, PIDAnchorCheck: pidAnchorCheck,
	})
	nativeNonces.close() // actual Execute/RunWorker has ended: no retained allocation grant
	imageProgressOutput.finish()
	imageProgressOutput = nil
	if req.View {
		_ = viewWorker.Close()
		<-viewDone
	}
	if imagePublicationError != nil {
		runErr = errors.Join(runErr, imagePublicationError)
		if receipt != nil {
			receipt.Completion = nil
		}
	}
	if runErr != nil && status == 0 {
		status = 2
	}
	if phase != nil {
		s.mu.Lock()
		var scope *Scope
		if receipt != nil {
			scope = cloneScope(receipt.Scope)
		}
		s.mu.Unlock()
		phase.Result(status, nativePhaseResult(runErr, status), scope)
	}

	if receipt != nil {
		detached, signal := owner.outcome()
		s.mu.Lock()
		receipt.Status, receipt.Finished = status, time.Now().UTC()
		receipt.State = "complete"
		receipt.ClientDetached = detached
		if signal != 0 {
			receipt.DetachSignal = SignalName(signal)
		}
		// The execution outcome decides; a departed client alone never does.
		var interrupted *WorkerInterrupted
		var uncertain *EffectsUncertain
		if errors.Is(runErr, errUncertainAfterCompletion) {
			// A contradicted checkpoint proves nothing.
			receipt.Completion = nil
		}
		if execCtx.Err() != nil || errors.As(runErr, &interrupted) || errors.As(runErr, &uncertain) {
			receipt.interrupt()
		}
		if receipt.State == "uncertain" {
			s.RecoveryRequired = true
		}
		saveErr := s.Receipts.Save(*receipt)
		s.record(*receipt, saveErr, true, false, "")
		if saveErr == nil && receipt.State != "uncertain" {
			_ = s.refreshRecovery()
		}
		if receipt.State == "uncertain" {
			s.wakeRecovery()
		}
		s.notifyReceipts()
		s.mu.Unlock()
		if saveErr != nil {
			fail(fmt.Errorf("operation %s ended but its receipt could not be flushed: %w", req.ID, saveErr))
			return
		}
	}
	exit := Frame{Type: "exit", Status: status}
	if imageInterrupted != nil && receipt != nil && receipt.EffectPhase == EffectWaiting && (receipt.State == "complete" || receipt.Recovery != nil && receipt.Recovery.Disposition == DispositionNotApplied) && execCtx.Err() == nil {
		exit.ImageRetryJob = imageInterrupted.JobID
	}
	if runErr != nil {
		exit.Error = runErr.Error()
	}
	_ = wire.Write(exit)
}

// receiptEvidence extends one admitted receipt after its effect permission.
// Each update is durable before the worker is acknowledged; a failed flush
// is a receipt-storage failure and the worker must not perform
// the effect it announced.
type receiptEvidence struct {
	server    *Server
	receipt   *Receipt
	footprint Footprint
	admin     bool
}

func (e *receiptEvidence) update(change func(*Receipt) error) error {
	s := e.server
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receiptFailure != nil {
		return errors.New("operation receipt storage failed")
	}
	if e.receipt.ProofVersion == 0 || e.receipt.EffectPhase == EffectWaiting || e.receipt.Completion != nil {
		return errors.New("operation evidence is recorded only after permission and before completion")
	}
	next := *e.receipt
	next.Scope = cloneScope(next.Scope)
	next.Proofs = append([]Proof(nil), next.Proofs...)
	if err := change(&next); err != nil {
		return err
	}
	if err := next.validateEvidence(); err != nil {
		return err
	}
	if err := s.Receipts.Save(next); err != nil {
		s.record(*e.receipt, err, false, false, "")
		return err
	}
	s.record(next, nil, false, false, "")
	*e.receipt = next
	s.notifyReceipts()
	return nil
}

func (e *receiptEvidence) ExpandEffects(_ context.Context, writes []Resource) error {
	return e.update(func(r *Receipt) error {
		if len(writes) == 0 {
			return errors.New("an operation scope expansion names no resource")
		}
		if err := e.footprint.allows(writes); err != nil {
			return err
		}
		if r.Scope == nil || r.Scope.Global {
			return nil // already the whole space
		}
		current, err := e.server.loadHolds(r.ID)
		if err != nil {
			return err
		}
		if resources, held, global := current.conflict(&Scope{Writes: writes}); len(held) != 0 {
			return heldError(e.server.Hello.Space, e.admin, global, resources, held)
		}
		for _, w := range writes {
			if err := w.validate(); err != nil {
				return err
			}
			if !containsResource(r.Scope.Writes, w) {
				r.Scope.Writes = append(r.Scope.Writes, w)
			}
		}
		if version := r.Scope.proofVersion(); version > r.ProofVersion {
			if len(r.Proofs) != 0 {
				return errors.New("cannot change a document proof schema after recording proofs")
			}
			r.ProofVersion = version
		}
		return nil
	})
}

func (e *receiptEvidence) RecordDocument(_ context.Context, d DocumentIntent) error {
	return e.update(func(r *Receipt) error {
		if err := d.validate(r.Verifier); err != nil {
			return err
		}
		key := e.server.RecoveryKey
		if key == nil {
			return errors.New("controller recovery key is unavailable")
		}
		if len(r.Proofs) >= maxProofs {
			return errors.New("operation proofs exceed their limit")
		}
		r.Proofs = append(r.Proofs, key.proofVersion(r.ProofVersion, r.DiskID, r.ID, r.Verifier, d))
		return nil
	})
}

func (e *receiptEvidence) CompleteEffects(_ context.Context, status int) error {
	return e.update(func(r *Receipt) error {
		r.Completion = &Completion{Status: status, At: time.Now().UTC()}
		return nil
	})
}

func cloneScope(s *Scope) *Scope {
	if s == nil {
		return nil
	}
	return &Scope{Global: s.Global, Writes: append([]Resource(nil), s.Writes...), Reads: append([]Resource(nil), s.Reads...)}
}

func containsResource(list []Resource, r Resource) bool {
	for _, item := range list {
		if item == r {
			return true
		}
	}
	return false
}

// Test seams around the effect gate; production leaves them empty.
var (
	beforeEffectGate   = func(context.Context) {}
	beforeEffectPermit = func(context.Context) {}
)

func setupThenAttach(method string) bool {
	return ManagedSessionMethod(method) || method == "chat.stream"
}

// ManagedSessionMethod identifies setup operations that may attach to a holder.
func ManagedSessionMethod(method string) bool {
	switch method {
	case "box.create", "box.copy", "box.start", "box.restart", "box.recreate", "box.attach":
		return true
	default:
		return false
	}
}

type frameWriter struct {
	wire *Wire
	kind string
}

func (w frameWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), 32768)
		if err := w.wire.Write(Frame{Type: w.kind, Data: p[:n]}); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

// Listen creates a socket in an infrastructure-owned directory. Removing only
// an old socket is safe after the controller's singleton lock is acquired.
func Listen(path string, mode os.FileMode) (net.Listener, error) {
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("controller endpoint path is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
