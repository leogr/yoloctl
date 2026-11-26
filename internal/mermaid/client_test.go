package mermaid

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const silentWorkerArgument = "__mermaid-test-silent-worker"
const observedWorkerArgument = "__mermaid-test-observed-worker"

// The test binary is its own worker when started with a worker argument.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case WorkerArgument:
			os.Exit(WorkerMain())
		case silentWorkerArgument:
			os.Exit(runSilentWorker())
		case observedWorkerArgument:
			os.Exit(runObservedWorker())
		}
	}
	os.Exit(m.Run())
}

// The child uses the real module and reports entry to its render function on
// a separate descriptor. Stdout still carries only the private worker frames.
func runObservedWorker() int {
	started := os.NewFile(3, "render-start")
	defer func() { _ = started.Close() }()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	defer func() { _ = rt.Close(ctx) }()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return 1
	}
	module, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		return 1
	}
	err = serveWorker(ctx, os.Stdin, os.Stdout, func(ctx context.Context, req request) Result {
		if req.Source == denseGraph() {
			if _, err := started.Write([]byte{1}); err != nil {
				return Result{Status: Failed}
			}
		}
		method, _ := widthMethod(req.Method)
		return render(ctx, rt, module, req, method, workerLimits{1024, renderDeadline, MaxDrawing})
	})
	if err != nil {
		return 1
	}
	return 0
}

// runSilentWorker replies once to confirm a real start, then stays silent.
func runSilentWorker() int {
	frame, err := readFrame(os.Stdin, maxRequest)
	var req request
	if err != nil || strictJSON(frame, &req) != nil {
		return 1
	}
	resp := response{
		Version:  req.Version,
		Artifact: req.Artifact,
		ID:       req.ID,
		Result:   Result{Status: OK, Lines: []string{"ready"}},
	}
	if err := writeFrame(os.Stdout, resp, maxResponse); err != nil {
		return 1
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

func silentWorker() *exec.Cmd {
	return exec.Command(os.Args[0], silentWorkerArgument)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testClient starts processes from build and counts the starts.
func testClient(t *testing.T, build func() *exec.Cmd) (*Client, *atomic.Int32, *clock) {
	t.Helper()
	c := NewClient()
	clk := &clock{t: time.Now()}
	c.now = clk.now
	c.hostOnly = func() bool { return true }
	c.guard.Store(int64(testGuard))
	var starts atomic.Int32
	c.command = func() *exec.Cmd { starts.Add(1); return build() }
	t.Cleanup(c.Close)
	return c, &starts, clk
}

func selfWorker() *exec.Cmd {
	cmd := exec.Command(os.Args[0], WorkerArgument)
	cmd.Env = []string{"LANG=C.UTF-8"}
	return cmd
}

const simple = "flowchart LR\n A[Build]-->B[Test]"

func TestClientRendersOnceAndCachesDeterministicOutcomes(t *testing.T) {
	c, starts, _ := testClient(t, selfWorker)
	ctx := context.Background()
	first := c.Render(ctx, simple, 80, ansi.GraphemeWidth)
	if first.Status != OK || len(first.Lines) == 0 {
		t.Fatalf("%+v", first)
	}
	if again := c.Render(ctx, simple, 80, ansi.GraphemeWidth); again.Status != OK || strings.Join(again.Lines, "\n") != strings.Join(first.Lines, "\n") {
		t.Fatalf("%+v", again)
	}
	if got := c.Render(ctx, "not a diagram", 80, ansi.GraphemeWidth); got.Status != ParseError {
		t.Fatalf("%+v", got)
	}
	if got := c.Render(ctx, simple, 1, ansi.GraphemeWidth); got.Status != TooWide || got.NeedCols <= 1 {
		t.Fatalf("%+v", got)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("%d worker starts", n)
	}
	// The width method is part of the key.
	if got := c.Render(ctx, simple, 80, ansi.WcWidth); got.Status != OK {
		t.Fatalf("%+v", got)
	}
}

// At the real limits a compute-bound diagram draws until the 20 s render
// deadline, then times out; the timeout is kept for the session (no-retry
// policy), and the worker keeps serving.
func TestClientTimeoutIsNotRetried(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	c, _, _ := testClient(t, selfWorker)
	ctx := context.Background()
	warm(t, c, 80) // measure the render deadline, not the worker's start
	start := time.Now()
	if got := c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth); got.Status != Timeout {
		t.Fatalf("%+v", got)
	}
	if took := time.Since(start); took < renderDeadline-time.Second || took > renderDeadline+30*time.Second {
		t.Fatalf("took %s, want the %s render deadline", took, renderDeadline)
	}
	start = time.Now()
	if got := c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth); got.Status != Timeout || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("not cached: %+v", got)
	}
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("%+v", got)
	}
}

