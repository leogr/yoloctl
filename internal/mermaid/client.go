package mermaid

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

const (
	// exchangeFailSafe bounds one exchange with the worker (its start, the
	// request and the reply) from the moment the request has the worker. The
	// worker itself ends a render at renderDeadline, so this catches only a
	// worker that stopped answering.
	exchangeFailSafe = 2 * time.Minute
	// restartEvery throttles restarting a crashed or killed worker.
	restartEvery = 30 * time.Second
)

// Client runs diagrams in one lazily started worker process. Requests are
// serialized: one waits for the worker as long as its caller does, then its
// exchange is bounded by exchangeFailSafe. It never retries a diagram.
// Methods are safe for concurrent use.
type Client struct {
	// command builds the worker process; nil uses this executable.
	command func() *exec.Cmd
	// hostOnly refuses to start a worker outside the host CLI (a space VM,
	// a controller context or an agent box), like follow.
	hostOnly func() bool
	now      func() time.Time
	guard    atomic.Int64 // nanoseconds; exchangeFailSafe

	slot  chan struct{} // one request in flight
	cache *resultCache

	// generation changes at every Close: a start requested before a Close
	// never creates a worker after it.
	generation atomic.Uint64
	requestID  atomic.Uint64

	mu        sync.Mutex
	proc      *workerProcess
	restarted time.Time // last restart after a crash or kill; zero before any
	crashed   bool      // a worker ended or was killed since the last start
}

// NewClient returns a Client for this executable's hidden worker.
func NewClient() *Client {
	c := &Client{now: time.Now, slot: make(chan struct{}, 1), cache: newResultCache(), hostOnly: onHost}
	c.guard.Store(int64(exchangeFailSafe))
	return c
}

var defaultClient = sync.OnceValue(NewClient)

// Default is the process's single renderer client.
func Default() *Client { return defaultClient() }

type workerProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func (c *Client) workerCommand() *exec.Cmd {
	if c.command != nil {
		return c.command()
	}
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	cmd := exec.Command(executable, WorkerArgument)
	// The worker needs no environment: no paths, no credentials, no config.
	cmd.Env = []string{"LANG=C.UTF-8"}
	return cmd
}

// start launches the worker, or reports why it may not run now.
func (c *Client) startLocked() (*workerProcess, bool) {
	if c.proc != nil {
		return c.proc, true
	}
	if c.hostOnly != nil && !c.hostOnly() {
		return nil, false
	}
	if c.crashed {
		if !c.restarted.IsZero() && c.now().Sub(c.restarted) < restartEvery {
			return nil, false
		}
		c.restarted = c.now()
	}
	cmd := c.workerCommand()
	if cmd == nil {
		c.crashed = true
		return nil, false
	}
	cmd.Stderr = &limitedWriter{n: 4096}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.crashed = true
		return nil, false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		c.crashed = true
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		c.crashed = true
		return nil, false
	}
	c.crashed = false
	c.proc = &workerProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64<<10)}
	return c.proc, true
}

// killLocked ends the worker and its process group.
func (c *Client) killLocked() {
	p := c.proc
	if p == nil {
		return
	}
	c.proc, c.crashed = nil, true
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = p.stdin.Close()
	go func() { _ = p.cmd.Wait() }()
}

// Close ends the worker, and any start requested before it creates none. A
// later Render starts a new one.
func (c *Client) Close() {
	c.generation.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killLocked()
	c.crashed = false
}

// Render returns a verified drawing or a reason to show the source. width is
// the card's inner width; method is the view's width method.
func (c *Client) Render(ctx context.Context, source string, width int, method ansi.Method) Result {
	generation := c.generation.Load()
	name := methodName(method)
	if name == "" || width <= 0 || width > 4096 || !validSource(source) {
		return Result{Status: Internal}
	}
	key := newCacheKey(source, width, name)
	if r, ok := c.cache.get(key); ok {
		return r
	}
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return Result{Status: Busy}
	}
	if r, ok := c.cache.get(key); ok { // filled while this request waited
		<-c.slot
		return r
	}
	if ctx.Err() != nil { // a closed view starts nothing
		<-c.slot
		return Result{Status: Busy}
	}
	req := request{Version: protocol, Artifact: moduleHash(), Width: width, Method: name, Source: source, ID: c.requestID.Add(1)}
	deadline := c.now().Add(time.Duration(c.guard.Load()))
	done := make(chan Result, 1)
	go func() {
		r := c.exchange(ctx, req, deadline, method, generation)
		if ctx.Err() == nil && r.cacheable() {
			c.cache.put(key, r)
		}
		<-c.slot // only the settled exchange releases the slot
		done <- r
	}()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		return Result{Status: Busy}
	}
}

// Lookup returns the cached outcome of a rendering without rendering it:
// it never waits for the worker, so a UI goroutine may call it.
func (c *Client) Lookup(source string, width int, method ansi.Method) (Result, bool) {
	name := methodName(method)
	if name == "" || width <= 0 || width > 4096 || !validSource(source) {
		return Result{}, false
	}
	return c.cache.get(newCacheKey(source, width, name))
}

// OnHost reports whether this process may run the worker: the host CLI, not
// a space VM, controller or agent box.
func OnHost() bool { return onHost() }

// errProtocol marks a reply that arrived but broke the protocol (frame size,
// JSON, version, artifact or drawing checks), as opposed to a lost exchange.
var errProtocol = errors.New("mermaid worker broke the protocol")

