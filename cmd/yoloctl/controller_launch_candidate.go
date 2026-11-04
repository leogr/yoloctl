package main

import (
	"errors"
	"sync"

	"github.com/leogr/yoloctl/internal/controller"
)

// A retained original-launch observation, deliberately NOT Server.PIDAnchor.
// Composed native storage/receipt/worker/SDK admission remains a separate gate.
type controllerLaunchObservation interface {
	controller.WorkerPIDAnchor
	Close() error
}
type controllerLaunchCandidate struct {
	mu       sync.Mutex
	source   controllerLaunchObservation
	failure  error
	closed   bool
	closeErr error
}
type controllerLaunchCandidateKey struct{}

func captureControllerLaunchCandidate(consume func() (controllerLaunchObservation, bool, error)) *controllerLaunchCandidate {
	c := &controllerLaunchCandidate{}
	source, claimed, err := consume()
	if !claimed {
		// No wrapping/Close on a false ownership result, even on error. An
		// inconsistent source result is unavailable, never adopted as authority.
		c.failure = errors.Join(err, errors.New("original launch transfer not claimed"))
		return c
	}
	if err != nil || source == nil {
		if source != nil {
			err = errors.Join(err, source.Close())
		}
		c.failure = errors.Join(err, errors.New("original launch transfer unavailable"))
		return c
	}
	c.source = source
	return c
}

func (c *controllerLaunchCandidate) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	if c.source != nil {
		c.closeErr = c.source.Close()
		c.source = nil
	}
	return c.closeErr
}