// A request queued behind a long render waits as long as its caller does: one
// whose caller gives up is busy and not cached; one whose caller waits draws
// once the worker is free.
func TestClientQueuedRequestFollowsItsCaller(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	c, _, _ := testClient(t, selfWorker)
	ctx := context.Background()
	warm(t, c, 80)
	slow := make(chan Result, 1)
	go func() { slow <- c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth) }()
	for len(c.slot) == 0 {
		time.Sleep(time.Millisecond)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if got := c.Render(short, simple, 80, ansi.GraphemeWidth); got.Status != Busy {
		t.Fatalf("%+v", got)
	}
	start := time.Now()
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("queued request: %+v (busy was cached?)", got)
	}
	if took := time.Since(start); took < renderDeadline/2 {
		t.Fatalf("queued request drew after %s, before the long render ended", took)
	}
	if got := <-slow; got.Status != Timeout {
		t.Fatalf("%+v", got)
	}
}

// A closed view starts no worker: a request whose caller has already ended
// returns at once.
func TestClientEndedCallerStartsNoWorker(t *testing.T) {
	c, _, _ := testClient(t, selfWorker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != Busy {
		t.Fatalf("%+v", got)
	}
	c.mu.Lock()
	started := c.proc != nil
	c.mu.Unlock()
	if started {
		t.Fatal("an ended caller started a worker")
	}
}

// warmSilentWorker starts the worker through Render before the measured
// request. Each start uses a new width so the warm-up reaches the worker.
func warmSilentWorker(t *testing.T, c *Client, width int) *workerProcess {
	t.Helper()
	warm(t, c, width)
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		t.Fatal("warm-up returned with no live worker")
	}
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

// holdReply leaves the reply pending on a Go pipe. The pipe lets synctest
// advance time after the client has sent its request and is waiting for a reply.
func holdReply(t *testing.T, p *workerProcess) {
	t.Helper()
	reply, withheld := io.Pipe()
	p.stdout = bufio.NewReader(reply)
	t.Cleanup(func() {
		_ = withheld.Close()
		_ = reply.Close()
	})
}

// The bubble joins the client's reaper before the test reads ProcessState.
func requireWorkerKilled(t *testing.T, c *Client, p *workerProcess) {
	t.Helper()
	state := p.cmd.ProcessState
	if state == nil || state.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("worker was not killed: %v", state)
	}
	c.mu.Lock()
	live := c.proc != nil
	c.mu.Unlock()
	if live {
		t.Fatal("killed worker left the client slot occupied")
	}
}

// A worker that does not answer within the guard is killed. Following the
// first restart, another must wait until restartEvery has elapsed.
func TestClientKillsAHungWorkerAndThrottlesRestarts(t *testing.T) {
	c, starts, clk := testClient(t, silentWorker)
	ctx := context.Background()
	expire := func(width int, wantStarts int32) {
		t.Helper()
		p := warmSilentWorker(t, c, width)
		const guard = 200 * time.Millisecond
		previous := c.guard.Swap(int64(guard))
		defer c.guard.Store(previous)
		synctest.Test(t, func(t *testing.T) {
			holdReply(t, p)
			start := time.Now()
			got := c.Render(ctx, simple, width, ansi.GraphemeWidth)
			if got.Status != Unavailable || time.Since(start) != guard {
				t.Fatalf("hung worker: %+v after %s, want unavailable at %s", got, time.Since(start), guard)
			}
		})
		requireWorkerKilled(t, c, p)
		if n := starts.Load(); n != wantStarts {
			t.Fatalf("%d starts, want %d", n, wantStarts)
		}
	}

	expire(80, 1)
	expire(81, 2)
	if got := c.Render(ctx, simple, 82, ansi.GraphemeWidth); got.Status != Unavailable || starts.Load() != 2 {
		t.Fatalf("restart during the throttle: %+v starts=%d", got, starts.Load())
	}
	clk.add(restartEvery)
	expire(90, 3)
}