// exchange starts the worker if needed, sends one request and reads its
// reply, all before deadline (the exchange's start plus its fail-safe).
// Starting runs in the background: if it outlasts the deadline or ctx, the
// request gives up and a late-started worker still becomes the client's
// worker (owned, reused, ended by Close), never an orphan.
func (c *Client) exchange(ctx context.Context, req request, deadline time.Time, method ansi.Method, generation uint64) Result {
	timer := time.NewTimer(max(deadline.Sub(c.now()), 0))
	defer timer.Stop()
	started := make(chan *workerProcess, 1)
	var abandoned atomic.Bool // set when this request stops waiting
	go func() {
		c.mu.Lock()
		var p *workerProcess
		ok := false
		// A start that has not begun when its request gave up, or that was
		// requested before a Close, never begins. One already running when
		// the request gives up completes and becomes the client's worker.
		if !abandoned.Load() && ctx.Err() == nil && c.generation.Load() == generation {
			p, ok = c.startLocked()
		}
		c.mu.Unlock()
		if !ok {
			p = nil
		}
		started <- p
	}()
	var p *workerProcess
	select {
	case p = <-started:
	case <-timer.C:
		abandoned.Store(true)
		return Result{Status: Unavailable}
	case <-ctx.Done():
		abandoned.Store(true)
		return Result{Status: Busy}
	}
	if p == nil {
		return Result{Status: Unavailable}
	}
	if ctx.Err() != nil {
		return Result{Status: Busy}
	}
	got, gaveUp := workerExchange(ctx, p, req, timer.C)
	if got.err == nil && (got.resp.Version != protocol || got.resp.Artifact != req.Artifact ||
		got.resp.ID != req.ID || !got.resp.Result.valid(req.Width, method) || got.resp.Result.Status == canceled && !gaveUp) {
		got.err = errProtocol
	}
	if got.err != nil {
		// A worker that broke the exchange or the protocol is killed; its
		// stream is no longer in step.
		c.mu.Lock()
		if c.proc == p {
			c.killLocked()
		}
		c.mu.Unlock()
		if errors.Is(got.err, errProtocol) {
			return Result{Status: Internal}
		}
		return Result{Status: Unavailable}
	}
	if gaveUp || ctx.Err() != nil {
		return Result{Status: Busy}
	}
	return got.resp.Result
}

type workerReply struct {
	resp response
	err  error
}

func writeWorkerRequest(p *workerProcess, req request) <-chan error {
	done := make(chan error, 1)
	go func() { done <- writeFrame(p.stdin, req, maxRequest) }()
	return done
}

func readWorkerReply(p *workerProcess) <-chan workerReply {
	done := make(chan workerReply, 1)
	go func() {
		frame, err := readFrame(p.stdout, maxResponse)
		var resp response
		if errors.Is(err, errFrameSize) || err == nil && strictJSON(frame, &resp) != nil {
			err = errProtocol
		}
		done <- workerReply{resp, err}
	}()
	return done
}

// workerExchange serializes writes and drains the terminal reply before the
// slot can pass to another request. A blocked write and a blocked reply are
// both covered by the original exchange guard, even after cancellation.
func workerExchange(ctx context.Context, p *workerProcess, req request, guard <-chan time.Time) (workerReply, bool) {
	write := writeWorkerRequest(p, req)
	var read <-chan workerReply
	var got workerReply
	var sent, gaveUp, cancelSent, replied bool
	caller := ctx.Done()
	for !replied || write != nil {
		select {
		case err := <-write:
			if err != nil {
				return workerReply{err: err}, gaveUp
			}
			write = nil
			if !sent {
				sent = true
				read = readWorkerReply(p)
			}
		case got = <-read:
			if got.err != nil {
				return got, gaveUp
			}
			read, replied = nil, true
		case <-caller:
			caller, gaveUp = nil, true
		case <-guard:
			return workerReply{err: errors.New("renderer did not answer within its guard")}, gaveUp
		}
		if gaveUp && write == nil && !replied && !cancelSent {
			cancelSent = true
			write = writeWorkerRequest(p, request{Version: protocol, Artifact: req.Artifact, ID: req.ID, Cancel: true})
		}
	}
	return got, gaveUp
}

// Transient reports an outcome that is not kept (busy, unavailable, a broken
// exchange): an explicit redraw may try it again.
func (r Result) Transient() bool { return !r.cacheable() }

// cacheable reports the outcomes kept for the session: deterministic ones,
// and (as a no-retry policy, not a fact) timeout and renderer failure.
func (r Result) cacheable() bool {
	switch r.Status {
	case OK, TooWide, TooTall, ParseError, Timeout, Failed:
		return true
	default:
		return false
	}
}

// onHost reports the host CLI context: not a space VM (native controller
// context or its context file), not a controller worker, not an agent box.
func onHost() bool {
	if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
		return false
	}
	if _, sandbox := os.LookupEnv("OPENSHELL_SANDBOX"); sandbox {
		return false
	}
	_, err := os.Lstat(spaceenv.ContextPath)
	return errors.Is(err, os.ErrNotExist)
}

// limitedWriter keeps nothing beyond n bytes of worker diagnostics.
type limitedWriter struct{ n int }

func (l *limitedWriter) Write(p []byte) (int, error) {
	l.n -= min(len(p), max(l.n, 0))
	return len(p), nil
}
