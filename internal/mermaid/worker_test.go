package mermaid

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// denseGraph is K25 (300 edges): compute-bound at the real 64 MiB ceiling
// (still running after 30 s on the reference host), and a guest trap within
// tens of milliseconds under a 16 MiB ceiling.
func denseGraph() string {
	var dense strings.Builder
	dense.WriteString("flowchart TD\n")
	for a := range 25 {
		for b := a + 1; b < 25; b++ {
			fmt.Fprintf(&dense, "N%d-->N%d\n", a, b)
		}
	}
	return dense.String()
}

func TestWorkerFramedSessionSurvivesFailedDiagram(t *testing.T) {
	// A controlled trap: under a 16 MiB guest ceiling K25 traps, which is a
	// generic failure (never claimed as an allocation failure), and the next
	// render works in a fresh module instance. The generous deadline keeps the
	// timeout path out of this test.
	limits := workerLimits{pages: 256, deadline: 30 * time.Second, stdout: MaxDrawing}
	draw := testWorkerSession(t, limits)
	for i, tc := range []struct {
		source string
		width  int
		want   Status
	}{
		{simple, 80, OK},
		{"not a diagram", 80, ParseError},
		{simple, 1, TooWide},
		{denseGraph(), 80, Failed},
		{"sequenceDiagram\n participant A as Alice\n A->>B: Hello", 80, OK},
	} {
		req := testRenderRequest(tc.width, "grapheme", tc.source)
		req.ID = uint64(i + 1)
		got := draw(req)
		if got.Result.Status != tc.want || !got.Result.valid(tc.width, ansi.GraphemeWidth) {
			t.Fatalf("reply %d: got %+v, want %s", i, got, tc.want)
		}
	}

}

// leogr's rule: a diagram may draw for 20 s; the view shows its source
// meanwhile. One exchange with a worker that stops answering ends at the
// sweep's 2-minute fail-safe.
func TestRenderDeadlineIsTwentySeconds(t *testing.T) {
	if renderDeadline != 20*time.Second || exchangeFailSafe != 2*time.Minute {
		t.Fatalf("render deadline %s, exchange fail-safe %s", renderDeadline, exchangeFailSafe)
	}
}

func TestWorkerSettlesCancelBeforeNextRequest(t *testing.T) {
	input, send := io.Pipe()
	read, output := io.Pipe()
	defer func() { _ = read.Close(); _ = send.Close() }()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		err := serveWorker(t.Context(), input, output, func(ctx context.Context, req request) Result {
			if req.ID == 1 {
				close(entered)
				<-ctx.Done()
			}
			return Result{Status: OK, Lines: []string{"drawn"}}
		})
		_ = output.CloseWithError(err)
		done <- err
	}()
	write := func(req request) {
		t.Helper()
		if err := writeFrame(send, req, maxRequest); err != nil {
			t.Fatal(err)
		}
	}
	check := func(id uint64, status Status) {
		t.Helper()
		frame, err := readFrame(read, maxResponse)
		var got response
		if err != nil || strictJSON(frame, &got) != nil || got.ID != id || got.Result.Status != status {
			t.Fatalf("reply %d: %v %+v, want %s", id, err, got, status)
		}
	}
	req := testRenderRequest(80, "grapheme", simple)
	write(req)
	<-entered
	stop := request{Version: protocol, Artifact: moduleHash(), ID: req.ID, Cancel: true}
	write(stop)
	check(1, canceled)
	write(stop) // late cancel must consume no reply and leave the next ID alone
	req.ID++
	write(req)
	check(2, OK)
	if err := send.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// At the real limits (64 MiB, 20 s) a compute-bound diagram ends at the
// render deadline: an honest timeout, and the worker keeps serving.
func TestWorkerRealLimitsTimeOutAComputeBoundDiagram(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	start := time.Now()
	draw := testWorkerSession(t, workerLimits{1024, renderDeadline, MaxDrawing})
	for i, tc := range []struct {
		source string
		want   Status
	}{{denseGraph(), Timeout}, {simple, OK}} {
		req := testRenderRequest(80, "grapheme", tc.source)
		req.ID = uint64(i + 1)
		if got := draw(req); got.Result.Status != tc.want {
			t.Fatalf("reply %d: got %+v, want %s", i, got, tc.want)
		}
	}
	// Without a deadline this graph renders for more than two minutes
	// (measured: still running at 120 s). Ending within 30 s after the 20 s
	// deadline therefore shows that the deadline stopped it, with room for a
	// loaded machine: the time also covers compiling the module and the
	// second render. Ending before it would mean a shorter deadline.
	if took := time.Since(start); took < renderDeadline || took > renderDeadline+30*time.Second {
		t.Fatalf("render deadline %s not enforced: %s", renderDeadline, took)
	}
}

func TestWorkerRenderDeadlineAndOutputBound(t *testing.T) {
	ctx := t.Context()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	defer func() { _ = rt.Close(ctx) }()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		t.Fatal(err)
	}
	compiled, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	req := testRenderRequest(80, "grapheme", "flowchart LR\n A[Build]-->B[Test]")
	for _, tc := range []struct {
		name   string
		limits workerLimits
		want   Status
	}{
		{"deadline", workerLimits{1024, time.Nanosecond, MaxDrawing}, Timeout},
		{"stdout bound", workerLimits{1024, 2 * time.Second, 1}, Failed},
		{"recovery", workerLimits{1024, 2 * time.Second, MaxDrawing}, OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(ctx, rt, compiled, req, ansi.GraphemeWidth, tc.limits); got.Status != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
		})
	}
	// A canceled parent also stops execution, but is not claimed as a per-render timeout.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := render(canceled, rt, compiled, req, ansi.GraphemeWidth, workerLimits{1024, time.Second, MaxDrawing}); got.Status == OK || got.Status == Timeout {
		t.Fatalf("cancellation: %+v", got)
	}
}

func testRenderRequest(width int, method, source string) request {
	return request{Version: protocol, Artifact: moduleHash(), Width: width, Method: method, Source: source, ID: 1}
}

// Use real request/reply ordering: the next frame is sent after the terminal
// reply. The worker keeps one compiled module throughout the session.
func testWorkerSession(t *testing.T, limits workerLimits) func(request) response {
	t.Helper()
	input, send := io.Pipe()
	read, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := runWorker(context.Background(), input, output, limits)
		_ = output.CloseWithError(err)
		done <- err
	}()
	t.Cleanup(func() {
		_ = send.Close()
		_ = read.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return func(req request) response {
		t.Helper()
		if err := writeFrame(send, req, maxRequest); err != nil {
			t.Fatal(err)
		}
		frame, err := readFrame(read, maxResponse)
		var got response
		if err != nil || strictJSON(frame, &got) != nil || got.Version != protocol || got.Artifact != moduleHash() || got.ID != req.ID {
			t.Fatalf("reply to %d: %v %+v", req.ID, err, got)
		}
		return got
	}
}
