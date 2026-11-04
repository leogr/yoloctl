//go:build linux

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmanchor"
)

// These narrow capability interfaces are Linux-only. The portable metadata DTO
// carries correlation, never a new source constructor or effect permission.
// The CLI's actual private bridge must hold its original candidate/controller
// lease through EVERY callback; this draft supplies no provider or installer.
type WorkerLaunchSource interface {
	WorkerPIDAnchor
	PrepareWorkerHandoff(vmanchor.WorkerHandoffRequest, vmanchor.WorkerHandoffFiles) (*vmanchor.WorkerHandoffTransfer, error)
}
type WorkerSourceBorrower interface {
	WorkerPIDAnchor
	WithWorkerLaunchSource(context.Context, func(WorkerLaunchSource, WorkerControllerAssociation) error) error
}

// Existing real sources/borrowers are pointer capabilities. Compare only safely
// comparable dynamic values; do not admit a reconstructed metadata identity.
func sameWorkerCapability(a, b any) bool {
	kind := reflect.TypeOf(a)
	if kind == nil || kind != reflect.TypeOf(b) || !kind.Comparable() {
		return false
	}
	value := reflect.ValueOf(a)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return false
	}
	return a == b
}
func newNativeWorkerHandoffOwner(o *nativeNonceOwner) workerHandoffOwner {
	if o == nil {
		return nil
	}
	borrower, ok := o.pidAnchor.(WorkerSourceBorrower)
	if !ok || !sameWorkerCapability(borrower, borrower) {
		return nil
	}
	return &nativeWorkerHandoffOwner{owner: o, borrower: borrower}
}

type nativeWorkerHandoffOwner struct {
	mu                    sync.Mutex
	owner                 *nativeNonceOwner
	borrower              WorkerSourceBorrower
	source                WorkerLaunchSource // latched SAME original borrowed capability; never Close source
	attempted, closed     bool
	failed                error
	closeErr              error
	associationDigest     string
	associationController []byte // controller projection ONLY, not mutable receipt scope
	transfer              *vmanchor.WorkerHandoffTransfer
	prepared              *nativePreparedWorkerHandoff
}
type nativePreparedWorkerHandoff struct {
	owner    *nativeWorkerHandoffOwner
	work     WorkerInput
	input    []byte // original PRIVATE payload; never log/store in receipts
	transfer *vmanchor.WorkerHandoffTransfer
}

func (p *nativePreparedWorkerHandoff) Input() WorkerInput     { return p.work }
func (p *nativePreparedWorkerHandoff) CanonicalInput() []byte { return bytes.Clone(p.input) }
func (p *nativePreparedWorkerHandoff) ChildFiles() []*os.File { return p.transfer.ChildFiles() }
func (p *nativePreparedWorkerHandoff) PublishStarted(child *os.Process) error {
	return p.owner.publishStarted(p, child)
}
func (p *nativePreparedWorkerHandoff) Close() error { return p.owner.Close() }

