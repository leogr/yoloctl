package main

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/controller"
)

type earlyWorkerMandatory interface {
	Decode(context.Context) (controller.WorkerInput, string, error)
	TakeCheckpoint() (*os.File, error)
	Close() error
}
type earlyWorkerPhase interface {
	Flush([]byte)
	Close()
}
type workerLaunchObservation interface {
	Check() error
	DuplicatePIDNamespace() (*os.File, error)
	Close() error
}

// Owned opaque OPTIONAL receiver result only; this is not a public/SDK grant.
// No methods expose an admitted namespace or construct Server.PIDAnchor.
type workerLaunchCandidate struct {
	mu                sync.Mutex
	source            workerLaunchObservation
	failure, closeErr error
	closed            bool
}
type workerLaunchCandidateKey struct{}

func captureWorkerLaunchCandidate(consume func() (workerLaunchObservation, bool, error)) *workerLaunchCandidate {
	c := &workerLaunchCandidate{}
	if consume == nil {
		c.failure = errors.New("original worker handoff unavailable")
		return c
	}
	source, claimed, err := consume()
	if !claimed {
		c.failure = errors.Join(err, errors.New("original worker optional pair unclaimed"))
		return c
	} // BOTH optional originals untouched
	if err != nil || source == nil {
		if source != nil {
			err = errors.Join(err, source.Close())
		}
		c.failure = errors.Join(err, errors.New("original worker handoff unavailable"))
		return c
	}
	c.source = source
	return c
}
func (c *workerLaunchCandidate) Close() error {
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

// The actual fixed entry always supplies the concrete production functions
// below. Local fixture substitution exercises ordering/Close ONLY and never
// supplies worker admission. No public callback setter/boolean is installed.
func earlyWorkerContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("early worker context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}

type earlyControllerWorkerOps struct {
	claim  func(context.Context) (earlyWorkerMandatory, bool, error)
	phase  func(controller.WorkerInput) earlyWorkerPhase
	launch func(controller.WorkerInput, string) *workerLaunchCandidate
	load   func(context.Context) error
}
type earlyControllerWorker struct {
	mu          sync.Mutex
	mandatory   earlyWorkerMandatory
	work        controller.WorkerInput
	inputDigest string
	phase       earlyWorkerPhase
	launch      *workerLaunchCandidate
	closed      bool
	closeOnce   sync.Once
	closeErr    error
}

func acquireEarlyControllerWorker(ctx context.Context) (*earlyControllerWorker, error) {
	return acquireEarlyControllerWorkerWith(ctx, earlyControllerWorkerOps{
		claim: func(ctx context.Context) (earlyWorkerMandatory, bool, error) {
			p, claimed, err := controller.ClaimWorkerPrivateIntake(ctx)
			if p == nil {
				return nil, claimed, err
			}
			return p, claimed, err
		},
		phase: func(work controller.WorkerInput) earlyWorkerPhase {
			p := controller.OpenNativePhaseWriter(work)
			if p == nil {
				return nil
			}
			return p
		},
		launch: consumeEarlyWorkerLaunch,
		load:   controller.LoadRuntime,
	})
}
func acquireEarlyControllerWorkerWith(ctx context.Context, ops earlyControllerWorkerOps) (_ *earlyControllerWorker, result error) {
	if ctx == nil || ops.claim == nil || ops.phase == nil || ops.launch == nil || ops.load == nil {
		return nil, errors.New("early worker intake unavailable")
	}
	e := &earlyControllerWorker{}
	defer func() {
		if result != nil {
			result = errors.Join(result, e.Close())
		}
	}()
	mandatory, claimed, err := ops.claim(ctx)
	if !claimed {
		return nil, errors.Join(err, errors.New("mandatory original worker pair unclaimed"))
	} // never Close inconsistent unclaimed object
	e.mandatory = mandatory
	if err != nil || mandatory == nil {
		return nil, errors.Join(err, errors.New("mandatory original worker pair unavailable"))
	}
	e.work, e.inputDigest, err = mandatory.Decode(ctx)
	if err != nil {
		return nil, err
	}
	if err := earlyWorkerContext(ctx); err != nil {
		return nil, err
	}
	// THESE exact calls precede the actual LoadRuntime child. Phase FIRST;
	// no standalone namespace claim can alter one half before whole-pair intake.
	e.phase = ops.phase(e.work)
	if err := earlyWorkerContext(ctx); err != nil {
		return nil, err
	}
	e.launch = ops.launch(e.work, e.inputDigest)
	if e.launch == nil {
		e.launch = captureWorkerLaunchCandidate(nil)
	}
	// Optional failure is retained as unavailable proof, not a product failure.
	if err := earlyWorkerContext(ctx); err != nil {
		return nil, err
	}
	if err := ops.load(ctx); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *earlyControllerWorker) TakeCheckpoint() (*os.File, error) {
	if e == nil {
		return nil, errors.New("early worker ownership unavailable")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.mandatory == nil {
		return nil, errors.New("early worker ownership expired")
	}
	return e.mandatory.TakeCheckpoint()
}
func (e *earlyControllerWorker) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		launch, phase, mandatory := e.launch, e.phase, e.mandatory
		e.mu.Unlock()
		var err error
		if launch != nil {
			err = errors.Join(err, launch.Close())
		}
		if phase != nil {
			phase.Close()
		}
		if mandatory != nil {
			err = errors.Join(err, mandatory.Close())
		}
		e.mu.Lock()
		e.closeErr = err
		e.mu.Unlock()
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeErr
}
