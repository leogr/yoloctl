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

// RunWorker serves private framed requests. Call only in the owned host child:
// the guest has a hard 64 MiB ceiling; Go's 256 MiB heap target is soft.
func RunWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	return runWorker(ctx, in, out, workerLimits{1024, 2 * time.Second, MaxDrawing})
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
	for {
		p, err := readFrame(in, maxRequest)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if strictJSON(p, &req) != nil || !req.valid() {
			return errors.New("invalid Mermaid request")
		}
		method, _ := widthMethod(req.Method)
		result := render(ctx, runtime, compiled, req, method, limits)
		if err := writeFrame(out, response{protocol, moduleHash(), result}, maxResponse); err != nil {
			return err
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
	// No preopens, environment, filesystem, sockets or inherited host handles.
	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
		WithName("").WithArgs("mermaid", strconv.Itoa(req.Width)).
		WithStdin(strings.NewReader(req.Source)).WithStdout(&out).WithStderr(&diagnostic))
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
	return verifyDrawing(out.String(), req.Width, method)
}