// A reply that breaks the protocol is an internal error; the worker is
// replaced and the outcome is not cached.
func TestClientRejectsAProtocolViolation(t *testing.T) {
	c, starts, _ := testClient(t, func() *exec.Cmd {
		return exec.Command("sh", "-c", `cat >/dev/null & printf '\000\000\000\002{}'; sleep 5`)
	})
	if got := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); got.Status != Internal {
		t.Fatalf("%+v", got)
	}
	c.mu.Lock()
	alive := c.proc != nil
	c.mu.Unlock()
	if alive || starts.Load() != 1 {
		t.Fatal("violating worker kept")
	}
}

func TestClientStartsNoWorkerOutsideTheHost(t *testing.T) {
	c, starts, _ := testClient(t, selfWorker)
	c.hostOnly = func() bool { return false }
	if got := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); got.Status != Unavailable || starts.Load() != 0 {
		t.Fatalf("%+v starts=%d", got, starts.Load())
	}
}

func TestResultCacheBounds(t *testing.T) {
	c := newResultCache()
	for i := range cacheEntries + 6 {
		c.put(newCacheKey(simple, i+1, "grapheme"), Result{Status: OK, Lines: []string{"x"}})
	}
	if c.order.Len() != cacheEntries {
		t.Fatal(c.order.Len())
	}
	if _, ok := c.get(newCacheKey(simple, 1, "grapheme")); ok {
		t.Fatal("oldest entry kept")
	}
	big := strings.Repeat("y", 1<<20)
	for i := range 5 {
		c.put(newCacheKey(simple, 1000+i, "grapheme"), Result{Status: OK, Lines: []string{big}})
	}
	if c.bytes > cacheBytes {
		t.Fatal(c.bytes)
	}
	r := Result{Status: OK, Lines: []string{"a"}}
	k := newCacheKey("x", 1, "grapheme")
	c.put(k, r)
	r.Lines[0] = "changed"
	if got, _ := c.get(k); got.Lines[0] != "a" {
		t.Fatal("cache shares the caller's lines")
	}
}

// A drawing over the 120-row cap is a deterministic "too tall", not a
// renderer failure.
func TestClientReportsATooTallDrawing(t *testing.T) {
	c, _, _ := testClient(t, selfWorker)
	var chain strings.Builder
	chain.WriteString("flowchart TD\n")
	for i := range 60 {
		chain.WriteString(" N" + strconv.Itoa(i) + "-->N" + strconv.Itoa(i+1) + "\n")
	}
	got := c.Render(context.Background(), chain.String(), 80, ansi.GraphemeWidth)
	if got.Status != TooTall || got.Note() != "taller than 120 rows" {
		t.Fatalf("%+v", got)
	}
}

// The guard runs from enqueue through a slow worker start: the request gives
// up on time, and the late-started worker becomes the client's own worker
// (reused next, ended by Close), never an orphan. Cancellation during the
// start is prompt too.
func TestClientGuardCoversWorkerStartup(t *testing.T) {
	c, starts, _ := testClient(t, func() *exec.Cmd {
		time.Sleep(250 * time.Millisecond)
		return selfWorker()
	})
	c.guard.Store(int64(50 * time.Millisecond))
	start := time.Now()
	if got := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); got.Status != Unavailable {
		t.Fatalf("%+v", got)
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("startup outlasted the guard: %s", took)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		adopted := c.proc != nil
		c.mu.Unlock()
		if adopted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("late worker not adopted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.guard.Store(int64(testGuard))
	if got := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); got.Status != OK || starts.Load() != 1 {
		t.Fatalf("late worker not reused: %+v starts=%d", got, starts.Load())
	}
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start = time.Now()
	if got := c.Render(ctx, simple, 81, ansi.GraphemeWidth); got.Status != Busy || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("cancel during start: %+v after %s", got, time.Since(start))
	}
}

