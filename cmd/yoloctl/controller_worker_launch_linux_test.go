package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/vmanchor"
)

func TestEarlyWorkerWholePairUsesFinalPhaseFirstCanonicalInput(t *testing.T) {
	for _, view := range []bool{false, true} {
		for _, phase := range []bool{false, true} {
			for _, pair := range []bool{false, true} {
				t.Run(fmt.Sprintf("view=%v/phase=%v/pair=%v", view, phase, pair), func(t *testing.T) {
					work := controller.WorkerInput{Request: controller.Request{ID: "11111111-2222-4333-8444-555555555555", Method: "box.show", Space: "fixture", View: view}, WorkerAssociationDigest: strings.Repeat("a", 64)}
					next := 5
					if view {
						next++
					}
					if phase {
						work.NativePhaseFD = next
						work.NativePhasePipe = &controller.NativePhasePipeID{Device: 1, Inode: 2}
						next++
					}
					if pair {
						work.PIDNamespaceFD = next
						work.PIDNamespaceBootstrapFD = next + 1
					}
					original, digest, err := controller.CanonicalWorkerInput(work)
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					candidate := consumeEarlyWorkerLaunchWith(work, digest, func(req vmanchor.WorkerHandoffRequest, raw []byte) (workerLaunchObservation, bool, error) {
						calls++
						wantView := 0
						if view {
							wantView = 5
						}
						if !bytes.Equal(raw, original) || req.ExecutionID != work.Request.ID || req.Method != work.Request.Method || req.AssociationDigest != work.WorkerAssociationDigest || req.InputDigest != digest || req.ViewFD != wantView || req.PhaseFD != work.NativePhaseFD || req.NamespaceFD != work.PIDNamespaceFD || req.BootstrapFD != work.PIDNamespaceBootstrapFD {
							t.Fatal("closed request/input changed", req)
						}
						return nil, false, errors.New("fixture has no original source")
					})
					if calls != 1 || candidate.source != nil || candidate.failure == nil {
						t.Fatal("optional projection supplied admission", calls)
					}
					_ = candidate.Close()
				})
			}
		}
	}
}
func TestEarlyWorkerChangedInputNeverCallsOptionalReceiver(t *testing.T) {
	work := controller.WorkerInput{Request: controller.Request{Method: "box.show"}}
	_, digest, err := controller.CanonicalWorkerInput(work)
	if err != nil {
		t.Fatal(err)
	}
	work.PIDNamespaceFD = 7
	calls := 0
	candidate := consumeEarlyWorkerLaunchWith(work, digest, func(vmanchor.WorkerHandoffRequest, []byte) (workerLaunchObservation, bool, error) {
		calls++
		return nil, false, nil
	})
	defer func() { _ = candidate.Close() }()
	if calls != 0 || candidate.failure == nil {
		t.Fatal("later private input repaired original digest", calls)
	}
}
func TestEarlyWorkerActualUnknownPairLeavesOriginalFileUntouched(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	work := controller.WorkerInput{Request: controller.Request{ID: "fixture", Method: "box.show"}, PIDNamespaceFD: int(file.Fd()), PIDNamespaceBootstrapFD: int(file.Fd()) + 1, WorkerAssociationDigest: strings.Repeat("a", 64)}
	_, digest, err := controller.CanonicalWorkerInput(work)
	if err != nil {
		t.Fatal(err)
	}
	candidate := consumeEarlyWorkerLaunch(work, digest)
	defer func() { _ = candidate.Close() }()
	if candidate.source != nil || candidate.failure == nil {
		t.Fatal("unknown pair became an observation")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("raw optional refusal closed unrelated original", err)
	}
}
