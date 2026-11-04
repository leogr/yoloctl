//go:build linux

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/vmanchor"
)

// Negative actual borrow entry points only. No configured native Context,
// reconstructed source, original source success callback or SDK install.
func TestControllerWorkerSDKSourceRequiresOriginalCandidate(t *testing.T) {
	failure := errors.New("original candidate failed")
	for _, fixture := range []struct {
		name string
		ctx  context.Context
	}{
		{"nil", nil},
		{"missing", t.Context()},
		{"zero", context.WithValue(t.Context(), workerLaunchCandidateKey{}, new(workerLaunchCandidate))},
		{"closed", context.WithValue(t.Context(), workerLaunchCandidateKey{}, &workerLaunchCandidate{closed: true})},
		{"failed", context.WithValue(t.Context(), workerLaunchCandidateKey{}, &workerLaunchCandidate{failure: failure})},
		{"zero-token", context.WithValue(t.Context(), workerLaunchCandidateKey{}, &workerLaunchCandidate{source: new(vmanchor.WorkerHandoff)})},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			called := false
			err := borrowControllerWorkerSDKSource(fixture.ctx, func(*openshell.NativeWorkerSource) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("unavailable original reached SDK callback", err, called)
			}
			if fixture.name == "failed" && !errors.Is(err, failure) {
				t.Fatal("original cause discarded", err)
			}
		})
	}
}
