package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type commandSignalsKey struct{}
type commandSignals struct {
	mu      sync.Mutex
	forward chan<- int
}

// Ordinary commands cancel on a process signal. An active controller stream
// forwards it instead, allowing remote detach/exit output and status to arrive.
// A second signal cancels if the remote command does not respond.
func commandSignalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &commandSignals{}
	ctx = context.WithValue(ctx, commandSignalsKey{}, state)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		forwarded := false
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-signals:
				state.mu.Lock()
				out := state.forward
				if out != nil && !forwarded {
					select {
					case out <- int(sig.(syscall.Signal)):
						forwarded = true
					default:
						cancel()
					}
				} else {
					cancel()
				}
				state.mu.Unlock()
			}
		}
	}()
	return ctx, func() { signal.Stop(signals); cancel() }
}

func forwardCommandSignals(ctx context.Context, out chan<- int) func() {
	state, _ := ctx.Value(commandSignalsKey{}).(*commandSignals)
	if state == nil {
		return func() {}
	}
	state.mu.Lock()
	state.forward = out
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		state.forward = nil
		state.mu.Unlock()
	}
}
