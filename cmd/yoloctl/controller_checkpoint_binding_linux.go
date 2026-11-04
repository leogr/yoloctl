//go:build linux

package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/vmanchor"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

// Only the retained concrete original handoff can bind. Fixture interfaces or
// nil-success callbacks cannot mint source. Candidate/early still owns Close.
func bindControllerCheckpoint(ctx context.Context, candidate *workerLaunchCandidate, c *workercheckpoint.Channel) {
	if candidate == nil {
		return
	}
	candidate.mu.Lock()
	if candidate.closed || candidate.failure != nil {
		candidate.mu.Unlock()
		return
	}
	original, ok := candidate.source.(*vmanchor.WorkerHandoff)
	candidate.mu.Unlock()
	if !ok || original == nil {
		return
	}
	err := original.BindCheckpoint(ctx, c)
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.closed || candidate.source != original {
		err = errors.Join(err, errors.New("original worker candidate expired during checkpoint binding"))
	}
	if err != nil {
		candidate.failure = errors.Join(candidate.failure, err)
	}
	// Failure is retained as unavailable proof, not an ordinary result override.
	// No Server.PIDAnchor or SDK source is installed by this hook.
}
