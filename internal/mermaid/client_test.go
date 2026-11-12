package mermaid

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The test binary is its own worker when started with the worker argument.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == WorkerArgument {
		os.Exit(WorkerMain())
	}
	os.Exit(m.Run())
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

// At the real limits a compute-bound diagram times out; the timeout is kept
// for the session (no-retry policy), and the worker keeps serving.
func TestClientTimeoutIsNotRetried(t *testing.T) {
	skipUnderRace(t)
	c, _, _ := testClient(t, selfWorker)
	ctx := context.Background()
	warm(t, c) // the guard includes startup; measure the render deadline
	start := time.Now()
	if got := c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth); got.Status != Timeout {
		t.Fatalf("%+v", got)
	}
	if took := time.Since(start); took > renderGuard {
		t.Fatalf("took %s", took)
	}
	start = time.Now()
	if got := c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth); got.Status != Timeout || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("not cached: %+v", got)
	}
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("%+v", got)
	}
}

// A request still queued at its guard is dropped as busy and not cached.
func TestClientQueuedRequestIsBusyAtItsGuard(t *testing.T) {
	skipUnderRace(t)
	c, _, _ := testClient(t, selfWorker)
	ctx := context.Background()
	warm(t, c) // the guard includes startup; measure the render deadline
	slow := make(chan Result, 1)
	go func() { slow <- c.Render(ctx, denseGraph(), 80, ansi.GraphemeWidth) }()
	for len(c.slot) == 0 {
		time.Sleep(time.Millisecond)
	}
	c.guard.Store(int64(100 * time.Millisecond))
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != Busy {
		t.Fatalf("%+v", got)
	}
	if got := <-slow; got.Status != Timeout {
		t.Fatalf("%+v", got)
	}
	c.guard.Store(int64(testGuard))
	if got := c.Render(ctx, simple, 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("busy was cached: %+v", got)
	}
}

// A worker that does not answer within the guard is killed; a restart is
// allowed at most once per 30 s, otherwise the renderer is unavailable.
func TestClientKillsAHungWorkerAndThrottlesRestarts(t *testing.T) {
	c, starts, clk := testClient(t, func() *exec.Cmd { return exec.Command("sleep", "60") })
	c.guard.Store(int64(200 * time.Millisecond))
	ctx := context.Background()
	for i, want := range []int32{1, 2, 2} {
		if got := c.Render(ctx, simple, 80+i, ansi.GraphemeWidth); got.Status != Unavailable {
			t.Fatalf("render %d: %+v", i, got)
		}
		if n := starts.Load(); n != want {
			t.Fatalf("render %d: %d starts, want %d", i, n, want)
		}
	}
	clk.add(restartEvery)
	if got := c.Render(ctx, simple, 90, ansi.GraphemeWidth); got.Status != Unavailable || starts.Load() != 3 {
		t.Fatalf("no restart after the throttle: %+v starts=%d", got, starts.Load())
	}
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
	if err := writeFrame(&frame, request{protocol, moduleHash(), 80, "grapheme", simple}, maxRequest); err != nil {
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
func warm(t *testing.T, c *Client) {
	t.Helper()
	limit := c.guard.Swap(int64(time.Minute))
	defer c.guard.Store(limit)
	if got := c.Render(context.Background(), "flowchart LR\n W[warm] --> U[up]", 80, ansi.GraphemeWidth); got.Status != OK {
		t.Fatalf("warm-up: %+v", got)
	}
}

// A caller that gives up while the worker renders (follow's refresh budget)
// gets busy at once; the worker is replaced, and that is not a crash: the
// next requests start a worker without the restart throttle.
func TestClientCancelDuringRenderIsBusyAndNotThrottled(t *testing.T) {
	c, starts, _ := testClient(t, func() *exec.Cmd {
		return exec.Command("sh", "-c", "cat >/dev/null & sleep 5")
	})
	for n := range 3 {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(30 * time.Millisecond); cancel() }()
		start := time.Now()
		got := c.Render(ctx, simple, 80+n, ansi.GraphemeWidth)
		if got.Status != Busy || time.Since(start) > time.Second {
			t.Fatalf("request %d: %+v after %s", n, got, time.Since(start))
		}
		if int(starts.Load()) != n+1 {
			t.Fatalf("request %d: %d starts; a canceled render throttled the restart", n, starts.Load())
		}
	}
}
