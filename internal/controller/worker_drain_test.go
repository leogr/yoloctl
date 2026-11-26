package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func withFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	old := controllerFailSafe
	controllerFailSafe = d
	t.Cleanup(func() { controllerFailSafe = old })
}

// stalledClient is a connected client that never reads: writes block until
// the drain deadline releases them, as on the controller's socket.
func stalledClient(t *testing.T) (clientSink, *clientOutput, func(time.Time)) {
	t.Helper()
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	shared := &clientOutput{}
	return clientSink{server, shared}, shared, func(d time.Time) { _ = server.SetWriteDeadline(d) }
}

func workerStreams(out io.Writer, deadline func(time.Time)) Streams {
	return Streams{In: strings.NewReader(""), Out: out, Err: io.Discard, Release: func() error { return nil }, DrainDeadline: deadline}
}

// A transient child of the worker's own group that inherited stdout no longer
// holds a finished command: the group is killed at exit, before output joins.
func TestWorkerDescendantHoldingStdoutEndsAtExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	started := time.Now()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.inherit-stdout"}, workerStreams(&out, nil))
	if code != 7 || err != nil || !strings.HasSuffix(out.String(), "parent done") || time.Since(started) > 2500*time.Millisecond {
		t.Fatalf("code=%d err=%v out=%q after %s", code, err, out.String(), time.Since(started))
	}
}

// A process outside the worker's group still holding stdout (or the PTY) keeps
// the output join only until the fail-safe: the worker returns while that
// holder is still alive, and the command is reported interrupted.
func TestWorkerOutputHeldOutsideTheGroupEndsAtFailSafe(t *testing.T) {
	withFailSafe(t, 300*time.Millisecond)
	for _, terminal := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		var out bytes.Buffer
		started := time.Now()
		code, err := RunWorker(ctx, Principal{}, Request{Method: "test.escape-stdout", Terminal: terminal, Cols: 80, Rows: 24, Term: "xterm"}, workerStreams(&out, nil))
		elapsed := time.Since(started)
		cancel()
		var pid int
		if _, scanErr := fmt.Sscanf(out.String(), "holder=%d", &pid); scanErr != nil || pid <= 0 {
			t.Fatalf("terminal=%v: no holder pid in %q", terminal, out.String())
		}
		alive := syscall.Kill(pid, 0) == nil
		_ = syscall.Kill(pid, syscall.SIGKILL)
		want := "worker exited with its output still open"
		if terminal {
			want = "worker exited with an inherited terminal still open"
		}
		var interrupted *WorkerInterrupted
		if !alive || elapsed > 5*time.Second || !errors.As(err, &interrupted) || !strings.Contains(err.Error(), want) || code != 2 {
			t.Fatalf("terminal=%v: holder alive=%v after %s: code=%d err=%v", terminal, alive, elapsed, code, err)
		}
	}
}

// A connected client that stops reading is detached at the fail-safe; the
// finished worker keeps its own result, in pipe and in PTY mode.
func TestWorkerStalledClientIsDetachedWithItsResult(t *testing.T) {
	withFailSafe(t, 300*time.Millisecond)
	for _, terminal := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		sink, shared, deadline := stalledClient(t)
		started := time.Now()
		code, err := RunWorker(ctx, Principal{}, Request{Method: "test.small", Terminal: terminal, Cols: 80, Rows: 24, Term: "xterm"}, workerStreams(sink, deadline))
		cancel()
		if code != 9 || err != nil || !shared.detached.Load() || time.Since(started) > 10*time.Second {
			t.Fatalf("terminal=%v: code=%d err=%v detached=%v after %s", terminal, code, err, shared.detached.Load(), time.Since(started))
		}
	}
}

// A slow client that resumes reading after the worker exited, later than
// today's 5-second limit, gets every byte and the worker's exit code.
func TestWorkerSlowClientResumesWithExactOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the former 5-second drain limit")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	server, peer := net.Pipe()
	defer func() { _ = server.Close(); _ = peer.Close() }()
	shared := &clientOutput{}
	received := make(chan []byte, 1)
	go func() {
		time.Sleep(6 * time.Second)
		raw, _ := io.ReadAll(peer)
		received <- raw
	}()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.small"}, workerStreams(clientSink{server, shared}, func(d time.Time) { _ = server.SetWriteDeadline(d) }))
	_ = server.Close()
	got := <-received
	if code != 9 || err != nil || shared.detached.Load() || !bytes.Equal(got, bytes.Repeat([]byte("small-"), 170)) {
		t.Fatalf("code=%d err=%v detached=%v got=%d bytes", code, err, shared.detached.Load(), len(got))
	}
}