// checkBootstrapReceipt is observation ONLY. It does not call checkReceipt,
// BeginEffects, allocate, loadHolds, Save or record an effect/recovery outcome.
// Caller holds s.mu. No s/owner lock spans source/kernel publication.
func (o *nativeNonceOwner) checkBootstrapReceipt(ctx context.Context) error {
	if o == nil || o.server == nil || o.ctx == nil || ctx == nil {
		return errors.New("original bootstrap owner unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	s, r := o.server, o.receipt
	if s.Hello != o.hello || s.DiskID == "" || s.DiskID != o.diskID || r == nil || r.ID != o.requestID || r.Method != o.method || r.Principal != o.principal || r.Generation != o.hello.Generation || r.DiskID != o.diskID || r.State != "pending" || r.EffectPhase != EffectWaiting || r.SetupComplete || r.Completion != nil || r.ProofVersion < documentProofVersion || r.ProofVersion > ProofVersion {
		return errors.New("original waiting bootstrap receipt changed")
	}
	if err := r.Scope.validate(); err != nil {
		return err
	}
	if s.receiptFailure != nil {
		return errors.New("bootstrap receipt storage is unavailable")
	}
	expected, err := json.Marshal(*r)
	if err != nil {
		return err
	}
	raw, err := safepath.ReadFile(s.Receipts.Dir, r.ID+".json", MaxDocument)
	if err != nil || !bytes.Equal(raw, expected) {
		return errors.Join(errors.New("original durable bootstrap receipt changed/unavailable"), err)
	}
	return ctx.Err()
}
func (h *nativeWorkerHandoffOwner) live() error {
	if h == nil || h.closed || h.owner == nil || h.borrower == nil {
		return errors.New("original worker handoff owner unavailable/closed")
	}
	if h.failed != nil {
		return h.failed
	}
	return h.owner.live(h.owner.ctx, "", "")
}

// Caller holds original owner.handoffLifetime RLock and h.mu. That new owner
// lifetime lock must also be held EXCLUSIVELY by nativeNonceOwner.close, before
// its ordinary owner.mu. It excludes expiry during the actual source borrow.
func (h *nativeWorkerHandoffOwner) borrow(ctx context.Context, run func(WorkerControllerAssociation) error) error {
	if ctx == nil {
		return errors.New("worker borrow context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.live(); err != nil {
		return err
	}
	if !sameWorkerCapability(h.owner.pidAnchor, h.borrower) {
		return errors.New("original handoff borrower replaced")
	}
	calls := 0
	var callbackErr error
	providerErr := h.borrower.WithWorkerLaunchSource(ctx, func(actual WorkerLaunchSource, association WorkerControllerAssociation) error {
		calls++
		if calls != 1 || !sameWorkerCapability(actual, actual) {
			callbackErr = errors.New("original source capability identity unavailable")
			return callbackErr
		}
		if h.source == nil {
			h.source = actual
		}
		if !sameWorkerCapability(actual, h.source) {
			callbackErr = errors.New("closed provider did not borrow SAME original source")
			return callbackErr
		}
		if err := actual.Check(); err != nil {
			callbackErr = err
			return err
		}
		controller, err := json.Marshal(association)
		if err != nil {
			callbackErr = err
			return err
		}
		if len(h.associationController) != 0 && !bytes.Equal(controller, h.associationController) {
			callbackErr = errors.New("original controller/singleton/source association changed")
			return callbackErr
		}
		if err := run(association); err != nil {
			callbackErr = err
			return err
		}
		if err := actual.Check(); err != nil {
			callbackErr = err
			return err
		}
		if err := h.live(); err != nil {
			callbackErr = err
			return err
		}
		return nil
	})
	if calls != 1 {
		return errors.Join(providerErr, callbackErr, errors.New("closed source borrow missing/repeated"))
	}
	return errors.Join(providerErr, callbackErr, ctx.Err())
}
func (h *nativeWorkerHandoffOwner) Prepare(work WorkerInput, files workerHandoffFiles) (_ workerPreparedHandoff, err error) {
	if h == nil || h.owner == nil {
		return nil, errors.New("worker handoff owner missing")
	}
	h.owner.handoffLifetime.RLock()
	defer h.owner.handoffLifetime.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attempted || h.closed || h.failed != nil {
		return nil, errors.Join(h.failed, errors.New("worker handoff already attempted/unavailable"))
	}
	h.attempted = true
	defer func() {
		if err != nil {
			h.failed = err
			if h.transfer != nil {
				err = errors.Join(err, h.transfer.Close())
				h.transfer = nil
			}
		}
	}()
	if work.Request.ID != h.owner.requestID || work.Request.Method != h.owner.method || work.Principal != h.owner.principal || work.PIDNamespaceFD == 0 || work.PIDNamespaceBootstrapFD != work.PIDNamespaceFD+1 || work.WorkerAssociationDigest != "" {
		return nil, errors.New("worker input differs from original execution/planned optional slots")
	}
	err = h.borrow(h.owner.ctx, func(association WorkerControllerAssociation) error {
		s := h.owner.server
		s.mu.Lock()
		checkErr := h.owner.checkBootstrapReceipt(h.owner.ctx)
		var receipt Receipt
		if checkErr == nil {
			receipt = *h.owner.receipt
			receipt.Scope = cloneScope(receipt.Scope)
		}
		s.mu.Unlock()
		if checkErr != nil {
			return checkErr
		}
		if association.Hello != h.owner.hello || association.DiskID != h.owner.diskID {
			return errors.New("closed provider differs from original execution controller/disk")
		}
		digest, _, err := projectWorkerAssociationV1(association, receipt)
		if err != nil {
			return err
		}
		h.associationDigest = digest
		h.associationController, err = json.Marshal(association)
		if err != nil {
			return err
		}
		work.WorkerAssociationDigest = digest
		raw, inputDigest, err := CanonicalWorkerInput(work)
		if err != nil {
			return err
		}
		viewFD := 0
		if work.Request.View {
			viewFD = 5
		}
		request := vmanchor.WorkerHandoffRequest{ExecutionID: work.Request.ID, Method: work.Request.Method, AssociationDigest: digest, InputDigest: inputDigest, ViewFD: viewFD, PhaseFD: work.NativePhaseFD, NamespaceFD: work.PIDNamespaceFD, BootstrapFD: work.PIDNamespaceBootstrapFD}
		transfer, err := h.source.PrepareWorkerHandoff(request, vmanchor.WorkerHandoffFiles{Payload: files.Payload, Checkpoint: files.Checkpoint, View: files.View, Phase: files.Phase})
		if err != nil {
			return err
		}
		if transfer == nil {
			return errors.New("original source returned no transfer")
		}
		h.transfer = transfer
		children := transfer.ChildFiles()
		if len(children) != 2 || children[0] == nil || children[1] == nil {
			return errors.New("worker handoff returned incomplete owned pair")
		}
		h.prepared = &nativePreparedWorkerHandoff{owner: h, work: work, input: raw, transfer: transfer}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h.prepared, nil
}
func (h *nativeWorkerHandoffOwner) publishStarted(prepared *nativePreparedWorkerHandoff, child *os.Process) (err error) {
	if h == nil || h.owner == nil {
		return errors.New("original worker handoff owner missing")
	}
	h.owner.handoffLifetime.RLock()
	defer h.owner.handoffLifetime.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if prepared == nil || prepared != h.prepared || prepared.transfer != h.transfer || h.transfer == nil {
		h.failed = errors.New("started worker differs from original prepared transfer")
		return h.failed
	}
	err = h.borrow(h.owner.ctx, func(WorkerControllerAssociation) error {
		s := h.owner.server
		s.mu.Lock()
		checkErr := h.owner.checkBootstrapReceipt(h.owner.ctx)
		s.mu.Unlock()
		if checkErr != nil {
			return checkErr
		}
		// ONE real started child; this optional call owns no Start/Wait/status.
		return h.transfer.PublishStarted(child)
	})
	if err != nil {
		h.failed = err
	}
	return err
}
func (h *nativeWorkerHandoffOwner) CheckPIDAnchor(ctx context.Context, box string) (err error) {
	if h == nil || h.owner == nil {
		return errors.New("original worker transfer checker unavailable")
	}
	h.owner.handoffLifetime.RLock()
	defer h.owner.handoffLifetime.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.transfer == nil || h.prepared == nil {
		h.failed = errors.New("actual original worker transfer unavailable")
		return h.failed
	}
	checkTransfer := func() error {
		return h.borrow(ctx, func(WorkerControllerAssociation) error { return h.transfer.Check() })
	}
	err = h.owner.checkWorkerHandoffCheckpoint(ctx, box, checkTransfer)
	if err != nil {
		h.failed = err
	}
	return err
}

// This private composition uses the actual original receipt owner. Only the
// genuine transfer owner supplies checkTransfer in production; no SDK/public
// callback can construct a receipt owner or turn these fixtures into admission.
func (o *nativeNonceOwner) checkWorkerHandoffCheckpoint(ctx context.Context, box string, checkTransfer func() error) error {
	if checkTransfer == nil {
		return errors.New("original transfer check unavailable")
	}
	if err := checkTransfer(); err != nil {
		return err
	}
	if err := o.checkPIDAnchor(ctx, box); err != nil {
		return err
	}
	if err := checkTransfer(); err != nil {
		return err
	}
	// No source/provider callback follows final original receipt/scope/hold checks.
	s := o.server
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.checkReceipt(ctx, box); err != nil {
		return err
	}
	return o.live(ctx, box, "")
}

func (h *nativeWorkerHandoffOwner) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return h.closeErr
	}
	h.closed = true
	if h.prepared != nil {
		clear(h.prepared.input)
		h.prepared.input = nil
	}
	if h.transfer == nil {
		return nil
	}
	h.closeErr = h.transfer.Close() // ONLY transfer-owned resources, never original source
	return h.closeErr
}