// A reply that is not a valid frame or JSON is a protocol violation
// (internal), while a worker that just goes away is unavailable.
func TestClientClassifiesMalformedRepliesAsInternal(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         Status
	}{
		{"malformed JSON", `cat >/dev/null & printf '\000\000\000\001{'; sleep 5`, Internal},
		{"oversized frame", `cat >/dev/null & printf '\377\377\377\377'; sleep 5`, Internal},
		{"worker exits", `exit 0`, Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := testClient(t, func() *exec.Cmd { return exec.Command("sh", "-c", tc.script) })
			if got := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); got.Status != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// The hidden worker refuses to run outside the host CLI context, before it
// reads anything; the client's minimal environment is accepted.
func TestWorkerEntryRefusesNonHostContexts(t *testing.T) {
	var frame strings.Builder
	if err := writeFrame(&frame, testRenderRequest(80, "grapheme", simple), maxRequest); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) (int, int) {
		cmd := exec.Command(os.Args[0], WorkerArgument)
		cmd.Env = append([]string{"LANG=C.UTF-8"}, env...)
		cmd.Stdin = strings.NewReader(frame.String())
		out, _ := cmd.Output()
		return cmd.ProcessState.ExitCode(), len(out)
	}
	for _, env := range []string{"OPENSHELL_SANDBOX=1", "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json"} {
		if code, n := run(env); code != 2 || n != 0 {
			t.Fatalf("%s: exit %d, %d reply bytes", env, code, n)
		}
	}
	if code, n := run(); code != 0 || n == 0 {
		t.Fatalf("host: exit %d, %d reply bytes", code, n)
	}
}

// Close wins over starts still waiting to run: requests that timed out while
// a slow start held the client leave no worker behind a Close.
func TestClientCloseLeavesNoWorkerFromPendingStarts(t *testing.T) {
	for range 5 {
		c, starts, _ := testClient(t, func() *exec.Cmd {
			time.Sleep(250 * time.Millisecond)
			return selfWorker()
		})
		c.guard.Store(int64(50 * time.Millisecond))
		for i := range 2 {
			if got := c.Render(context.Background(), simple, 80+i, ansi.GraphemeWidth); got.Status != Unavailable {
				t.Fatalf("%+v", got)
			}
		}
		c.Close()
		time.Sleep(700 * time.Millisecond) // past every pending start
		c.mu.Lock()
		alive := c.proc != nil
		c.mu.Unlock()
		if alive || starts.Load() > 1 {
			t.Fatalf("worker after Close: alive=%v starts=%d", alive, starts.Load())
		}
	}
}

// A request that gave up while another start held the client never starts a
// worker later, even when it began after a Close that is still waiting.
func TestClientAbandonedStartNeverBegins(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	c, starts, _ := testClient(t, func() *exec.Cmd {
		if first.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return exec.Command("sleep", "5")
	})
	c.guard.Store(int64(30 * time.Millisecond))
	done := make(chan struct{})
	go func() { c.Render(context.Background(), simple, 80, ansi.GraphemeWidth); close(done) }()
	<-entered
	<-done
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	time.Sleep(20 * time.Millisecond)
	if got := c.Render(context.Background(), simple, 81, ansi.GraphemeWidth); got.Status != Unavailable {
		t.Fatalf("%+v", got)
	}
	close(release)
	<-closed
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	live := c.proc != nil
	c.mu.Unlock()
	if live || starts.Load() != 1 {
		t.Fatalf("worker after Close: live=%v starts=%d", live, starts.Load())
	}
}

// warm starts the worker (startup counts against the guard) so a test of the
// real render deadline is not decided by process start and compile time on a
// loaded machine.
func warm(t *testing.T, c *Client, width int) {
	t.Helper()
	limit := c.guard.Swap(int64(time.Minute))
	defer c.guard.Store(limit)
	if got := c.Render(context.Background(), "flowchart LR\n W[warm] --> U[up]", width, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("warm-up at width %d: %+v", width, got)
	}
}

