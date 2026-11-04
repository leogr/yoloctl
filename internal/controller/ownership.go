package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrClientDetached acknowledges a completed setup whose client already left,
// and refuses effects once a signal has detached a waiting client. The worker
// ends without streaming; a started session keeps running.
var ErrClientDetached = errors.New("client detached before the attachment started")

// ClientDetached is a client's outcome when a signal ended it before its setup
// handed off. Owned reports that the authorized setup continues without the
// client; otherwise nothing was changed. It is never an uncertain outcome.
type ClientDetached struct {
	ID     string
	Signal int
	Owned  bool
}

func (e *ClientDetached) Error() string {
	if e.Owned {
		return fmt.Sprintf("detached on %s before the attachment started; request %s keeps starting the session without this client", SignalName(e.Signal), e.ID)
	}
	return fmt.Sprintf("detached on %s before request %s changed anything", SignalName(e.Signal), e.ID)
}

// SignalName names the signals a client may forward.
func SignalName(sig int) string {
	if name := unix.SignalName(syscall.Signal(sig)); name != "" {
		return name
	}
	return fmt.Sprintf("signal %d", sig)
}

// retainedOutputLimit bounds the output a detached setup keeps in memory.
const retainedOutputLimit = 256 << 10

const (
	setupWaiting = iota
	setupOwned
	setupFinished
)

// setupOwner separates one request's client lifetime from its execution.
// Its mutex serializes the effect gate, the setup handoff and detachment:
// before effects are authorized a vanishing client cancels the execution;
// once an OwnedSetup operation is authorized the controller finishes setup
// on its own evidence and the client only detaches its transport. Before
// handoff a client signal ends the client through the same detachment, never
// the setup.
type setupOwner struct {
	policy     bool
	conn       context.Context
	cancelExec context.CancelFunc
	output     *clientOutput
	released   chan struct{} // closed when held client input may reach the worker

	mu           sync.Mutex
	state        int
	detached     bool // the client left before handoff
	detachSignal int  // the signal that detached it, if any
}

func newSetupOwner(policy bool, conn context.Context, cancelExec context.CancelFunc, output *clientOutput) *setupOwner {
	o := &setupOwner{policy: policy, conn: conn, cancelExec: cancelExec, output: output, released: make(chan struct{})}
	if !policy {
		close(o.released)
	}
	return o
}

// detach runs once the public connection ends. Only an owned setup survives.
func (o *setupOwner) detach() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.detached = o.detached || o.state != setupFinished
	o.output.detach()
	if !o.policy || o.state != setupOwned {
		o.cancelExec()
	}
}

// authorize implements the gate order: refuse a departed client, take
// ownership, then durably save the started phase. A detach that wins the
// mutex first denies effects; one that comes later finds the setup owned.
func (o *setupOwner) authorize(exec context.Context, save func() error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.detached {
		return ErrClientDetached
	}
	if err := o.conn.Err(); err != nil {
		return err
	}
	if err := exec.Err(); err != nil {
		return err
	}
	if o.policy {
		o.state = setupOwned
	}
	if err := save(); err != nil {
		o.state = setupWaiting
		return err
	}
	return nil
}

// handoff records durable setup completion and reports whether an owned
// setup's client is still attached. Held input is released only to it.
// Other operations keep streaming until the departed client cancels them.
func (o *setupOwner) handoff(save func() error) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := save(); err != nil {
		return false, err
	}
	if !o.policy || o.state == setupFinished {
		return true, nil
	}
	o.state = setupFinished
	if o.detached || o.conn.Err() != nil {
		o.detached = true
		return false, nil
	}
	close(o.released)
	return true, nil
}

// signal reports whether a client signal frame may reach the worker. Before
// handoff it never does: the client is marked detached, so a waiting setup is
// refused effects, and owned reports that an authorized setup continues. The
// caller then ends the connection, which runs the ordinary detachment.
func (o *setupOwner) signal(sig int) (forward, owned bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.policy || o.state == setupFinished {
		return true, false
	}
	o.detached = true
	if o.detachSignal == 0 {
		o.detachSignal = sig
	}
	return false, o.state == setupOwned
}

// outcome reports client facts recorded on an owned setup's receipt.
func (o *setupOwner) outcome() (detached bool, signal int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.policy && o.detached, o.detachSignal
}

// clientOutput is shared by every worker output stream of one request.
type clientOutput struct {
	onDetach func()
	detached atomic.Bool

	mu       sync.Mutex
	retained []byte
	start    int
	accepted int64
	dropped  int64
}

func (c *clientOutput) detach() {
	if c.detached.CompareAndSwap(false, true) && c.onDetach != nil {
		c.onDetach()
	}
}

func (c *clientOutput) count(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accepted += int64(n)
}

// keep retains the newest output of a detached request in a bounded ring.
func (c *clientOutput) keep(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accepted += int64(len(p))
	if room := retainedOutputLimit - len(c.retained); room > 0 {
		n := min(room, len(p))
		c.retained = append(c.retained, p[:n]...)
		p = p[n:]
	}
	c.dropped += int64(len(p))
	for len(p) > 0 {
		n := copy(c.retained[c.start:], p)
		c.start = (c.start + n) % retainedOutputLimit
		p = p[n:]
	}
}

// clientSink is the only writer a worker's output reaches. It never returns
// an error: os/exec closes a child's pipe on the first failed write, which
// would turn a vanished client into EPIPE/SIGPIPE inside the worker. A failed
// client write detaches the client; later output is retained and dropped, so
// the worker neither blocks nor fails.
type clientSink struct {
	w      io.Writer
	shared *clientOutput
}

func (s clientSink) Write(p []byte) (int, error) {
	total := len(p)
	if !s.shared.detached.Load() {
		n, err := s.w.Write(p)
		s.shared.count(n)
		if err == nil {
			return total, nil
		}
		s.shared.detach()
		p = p[n:]
	}
	s.shared.keep(p)
	return total, nil
}
