//go:build linux

package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/vmanchor"
)

// UNWIRED accessor for the future exact SDK call AFTER ordinary catalog/context
// setup (executeControllerWorker AFTER UseCatalogSnapshot). It does not install
// a context value, set Server.PIDAnchor, call16 or create a source on fallback.
// The retained original early candidate alone owns token Close.
func borrowControllerWorkerSDKSource(ctx context.Context, use func(*openshell.NativeWorkerSource) error) error {
	if ctx == nil || use == nil {
		return errors.New("original SDK source borrow unavailable")
	}
	candidate, ok := ctx.Value(workerLaunchCandidateKey{}).(*workerLaunchCandidate)
	if !ok || candidate == nil {
		return errors.New("original worker candidate unavailable")
	}
	candidate.mu.Lock()
	if candidate.closed || candidate.failure != nil {
		err := errors.Join(candidate.failure, errors.New("original worker candidate expired"))
		candidate.mu.Unlock()
		return err
	}
	original, ok := candidate.source.(*vmanchor.WorkerHandoff)
	candidate.mu.Unlock()
	if !ok || original == nil {
		return errors.New("genuine original worker handoff unavailable")
	}
	useErr := openshell.WithNativeWorkerSource(ctx, original, use)
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.closed || candidate.source != original || candidate.failure != nil {
		return errors.Join(useErr, candidate.failure, errors.New("SAME original worker candidate expired during SDK borrow"))
	}
	return useErr
}
