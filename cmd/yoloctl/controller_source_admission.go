package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const controllerSourceAdmissionWait = 10 * time.Second // QUEUE admission ONLY

func controllerSourceContextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("original source caller context missing")
	}
	return errors.Join(ctx.Err(), context.Cause(ctx))
}

// One original borrow at a time. Close stops NEW admission immediately, then
// settles the actual active borrower before touching OWN observation resources.
// No canceled waiter releases, closes or otherwise touches another borrow.
type controllerSourceAdmission struct {
	once      sync.Once
	token     chan struct{}
	stopping  chan struct{}
	settled   chan struct{}
	closeOnce sync.Once
	closeErr  error // published before settled closes
}

func (g *controllerSourceAdmission) init() {
	g.once.Do(func() {
		g.token = make(chan struct{}, 1)
		g.token <- struct{}{}
		g.stopping = make(chan struct{})
		g.settled = make(chan struct{})
	})
}
func (g *controllerSourceAdmission) waitError(ctx, lifetime context.Context) error {
	if err := controllerSourceContextError(ctx); err != nil {
		return err
	}
	if err := controllerSourceContextError(lifetime); err != nil {
		return err
	}
	select {
	case <-g.stopping:
		return errors.New("original source admission closed")
	default:
		return nil
	}
}
func (g *controllerSourceAdmission) acquire(ctx, lifetime context.Context) (func(), error) {
	g.init()
	if err := g.waitError(ctx, lifetime); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, controllerSourceContextError(ctx)
	case <-lifetime.Done():
		return nil, controllerSourceContextError(lifetime)
	case <-g.stopping:
		return nil, errors.New("original source admission closed")
	case <-g.token:
	}
	// Select may choose a ready token alongside canceled/closed state. Put back
	// ONLY the token THIS call actually acquired, before returning unavailable.
	if err := g.waitError(ctx, lifetime); err != nil {
		g.token <- struct{}{}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { g.token <- struct{}{} }) }, nil
}
func (g *controllerSourceAdmission) closeOwned(closeOwned func() error) error {
	g.init()
	g.closeOnce.Do(func() {
		close(g.stopping)
		<-g.token // actual active borrower MUST settle; no hard I/O-close deadline
		if closeOwned != nil {
			g.closeErr = closeOwned()
		}
		close(g.settled)
	})
	<-g.settled
	return g.closeErr
}

// Original candidate/activation mutexes predate this lease. Their acquisition
// must not discard the same bounded caller admission context either. TryLock
// never calls source methods; cancellation stops only this queued attempt.
func controllerSourceLock(ctx, lifetime context.Context, stopping <-chan struct{}, mu *sync.Mutex) (func(), error) {
	if mu == nil {
		return nil, errors.New("original source synchronization missing")
	}
	check := func() error {
		if err := controllerSourceContextError(ctx); err != nil {
			return err
		}
		if err := controllerSourceContextError(lifetime); err != nil {
			return err
		}
		select {
		case <-stopping:
			return errors.New("original source admission closed")
		default:
			return nil
		}
	}
	if err := check(); err != nil {
		return nil, err
	}
	timer := time.NewTicker(time.Millisecond)
	defer timer.Stop()
	for {
		if mu.TryLock() {
			if err := check(); err != nil {
				mu.Unlock()
				return nil, err
			}
			return mu.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, controllerSourceContextError(ctx)
		case <-lifetime.Done():
			return nil, controllerSourceContextError(lifetime)
		case <-stopping:
			return nil, errors.New("original source admission closed")
		case <-timer.C:
		}
	}
}