// Row 84: a checkpoint descriptor still held after the group SIGKILL keeps the
// join until the fail-safe (no longer one second), then the operation is
// reported interrupted as today.
func TestWorkerCheckpointHeldPastKillEndsAtFailSafe(t *testing.T) {
	withFailSafe(t, 1500*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	started := time.Now()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.escape-checkpoint"}, workerStreams(io.Discard, nil))
	elapsed := time.Since(started)
	var interrupted *WorkerInterrupted
	if !errors.As(err, &interrupted) || code != 7 || elapsed < 1400*time.Millisecond || elapsed > 2900*time.Millisecond {
		t.Fatalf("code=%d err=%v after %s", code, err, elapsed)
	}
}

// Row 82: each wait of the opening exchange has its own fail-safe, so a slow
// client whose waits add up past one fail-safe still gets its reply.
func TestServerOpeningExchangeBoundsEachWait(t *testing.T) {
	withFailSafe(t, 400*time.Millisecond)
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
	wire := NewWire(client, client)
	h := hello()
	time.Sleep(250 * time.Millisecond)
	if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
		t.Fatal(err)
	}
	if f, err := wire.Read(); err != nil || f.Type != "hello" {
		t.Fatal(f, err)
	}
	time.Sleep(250 * time.Millisecond)
	req := request("control.ping")
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := wire.Read()
		if err != nil {
			t.Fatal("slow client lost its reply:", err)
		}
		if f.Type == "exit" {
			if f.Status != 0 {
				t.Fatal(f)
			}
			break
		}
	}
	<-done
}

// A client silent on its hello, or after it, is dropped at the fail-safe.
func TestServerOpeningExchangeDropsASilentClient(t *testing.T) {
	withFailSafe(t, 300*time.Millisecond)
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}}
	for _, afterHello := range []bool{false, true} {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
		if afterHello {
			wire := NewWire(client, client)
			h := hello()
			if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
				t.Fatal(err)
			}
			if f, err := wire.Read(); err != nil || f.Type != "hello" {
				t.Fatal(f, err)
			}
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("afterHello=%v: silent client kept its connection", afterHello)
		}
		_ = client.Close()
	}
}

// A hello that arrives after today's 10-second connection deadline is served.
func TestServerOpeningExchangeServesALateHello(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the former 10-second limit")
	}
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go s.ServeConn(t.Context(), server, Principal{})
	wire := NewWire(client, client)
	h := hello()
	time.Sleep(10500 * time.Millisecond)
	if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
		t.Fatal("late hello refused:", err)
	}
	if f, err := wire.Read(); err != nil || f.Type != "hello" {
		t.Fatal(f, err)
	}
}

// Case 2 of the G3 source review: a client that stops reading while a finished
// worker's output is delivered is detached and its connection cancelled; the
// recorded result stays the worker's own exit, successful or not.
func TestNonreadingClientAfterExitKeepsTheRecordedResult(t *testing.T) {
	withFailSafe(t, 300*time.Millisecond)
	for _, code := range []int{0, 9} {
		s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
			Operations: map[string]Operation{"test.bulk-exit": {BoxAllowed: true, Mutates: true}},
			Execute:    RunWorker,
		}
		req := request("test.bulk-exit")
		req.Args = []string{strconv.Itoa(code)}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
		wire := NewWire(client, client)
		h := hello()
		if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
			t.Fatal(err)
		}
		if f, err := wire.Read(); err != nil || f.Type != "hello" {
			t.Fatal(f, err)
		}
		if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := wire.Read()
			if err != nil {
				t.Fatal("before the first output frame:", err)
			}
			if f.Type == "exit" {
				t.Fatal("exit before the stall", f)
			}
			if f.Type == "stdout" {
				break // the client stops reading here
			}
		}
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("exit %d: a nonreading client held the finished command", code)
		}
		_ = client.Close()
		r, err := s.Receipts.Read(req.ID)
		if err != nil || r.State != "complete" || r.Status != code {
			t.Fatalf("exit %d: receipt %+v, %v", code, r, err)
		}
	}
}

// An outside-group holder that keeps writing cannot extend the output join:
// after the fail-safe only the bytes ready at that moment are drained, the
// worker returns at the fail-safe while the writer still produces, and the
// command is reported interrupted with its output held open.
func TestWorkerOutputWriterOutsideTheGroupEndsAtFailSafe(t *testing.T) {
	withFailSafe(t, 300*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	started := time.Now()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.escape-writer"}, workerStreams(&out, nil))
	elapsed := time.Since(started)
	var pid int
	if _, scanErr := fmt.Sscanf(out.String(), "holder=%d", &pid); scanErr != nil || pid <= 0 {
		t.Fatalf("no holder pid in %.200q", out.String())
	}
	// The writer dies of SIGPIPE once the controller closes the read end; the
	// join ended at the fail-safe while it was still producing.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	var interrupted *WorkerInterrupted
	if elapsed > 5*time.Second || !errors.As(err, &interrupted) || !strings.Contains(err.Error(), "worker exited with its output still open") || code != 2 {
		t.Fatalf("after %s: code=%d err=%v", elapsed, code, err)
	}
	if !strings.Contains(out.String(), "writer-line") {
		t.Fatal("the writer's output never reached the client")
	}
}
