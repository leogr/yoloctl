package main

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
)

// restartSessionOutput owns only the diagnostic streams of space start's
// restart callback. Plain/debug and every command-data stream keep their
// existing writers. A replaced Err must not redirect typed worker records.
func restartSessionOutput(ctx context.Context, name string, streams controller.ClientIO, run func(controller.ClientIO) error) error {
	if stepview.Current() == nil || logging.Verbosity() != prettylog.Normal {
		return run(streams)
	}
	step := cliStep(ctx, "Restarting box "+name, prettylog.Actor(logging.Box))
	out := restartDetailStream{detail: step.Detail()}
	errw := restartDetailStream{detail: step.Detail()}
	streams.Out, streams.Err = &out, &errw
	streams.Log = controller.HostLogSink()
	result := run(streams)
	out.flush()
	errw.flush()
	cliEndStep(step, result)
	return result
}

// Each descriptor keeps its own partial line. Completed lines enter the same
// Step.Detail writer atomically, so interleaved stdout/stderr chunks cannot
// join unrelated diagnostics. Reuse Detail's existing MaxWire partial bound.
type restartDetailStream struct {
	mu     sync.Mutex
	detail io.Writer
	buf    []byte
}

func (w *restartDetailStream) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = w.detail.Write(w.buf[:i+1])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > prettylog.MaxWire {
		w.flushLocked()
	}
	return len(p), nil
}

func (w *restartDetailStream) flush() { w.mu.Lock(); defer w.mu.Unlock(); w.flushLocked() }
func (w *restartDetailStream) flushLocked() {
	if len(w.buf) > 0 {
		_, _ = w.detail.Write(append(w.buf, '\n'))
		w.buf = nil
	}
}
