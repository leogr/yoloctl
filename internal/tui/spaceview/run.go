package spaceview

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

const (
	minBackoff = time.Second
	maxBackoff = 15 * time.Second
)

// Run shows the view until the user quits or ctx ends. The stream reconnects
// with backoff; reading never starts a VM, so a stopped space stays visible as
// unavailable until it is started elsewhere.
func Run(ctx context.Context, opts Options) error {
	m := New(ctx, opts)
	p := tea.NewProgram(m, tea.WithoutSignalHandler())
	retry := make(chan struct{}, 1)
	m.retry = func() {
		select {
		case retry <- struct{}{}:
		default:
		}
	}
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	go keepConnected(streamCtx, opts, p.Send, retry)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			p.Quit()
		case <-done:
		}
	}()
	_, err := p.Run()
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

func keepConnected(ctx context.Context, opts Options, send func(tea.Msg), retry <-chan struct{}) {
	backoff := minBackoff
	for {
		gotFrames := false
		err := opts.Connect(ctx, func(f spacetelemetry.Frame) {
			gotFrames = true
			send(frameMsg(f))
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("the telemetry stream ended")
		}
		if gotFrames {
			backoff = minBackoff
		}
		send(lostMsg{err: err, retryAt: time.Now().Add(backoff)})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		case <-retry:
		}
		backoff = min(backoff*2, maxBackoff)
		send(connectingMsg{})
	}
}

type connectingMsg struct{}
