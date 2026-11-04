//go:build linux

package openshell

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

// All fixtures below are INCOMPLETE/metadata-only and cannot pass the genuine
// token/native constructor. They NEVER configure Current/fake NativeCtx, create
// a positive original source, supply kernel ops or execute a parent success hook.
func attachmentSelectionFixture() (*NativeWorkerSource, *spaceenv.Context) {
	metadata := &spaceenv.Context{Space: "metadata-only", Mounts: []spaceenv.Mount{{Source: "/original", Writable: false}}}
	s := &NativeWorkerSource{contextOriginal: metadata, contextSnapshot: cloneWorkerNativeContext(metadata), catalogSnapshot: "/metadata-only-original"}
	return s, metadata // active=false, token=nil, lifetime=nil: NO usable source
}
func TestObservedOriginalCatalogFailureCannotBeRestoredIntoSameAttachment(t *testing.T) {
	s, metadata := attachmentSelectionFixture()
	first := s.observeAttachmentSelection(metadata, "/metadata-only-moved")
	if first == nil {
		t.Fatal("catalog drift unobserved")
	}
	restored := s.observeAttachmentSelection(metadata, s.catalogSnapshot)
	if restored == nil || !errors.Is(restored, first) {
		t.Fatal("restored catalog repaired observed failure", restored)
	}
	// Even a callback that ignores the first failure cannot make the final actual
	// attachment check return nil by restoring original selection.
	if final := s.checkAttachment(t.Context()); !errors.Is(final, first) {
		t.Fatal("outer final attachment check lost observed failure", final)
	}
	called := false
	if err := s.WithProcessObservation(t.Context(), "box", func(*NativeWorkerProcessRead) error { called = true; return nil }); err == nil || called {
		t.Fatal("failed attachment reached process callback", err, called)
	}
}
func TestObservedOriginalContextFailureCannotBeRestoredIntoSameAttachment(t *testing.T) {
	for _, kind := range []string{"pointer", "field", "mount"} {
		t.Run(kind, func(t *testing.T) {
			s, metadata := attachmentSelectionFixture()
			current := metadata
			switch kind {
			case "pointer":
				copy := cloneWorkerNativeContext(metadata)
				current = &copy
			case "field":
				metadata.Space = "metadata-only-changed"
			case "mount":
				metadata.Mounts[0].Writable = true
			}
			first := s.observeAttachmentSelection(current, s.catalogSnapshot)
			if first == nil {
				t.Fatal("original drift unobserved")
			}
			*metadata = cloneWorkerNativeContext(&s.contextSnapshot)
			later := s.observeAttachmentSelection(metadata, s.catalogSnapshot)
			if later == nil || !errors.Is(later, first) {
				t.Fatal("restored Context repaired attachment", later)
			}
		})
	}
}
func TestOriginalAttachmentFailureRetainsFirstAndJoinedTypedCause(t *testing.T) {
	var state nativeWorkerAttachmentFailure
	first := errors.New("ORIGINAL context drift observed")
	if got := state.observe(first); !errors.Is(got, first) {
		t.Fatal(got)
	}
	parent := &workercheckpoint.ParentRefusal{Message: "original storage/receipt uncertain", Uncertain: true}
	_ = state.observe(parent)
	joined := workerSourceOutcome(state.load())
	var uncertain *NativeEffectsUncertain
	if !errors.Is(joined, first) || !errors.Is(joined, workercheckpoint.ErrUncertain) || !errors.As(joined, &uncertain) {
		t.Fatal("first/typed original cause discarded", joined)
	}
	if later := state.observe(nil); !errors.Is(later, first) || !errors.Is(later, workercheckpoint.ErrUncertain) {
		t.Fatal("restored observation erased failure", later)
	}
}
func TestCallerCancellationOnlyDoesNotLatchOriginalAttachmentFailure(t *testing.T) {
	s := new(NativeWorkerSource) // only the PRIVATE error-combination unit, no source
	cause := errors.New("local read canceled")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	err := s.callerAttachmentResult(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatal("caller cancellation lost", err)
	}
	if s.attachmentFailure.load() != nil {
		t.Fatal("caller-only cancellation poisoned original attachment")
	}
	if err := s.callerAttachmentResult(t.Context()); err != nil {
		t.Fatal("healthy local error-state was latched", err)
	}
	// A preexisting original failure is still reported alongside cancellation.
	originalLoss := errors.New("original token lost")
	_ = s.recordOriginalAttachmentFailure(originalLoss)
	if err := s.callerAttachmentResult(ctx); !errors.Is(err, originalLoss) || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatal("cancellation erased actual original loss", err)
	}
}
func TestOriginalAttachmentFailureConcurrentFirstAndTypedCause(t *testing.T) {
	var state nativeWorkerAttachmentFailure
	first := errors.New("first original loss")
	_ = state.observe(first)
	parent := &workercheckpoint.ParentRefusal{Message: "known original outcome uncertain", Uncertain: true}
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			if i%2 == 0 {
				_ = state.observe(parent)
			} else {
				_ = state.observe(errors.New("secondary original failure"))
			}
			_ = state.load()
		}(i)
	}
	workers.Wait()
	err := state.load()
	if !errors.Is(err, first) || !errors.Is(err, workercheckpoint.ErrUncertain) {
		t.Fatal("synchronized original loss/typed cause lost", err)
	}
}
