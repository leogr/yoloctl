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
	// renderGuard bounds one request from the moment it is enqueued: queue
	// wait plus the worker's own 2 s render deadline.
	renderGuard = 4 * time.Second
	// restartEvery throttles restarting a crashed or killed worker.
	restartEvery = 30 * time.Second
)

// Client runs diagrams in one lazily started worker process. Requests are
// serialized; each is bounded by renderGuard from enqueue. It never retries a
// diagram. Methods are safe for concurrent use.
type Client struct {
	// command builds the worker process; nil uses this executable.
	command func() *exec.Cmd
	// hostOnly refuses to start a worker outside the host CLI (a space VM,
	// a controller context or an agent box), like follow.
	hostOnly func() bool
	now      func() time.Time
	guard    atomic.Int64 // nanoseconds; renderGuard

	slot  chan struct{} // one request in flight
	cache *resultCache

	// generation changes at every Close: a start requested before a Close
	// never creates a worker after it.
	generation atomic.Uint64

	mu        sync.Mutex
	proc      *workerProcess
	restarted time.Time // last restart after a crash or kill; zero before any
	crashed   bool      // a worker ended or was killed since the last start
}

// NewClient returns a Client for this executable's hidden worker.
func NewClient() *Client {
	c := &Client{now: time.Now, slot: make(chan struct{}, 1), cache: newResultCache(), hostOnly: onHost}
	c.guard.Store(int64(renderGuard))
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
	name := methodName(method)
	if name == "" || width <= 0 || width > 4096 || !validSource(source) {
		return Result{Status: Internal}
	}
	key := newCacheKey(source, width, name)
	if r, ok := c.cache.get(key); ok {
		return r
	}
	enqueued, limit := c.now(), time.Duration(c.guard.Load())
	guard := time.NewTimer(limit)
	defer guard.Stop()
	select {
	case c.slot <- struct{}{}:
	case <-guard.C:
		return Result{Status: Busy}
	case <-ctx.Done():
		return Result{Status: Busy}
	}
	defer func() { <-c.slot }()
	if r, ok := c.cache.get(key); ok { // filled while this request waited
		return r
	}
	r := c.exchange(ctx, request{protocol, moduleHash(), width, name, source}, enqueued.Add(limit), method)
	if r.cacheable() {
		c.cache.put(key, r)
	}
	return r
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
// reply, all before deadline (the request's enqueue time plus its guard).
// Starting runs in the background: if it outlasts the deadline or ctx, the
// request gives up and a late-started worker still becomes the client's
// worker (owned, reused, ended by Close), never an orphan.
func (c *Client) exchange(ctx context.Context, req request, deadline time.Time, method ansi.Method) Result {
	timer := time.NewTimer(max(deadline.Sub(c.now()), 0))
	defer timer.Stop()
	started := make(chan *workerProcess, 1)
	generation := c.generation.Load()
	var abandoned atomic.Bool // set when this request stops waiting
	go func() {
		c.mu.Lock()
		var p *workerProcess
		ok := false
		// A start that has not begun when its request gave up, or that was
		// requested before a Close, never begins. One already running when
		// the request gives up completes and becomes the client's worker.
		if !abandoned.Load() && c.generation.Load() == generation {
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
	type reply struct {
		resp response
		err  error
	}
	done := make(chan reply, 1)
	go func() {
		if err := writeFrame(p.stdin, req, maxRequest); err != nil {
			done <- reply{err: err}
			return
		}
		frame, err := readFrame(p.stdout, maxResponse)
		var resp response
		switch {
		case errors.Is(err, errFrameSize):
			err = errProtocol
		case err == nil && strictJSON(frame, &resp) != nil:
			err = errProtocol
		}
		done <- reply{resp, err}
	}()
	var got reply
	canceled := false
	select {
	case got = <-done:
	case <-timer.C:
		got.err = errors.New("renderer did not answer within its guard")
	case <-ctx.Done():
		got.err, canceled = ctx.Err(), true
	}
	if got.err == nil && (got.resp.Version != protocol || got.resp.Artifact != req.Artifact || !got.resp.Result.valid(req.Width, method)) {
		got.err = errProtocol
	}
	if got.err != nil {
		// A worker that broke the exchange or the protocol is killed; its
		// stream is no longer in step.
		c.mu.Lock()
		if c.proc == p {
			c.killLocked()
			if canceled {
				// The caller gave up (a view's time budget, a close): not
				// a worker failure, so no restart throttle follows.
				c.crashed = false
			}
		}
		c.mu.Unlock()
		if canceled {
			return Result{Status: Busy}
		}
		if errors.Is(got.err, errProtocol) {
			return Result{Status: Internal}
		}
		return Result{Status: Unavailable}
	}
	return got.resp.Result
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