// Cancellation returns before a blocked write and cannot release the
// slot while the old frame still owns the exchange.
func TestClientCanceledPartialWriteKeepsSlotUntilGuard(t *testing.T) {
	c, _, _ := testClient(t, silentWorker)
	p := warmSilentWorker(t, c, 80)
	synctest.Test(t, func(t *testing.T) {
		input, writer := io.Pipe()
		p.stdin = writer
		t.Cleanup(func() { _ = input.Close() })

		part := make(chan error, 1)
		go func() {
			_, err := io.ReadFull(input, make([]byte, 5))
			part <- err
		}()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan Result, 1)
		start := time.Now()
		go func() { result <- c.Render(ctx, simple, 80, ansi.GraphemeWidth) }()
		if err := <-part; err != nil {
			t.Fatal(err)
		}
		cancel()
		if got := <-result; got.Status != Busy || time.Since(start) != 0 {
			t.Fatalf("cancellation: %+v after %s", got, time.Since(start))
		}
		synctest.Wait()
		if len(c.slot) != 1 {
			t.Fatal("caller cancellation released the blocked slot")
		}
		time.Sleep(time.Duration(c.guard.Load()))
		synctest.Wait()
		if len(c.slot) != 0 {
			t.Fatal("guard did not release the blocked slot")
		}
	})
	requireWorkerKilled(t, c, p)
}

func TestClientCooperativeCancelKeepsWorker(t *testing.T) {
	started, signal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = started.Close(); _ = signal.Close() }()
	c, starts, _ := testClient(t, func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], observedWorkerArgument)
		cmd.ExtraFiles = []*os.File{signal}
		return cmd
	})
	start := time.Now()
	warm(t, c, 80)
	cold := time.Since(start)
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		t.Fatal("no warmed worker")
	}
	_ = signal.Close()
	seen := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(started, make([]byte, 1))
		seen <- err
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dense := denseGraph()
	result := make(chan Result, 1)
	go func() { result <- c.Render(ctx, dense, 80, ansi.GraphemeWidth) }()
	select {
	case err := <-seen:
		if err != nil {
			t.Fatal(err)
		}
	case got := <-result:
		t.Fatalf("render returned before WASM began: %+v", got)
	}
	start = time.Now()
	cancel()
	if got := <-result; got.Status != Busy {
		t.Fatalf("canceled caller received %+v", got)
	}
	caller := time.Since(start)
	if got := c.Render(t.Context(), simple, 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("next draw: %+v", got)
	}
	settled := time.Since(start)
	start = time.Now()
	if got := c.Render(t.Context(), simple, 81, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("warm draw at new width: %+v", got)
	}
	c.mu.Lock()
	same := c.proc == p
	c.mu.Unlock()
	if !same || starts.Load() != 1 {
		t.Fatalf("cancel replaced the worker: same=%v starts=%d", same, starts.Load())
	}
	if _, ok := c.Lookup(dense, 80, ansi.GraphemeWidth); ok {
		t.Fatal("canceled draw was cached")
	}
	t.Logf("cold draw=%s caller cancel=%s cancel plus next draw=%s warm draw=%s", cold, caller, settled, time.Since(start))
}

// Lookup answers from the cache only: a miss before rendering, the drawing
// after, and never a transient outcome.
func TestClientLookupNeverRenders(t *testing.T) {
	c, starts, _ := testClient(t, selfWorker)
	if _, ok := c.Lookup(simple, 80, ansi.GraphemeWidth); ok || starts.Load() != 0 {
		t.Fatal("lookup before any render")
	}
	drawn := c.Render(context.Background(), simple, 80, ansi.GraphemeWidth)
	got, ok := c.Lookup(simple, 80, ansi.GraphemeWidth)
	if !ok || got.Status != OK || strings.Join(got.Lines, "\n") != strings.Join(drawn.Lines, "\n") {
		t.Fatalf("lookup after render: %+v %v", got, ok)
	}
	if _, ok := c.Lookup(simple, 81, ansi.GraphemeWidth); ok {
		t.Fatal("lookup at another width hit")
	}
	busy, _, _ := testClient(t, func() *exec.Cmd { return exec.Command("sh", "-c", "exit 0") })
	if r := busy.Render(context.Background(), simple, 80, ansi.GraphemeWidth); !r.Transient() {
		t.Fatalf("%+v", r)
	}
	if _, ok := busy.Lookup(simple, 80, ansi.GraphemeWidth); ok {
		t.Fatal("a transient outcome was cached")
	}
}
