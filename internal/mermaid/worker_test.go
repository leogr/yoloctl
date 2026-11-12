package mermaid

import (
	"bytes"
	"context"
	"fmt"
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
	var in, out bytes.Buffer
	for _, tc := range []struct {
		source string
		width  int
	}{
		{"flowchart LR\n A[Build]-->B[Test]", 80},
		{"not a diagram", 80},
		{"flowchart LR\n A[Build]-->B[Test]", 1},
		{denseGraph(), 80},
		{"sequenceDiagram\n participant A as Alice\n A->>B: Hello", 80},
	} {
		if err := writeFrame(&in, request{protocol, moduleHash(), tc.width, "grapheme", tc.source}, maxRequest); err != nil {
			t.Fatal(err)
		}
	}
	if err := runWorker(t.Context(), &in, &out, limits); err != nil {
		t.Fatal(err)
	}
	for i, want := range []Status{OK, ParseError, TooWide, Failed, OK} {
		p, err := readFrame(&out, maxResponse)
		var got response
		if err != nil || strictJSON(p, &got) != nil || got.Version != protocol || got.Artifact != moduleHash() || got.Result.Status != want {
			t.Fatalf("reply %d: err=%v got=%+v want=%s", i, err, got, want)
		}
		if !got.Result.valid(80, ansi.GraphemeWidth) && want != TooWide {
			t.Fatalf("invalid reply: %+v", got)
		}
	}
	if out.Len() != 0 {
		t.Fatal("extra reply bytes")
	}
}

// At the real limits (64 MiB, 2 s) a compute-bound diagram ends at the
// render deadline: an honest timeout, and the worker keeps serving.
func TestWorkerRealLimitsTimeOutAComputeBoundDiagram(t *testing.T) {
	skipUnderRace(t)
	var in, out bytes.Buffer
	for _, source := range []string{denseGraph(), "flowchart LR\n A[Build]-->B[Test]"} {
		if err := writeFrame(&in, request{protocol, moduleHash(), 80, "grapheme", source}, maxRequest); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if err := RunWorker(t.Context(), &in, &out); err != nil {
		t.Fatal(err)
	}
	for i, want := range []Status{Timeout, OK} {
		p, err := readFrame(&out, maxResponse)
		var got response
		if err != nil || strictJSON(p, &got) != nil || got.Result.Status != want {
			t.Fatalf("reply %d: err=%v got=%+v want=%s", i, err, got, want)
		}
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("deadline not enforced: %s", took)
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
	req := request{protocol, moduleHash(), 80, "grapheme", "flowchart LR\n A[Build]-->B[Test]"}
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
