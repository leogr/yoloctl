package main

import (
	"context"
	"testing"

	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/vmanchor"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

// Transport/ownership negatives only. Equal fixture metadata or a zero token
// supplies no original controller/worker/SDK authority.
func TestCheckpointBindingUnavailableProofLeavesOrdinaryChannelOwned(t *testing.T) {
	for _, kind := range []string{"absent", "foreign-interface", "zero-handoff", "closed-candidate"} {
		t.Run(kind, func(t *testing.T) {
			parent, child, err := termio.ViewPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			physical := workercheckpoint.Take(child)
			defer func() { _ = physical.Close() }()
			var candidate *workerLaunchCandidate
			var order []string
			switch kind {
			case "foreign-interface":
				candidate = &workerLaunchCandidate{source: &workerLaunchOrderFixture{order: &order}}
			case "zero-handoff":
				candidate = &workerLaunchCandidate{source: new(vmanchor.WorkerHandoff)}
			case "closed-candidate":
				candidate = &workerLaunchCandidate{closed: true}
			}
			bindControllerCheckpoint(context.Background(), candidate, physical)
			if _, err := physical.Observe(t.Context()); err != nil {
				t.Fatal("optional source refusal closed original protocol owner", err)
			}
			if kind == "zero-handoff" && candidate.failure == nil {
				t.Fatal("zero handoff became a source binding")
			}
			if len(order) != 0 {
				t.Fatal("binder called/closed unrelated interface source", order)
			}
		})
	}
}
