//go:build linux

package controller

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/leogr/yoloctl/internal/vmanchor"
)

func draftWaitingWorkerOwner(t *testing.T) (*Server, *Receipt, *nativeNonceOwner) {
	t.Helper()
	s := &Server{Hello: hello(), DiskID: testDisk, Receipts: Receipts{t.TempDir()}, VerifyStorage: func(context.Context) error { return errors.New("bootstrap must not call effect/storage authorization") }}
	req := request("test.worker-association")
	r, err := s.Receipts.begin(req, Principal{}, s.Hello.Generation, testDisk, true)
	if err != nil {
		t.Fatal(err)
	}
	o := s.newNativeNonceOwner(t.Context(), Principal{}, req, &r, Operation{Mutates: true, EffectGated: true})
	t.Cleanup(o.close)
	return s, &r, o
}
func TestWorkerBootstrapObservesPendingDurableReceiptWithoutBeginGrant(t *testing.T) {
	s, r, o := draftWaitingWorkerOwner(t)
	s.mu.Lock()
	observation := o.checkBootstrapReceipt(t.Context())
	strict := o.checkReceipt(t.Context(), "box")
	s.mu.Unlock()
	if observation != nil || strict == nil || r.EffectPhase != EffectWaiting || r.Scope != nil {
		t.Fatal("bootstrap became effect permission", observation, strict)
	}
	// Actual effect transition is separate. Frozen association must survive that
	// transition; the bootstrap reader itself cannot be reused after it.
	r.EffectPhase = EffectStarted
	r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}}
	if err := s.Receipts.Save(*r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	observation = o.checkBootstrapReceipt(t.Context())
	strict = o.checkReceipt(t.Context(), "box")
	s.mu.Unlock()
	if observation == nil || strict != nil {
		t.Fatal("bootstrap/strict policy phases conflated", observation, strict)
	}
}
func TestWorkerBootstrapRequiresExactOriginalDurableBytes(t *testing.T) {
	s, r, o := draftWaitingWorkerOwner(t)
	r.Principal = Principal{Box: "copied", Incarnation: "actor"} // not owner identity
	if err := s.Receipts.Save(*r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err := o.checkBootstrapReceipt(t.Context())
	s.mu.Unlock()
	if err == nil {
		t.Fatal("foreign receipt adopted")
	}
}
func TestWorkerBootstrapRejectsUnsavedReceiptMutation(t *testing.T) {
	s, r, o := draftWaitingWorkerOwner(t)
	r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}} // no Save
	s.mu.Lock()
	err := o.checkBootstrapReceipt(t.Context())
	s.mu.Unlock()
	if err == nil {
		t.Fatal("unsaved original metadata accepted")
	}
}

// Negative-only counterfeit source: no NativeCtx, fake pins or positive transfer.
type draftCounterfeitWorkerSource struct{ calls int }

func (*draftCounterfeitWorkerSource) Check() error {
	return errors.New("not an actual retained source")
}
func (*draftCounterfeitWorkerSource) DuplicatePIDNamespace() (*os.File, error) {
	return nil, errors.New("unavailable")
}
func (f *draftCounterfeitWorkerSource) WithWorkerLaunchSource(ctx context.Context, fn func(WorkerLaunchSource, WorkerControllerAssociation) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.calls++
	return fn(&vmanchor.LaunchAnchor{}, WorkerControllerAssociation{})
}
func TestWorkerCounterfeitMetadataCannotMintHandoffAndFailureSticks(t *testing.T) {
	_, _, o := draftWaitingWorkerOwner(t)
	source := &draftCounterfeitWorkerSource{}
	o.pidAnchor = source
	h := newNativeWorkerHandoffOwner(o)
	if h == nil {
		t.Fatal("negative borrowed interface not recognized")
	}
	defer func() { _ = h.Close() }()
	work := WorkerInput{Request: request("test.worker-association"), PIDNamespaceFD: 5, PIDNamespaceBootstrapFD: 6}
	if p, err := h.Prepare(work, workerHandoffFiles{}); err == nil || p != nil {
		t.Fatal("counterfeit/zero source minted pair")
	}
	first := source.calls
	if p, err := h.Prepare(work, workerHandoffFiles{}); err == nil || p != nil || source.calls != first {
		t.Fatal("failed original preparation repaired/retried")
	}
	if err := h.CheckPIDAnchor(t.Context(), "box"); err == nil {
		t.Fatal("counterfeit source granted checkpoint")
	}
}
func TestWorkerOwnerCloseExcludesOriginalBorrowLifetime(t *testing.T) {
	_, _, o := draftWaitingWorkerOwner(t)
	o.handoffLifetime.RLock()
	locked := true
	defer func() {
		if locked {
			o.handoffLifetime.RUnlock()
		}
	}()
	started, finished := make(chan struct{}), make(chan struct{})
	go func() { close(started); o.close(); close(finished) }()
	<-started
	// close cannot expire the owner while an original source borrow is live.
	o.mu.Lock()
	active := o.active
	o.mu.Unlock()
	if !active {
		t.Fatal("owner expired through active borrow lock")
	}
	o.handoffLifetime.RUnlock()
	locked = false
	<-finished
	if err := o.live(t.Context(), "", ""); err == nil {
		t.Fatal("closed original owner revived")
	}
}

// Controller receipt/scope scheduling only. Fixture source checks do NOT supply
// genuine VM provenance or a positive admitted controller-to-worker handoff.
func TestWorkerHandoffFinalProviderCallbackCannotEraseOriginalBoxScope(t *testing.T) {
	for _, change := range []string{"scope", "receipt", "expiry", "receipt-health"} {
		t.Run(change, func(t *testing.T) {
			s, r, o := draftWaitingWorkerOwner(t)
			s.VerifyStorage = func(context.Context) error { return nil }
			o.pidAnchor = fixtureWorkerPIDAnchor{check: func() error { return nil }}
			r.EffectPhase = EffectStarted
			r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}}
			if err := s.Receipts.Save(*r); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := o.checkWorkerHandoffCheckpoint(t.Context(), "box", func() error {
				calls++
				if calls != 2 {
					return nil
				}
				switch change {
				case "scope":
					r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "foreign"}}}
					return s.Receipts.Save(*r)
				case "receipt":
					r.Principal = Principal{Box: "foreign", Incarnation: "changed"}
					return s.Receipts.Save(*r)
				case "receipt-health":
					s.mu.Lock()
					s.record(*r, errors.New("disjoint receipt fsync failure"), false, false, "")
					s.mu.Unlock()
				case "expiry":
					o.close()
				}
				return nil
			})
			if err == nil || calls != 2 {
				t.Fatal("late provider invalidation became success", calls, err)
			}
		})
	}
}
