package mermaid

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type workerLimits struct {
	pages    uint32
	deadline time.Duration
	stdout   int
}

// renderDeadline is how long one diagram may draw (leogr): the view shows the
// source meanwhile and the drawing replaces it if it ends in time; after it
// the source stays, as a cached renderer timeout.
const renderDeadline = 20 * time.Second

// RunWorker serves private framed requests. Call only in the owned host child:
// the guest has a hard 64 MiB ceiling; Go's 256 MiB heap target is soft.
func RunWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	return runWorker(ctx, in, out, workerLimits{1024, renderDeadline, MaxDrawing})
}

func runWorker(ctx context.Context, in io.Reader, out io.Writer, limits workerLimits) error {
	previous := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(previous)
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(limits.pages).WithCloseOnContextDone(true))
	defer func() { _ = runtime.Close(ctx) }()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return err
	}
	compiled, err := runtime.CompileModule(ctx, artifact)
	if err != nil {
		return err
	}
	return serveWorker(ctx, in, out, func(ctx context.Context, req request) Result {
		method, _ := widthMethod(req.Method)
		return render(ctx, runtime, compiled, req, method, limits)
	})
}

type workerRequest struct {
	request request
	err     error
}

func readWorkerRequests(in io.Reader, done <-chan struct{}) <-chan workerRequest {
	requests := make(chan workerRequest)
	go func() {
		defer close(requests)
		for {
			frame, err := readFrame(in, maxRequest)
			if errors.Is(err, io.EOF) {
				return
			}
			var req request
			if err == nil && (strictJSON(frame, &req) != nil || !req.valid()) {
				err = errors.New("invalid Mermaid request")
			}
			select {
			case requests <- workerRequest{req, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return requests
}

// serveWorker owns the active request, and is the only writer of replies.
// The frame reader continues receiving cancellation while WASM runs.
func serveWorker(ctx context.Context, in io.Reader, out io.Writer, draw func(context.Context, request) Result) error {
	done := make(chan struct{})
	defer close(done)
	if closer, ok := in.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	requests := readWorkerRequests(in, done)
	results := make(chan Result, 1)
	var active, settled uint64
	var cancel context.CancelFunc
	defer func() {
		if active != 0 {
			cancel()
			<-results // finish before the runtime is closed
		}
	}()
	for {
		if requests == nil && active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if frame.err != nil {
				return frame.err
			}
			req := frame.request
			if req.Cancel {
				switch req.ID {
				case active:
					cancel()
				case settled:
					// The terminal result may already be on its way to the client.
				default:
					return errors.New("invalid Mermaid cancellation")
				}
				continue
			}
			if active != 0 || req.ID <= settled {
				return errors.New("unexpected Mermaid render request")
			}
			renderCtx, stop := context.WithCancel(ctx)
			cancel = stop
			active = req.ID
			go func() {
				defer stop()
				result := draw(renderCtx, req)
				if renderCtx.Err() != nil {
					result = Result{Status: canceled}
				}
				results <- result
			}()
		case result := <-results:
			cancel()
			settled, active = active, 0
			if err := writeFrame(out, response{protocol, moduleHash(), result, settled}, maxResponse); err != nil {
				return err
			}
		}
	}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := min(len(p), max(0, b.limit-b.Len()))
	_, _ = b.Buffer.Write(p[:n])
	if n != len(p) {
		b.overflow = true
		return n, io.ErrShortWrite
	}
	return n, nil
}

func render(ctx context.Context, runtime wazero.Runtime, compiled wazero.CompiledModule,
	req request, method ansi.Method, limits workerLimits,
) Result {
	ctx, cancel := context.WithTimeout(ctx, limits.deadline)
	defer cancel()
	out := boundedBuffer{limit: limits.stdout}
	diagnostic := boundedBuffer{limit: 4096}
	source, standIns := withStandIns(req.Source, method)
	// No preopens, environment, filesystem, sockets or inherited host handles.
	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
		WithName("").WithArgs("mermaid", strconv.Itoa(req.Width)).
		WithStdin(strings.NewReader(source)).WithStdout(&out).WithStderr(&diagnostic))
	if module != nil {
		defer func() { _ = module.Close(ctx) }()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: Timeout}
	}
	if out.overflow || diagnostic.overflow {
		return Result{Status: Failed}
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) && exit.ExitCode() != 0 {
		switch exit.ExitCode() {
		case 3:
			return Result{Status: ParseError}
		case 4:
			needed, err := strconv.Atoi(strings.TrimSpace(diagnostic.String()))
			if err == nil && needed > req.Width && needed <= MaxDrawing*2 {
				return Result{Status: TooWide, NeedCols: needed}
			}
		}
		return Result{Status: Failed}
	}
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 0) {
		// A trap (even at the memory ceiling) does not prove an allocation failure.
		return Result{Status: Failed}
	}
	drawing, whole := restoreStandIns(out.String(), standIns)
	if !whole {
		return Result{Status: Failed}
	}
	return verifyDrawing(drawing, req.Width, method)
}
