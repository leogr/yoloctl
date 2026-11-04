//go:build linux

package vmanchor

import (
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

// Deliberately FAILED/zero-source negative. This never constructs a positive
// source. It verifies monotonic error information after original pin loss.
func TestCheckpointFailureRetainsKnownUncertaintyAcrossSourceLoss(t *testing.T) {
	pinLoss := errors.New("original source pin lost")
	w := &WorkerHandoff{failed: pinLoss}
	parent := &workercheckpoint.ParentRefusal{Message: "receipt storage outcome unknown", Uncertain: true}
	first := w.failCheckpoint(parent)
	if !errors.Is(first, workercheckpoint.ErrUncertain) || !errors.Is(first, pinLoss) {
		t.Fatal("first source-loss return discarded parent uncertainty", first)
	}
	later := w.Check()
	if !errors.Is(later, workercheckpoint.ErrUncertain) || !errors.Is(later, pinLoss) {
		t.Fatal("sticky source failure erased known uncertainty", later)
	}
}
