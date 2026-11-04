// Package workercheckpoint owns one physical original private worker stream.
// The constructor grants NO source/SDK/receipt permission. A genuine closed
// vmanchor.WorkerHandoff must bind this SAME owner before SDK source use.
package workercheckpoint

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
)

const MaxFrame = 4 << 20
const NativeFrameLimit = 4096
const (
	setup     byte = 1
	begin     byte = 2
	permit    byte = 3
	deny      byte = 4
	finish    byte = 5
	uncertain byte = 6
	attached  byte = 7
	detached  byte = 8
	expand    byte = 9
	proof     byte = 10
	complete  byte = 11
	recorded  byte = 12
	image     byte = 13
	advisory  byte = 14
	nonce     byte = 15
	pidAnchor byte = 16
	retireOp  byte = 17
)

var ErrRefused = errors.New("controller refused the operation checkpoint")
var ErrDetached = errors.New("controller client detached")
var ErrUncertain = errors.New("original parent reported uncertain effects")

// localCancellationError marks only admission/metadata cancellation that did
// not own an active protocol exchange. It is not exported or constructed by
// protocol decoding. Classification is OUTERMOST-only: a caller-supplied
// cancellation cause containing an older marker cannot disguise active loss.
type localCancellationError struct{ cause error }

func (e *localCancellationError) Error() string { return e.cause.Error() }
func (e *localCancellationError) Unwrap() error { return e.cause }
func localCancellation(err error) error         { return &localCancellationError{err} }

// IsLocalCancellation reports a canceled local request with no active protocol
// exchange. Active cancellation closes the physical owner and returns false,
// even when its context cause contains an older local-cancellation error.
func IsLocalCancellation(err error) bool {
	_, ok := err.(*localCancellationError)
	return ok
}

type ParentRefusal struct {
	Message   string
	Uncertain bool
}

func (e *ParentRefusal) Error() string { return e.Message }
func (e *ParentRefusal) Unwrap() error {
	if e.Uncertain {
		return ErrUncertain
	}
	return nil
}

// Identity is copied PRIMARY kernel metadata, never a constructor/source grant.
type Identity struct {
	FD               int
	Device, Inode    uint64
	Mode             uint32
	PeerPID          int32
	PeerUID, PeerGID uint32
}
type fileIdentity struct {
	device, inode uint64
	mode          uint32
}
type Channel struct {
	file              *os.File // SOLE original ownership, never returned/wrapped/duplicated
	base              fileIdentity
	gate              chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
	mu                sync.Mutex // ONLY fatal/close result; never held across gate/I/O
	failure, closeErr error
	// phaseMu is NEVER held across gate admission, I/O, context callbacks or Close.
	// It permits stable no-write Complete idempotence after cancellation/Close,
	// while beginInFlight prevents treating an unresolved Begin as no permission.
	phaseMu                                sync.Mutex
	gated, begun, completed, beginInFlight bool // phaseMu protected
	released, finished                     bool // gate protected
	releaseOnce                            sync.Once
	releaseErr                             error
}

func (*Channel) String() string {
	return "original private worker checkpoint stream [not source admission]"
}
func (c *Channel) GoString() string { return c.String() }

// Take accepts ownership of THIS File object, never a numeric FD/path/backend.
// Ordinary transport callers alone gain no VM/SDK authority. Actual CLI calls
// this once with TakeCheckpoint's original File, then passes SharedChannel to
// the genuine handoff binder. No further raw File wrapper/Close owner remains.
func Take(file *os.File) *Channel {
	c := &Channel{file: file, gate: make(chan struct{}, 1), done: make(chan struct{})}
	c.gate <- struct{}{}
	if file == nil {
		c.failure = errors.New("original checkpoint file unavailable")
		_ = c.Close()
		return c
	}
	id, err := c.initialize()
	if err != nil {
		c.failure = err
		_ = c.Close()
		return c
	}
	c.base = id
	return c
}
func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("checkpoint context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}
func (c *Channel) closedError() error {
	if c == nil {
		return os.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(os.ErrClosed, c.failure, c.closeErr)
}
func (c *Channel) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, contextError(ctx) // unavailable input is NOT cancellation
	}
	if err := contextError(ctx); err != nil {
		return nil, localCancellation(err)
	}
	if c == nil || c.gate == nil || c.done == nil {
		return nil, os.ErrClosed
	}
	// A canceled WAITER never closes/touches the current active exchange.
	select {
	case <-ctx.Done():
		return nil, localCancellation(contextError(ctx))
	case <-c.done:
		return nil, c.closedError()
	case <-c.gate:
	}
	release := func() { c.gate <- struct{}{} }
	if err := contextError(ctx); err != nil {
		release()
		return nil, localCancellation(err)
	}
	select {
	case <-c.done:
		release()
		return nil, c.closedError()
	default:
	}
	return release, nil
}
func (c *Channel) poison(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if c.failure == nil {
		c.failure = err
	}
	c.mu.Unlock()
	_ = c.Close()
}
func (c *Channel) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		var err error
		if c.file != nil {
			err = c.file.Close()
		}
		c.mu.Lock()
		c.closeErr = err
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// exchange is INTERNAL ONLY. Public methods are closed protocol operations;
// no caller-supplied I/O, decoder, nil-success check or backend is exposed.
func (c *Channel) exchange(ctx context.Context, call func() (bool, error)) error {
	release, err := c.acquire(ctx)
	if err != nil {
		return err
	}
	return c.exchangeOwned(ctx, release, call)
}

// Called only with the sole physical token. Complete may first inspect its
// settled no-write state, before installing ANY active-cancellation callback.
func (c *Channel) exchangeOwned(ctx context.Context, release func(), call func() (bool, error)) error {
	defer release()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.Close(); close(closed) })
	fatal, err := call()
	if !stop() {
		<-closed
	}
	if cause := contextError(ctx); cause != nil {
		err = errors.Join(err, cause)
		fatal = true
	}
	if fatal {
		c.poison(err)
	}
	return err
}
func (c *Channel) write(ctx context.Context, b []byte) error {
	for len(b) > 0 {
		n, err := c.writePart(ctx, b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (c *Channel) read(ctx context.Context, b []byte) error {
	for len(b) > 0 {
		n, err := c.readPart(ctx, b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		b = b[n:]
	}
	return nil
}
func (c *Channel) send(ctx context.Context, kind byte, body any, limit int) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return errors.New("checkpoint exceeds size limit")
	}
	var head [5]byte
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(raw)))
	return c.write(ctx, append(head[:], raw...))
}
func (c *Channel) receiveJSON(ctx context.Context, dst any, limit int, canonical bool) error {
	var head [4]byte
	if err := c.read(ctx, head[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n == 0 || n > uint32(limit) {
		return errors.New("invalid private checkpoint body size")
	}
	raw := make([]byte, int(n))
	if err := c.read(ctx, raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid private checkpoint body")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing private checkpoint body")
	}
	if canonical {
		encoded, err := json.Marshal(dst)
		if err != nil || !bytes.Equal(encoded, raw) {
			return errors.New("noncanonical/aliased private checkpoint body")
		}
	}
	return nil
}
func (c *Channel) verdict(ctx context.Context, kind byte, body any, accept byte) (bool, error) {
	// Marshal/size failure is pre-write; it cannot desynchronize the stream.
	raw, err := json.Marshal(body)
	if err != nil {
		return false, err
	}
	if len(raw) > MaxFrame {
		return false, errors.New("checkpoint exceeds size limit")
	}
	var head [5]byte
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(raw)))
	if err := c.write(ctx, append(head[:], raw...)); err != nil {
		return true, err
	}
	var reply [1]byte
	if err := c.read(ctx, reply[:]); err != nil {
		return true, err
	}
	if reply[0] != accept {
		return false, ErrRefused
	}
	return false, nil
}
func (c *Channel) Gate() {
	if c == nil {
		return
	}
	c.phaseMu.Lock()
	c.gated = true
	c.phaseMu.Unlock()
}
func (c *Channel) Begin(ctx context.Context, intent any) error {
	return c.exchange(ctx, func() (bool, error) {
		c.phaseMu.Lock()
		c.beginInFlight = true
		c.phaseMu.Unlock()
		// Begin is linearized as in-flight BEFORE writing any request bytes. No
		// phase lock remains held during the actual parent exchange.
		fatal, err := c.verdict(ctx, begin, intent, permit)
		c.phaseMu.Lock()
		if err == nil {
			c.begun = true
		}
		c.beginInFlight = false
		c.phaseMu.Unlock()
		return fatal, err
	})
}
func (c *Channel) effectOpen() bool {
	c.phaseMu.Lock()
	defer c.phaseMu.Unlock()
	return c.begun && !c.completed
}
func (c *Channel) Expand(ctx context.Context, writes any) error {
	return c.exchange(ctx, func() (bool, error) {
		if !c.effectOpen() {
			return false, errors.New("operation scope can grow only between permission and completion")
		}
		return c.verdict(ctx, expand, writes, recorded)
	})
}
func (c *Channel) Proof(ctx context.Context, document any) error {
	return c.exchange(ctx, func() (bool, error) {
		if !c.effectOpen() {
			return false, errors.New("document proofs are recorded only between permission and completion")
		}
		return c.verdict(ctx, proof, document, recorded)
	})
}
func (c *Channel) completeNoWrite() bool {
	if c == nil {
		return false
	}
	c.phaseMu.Lock()
	defer c.phaseMu.Unlock()
	return c.completed || c.gated && !c.begun && !c.beginInFlight
}
func (c *Channel) Complete(ctx context.Context, status int) error {
	// Match original CompleteEffects: known settled no-write cases remain nil,
	// including already canceled context or closed descriptor. This takes no IO
	// token and cannot shortcut an actual unresolved Begin.
	if c.completeNoWrite() {
		return nil
	}
	release, err := c.acquire(ctx)
	if err != nil {
		if c.completeNoWrite() {
			return nil
		}
		return err
	}
	// State may have settled while waiting. Do not install a cancellation
	// callback or touch the descriptor for this no-write path.
	if c.completeNoWrite() {
		release()
		return nil
	}
	return c.exchangeOwned(ctx, release, func() (bool, error) {
		if status < 0 || status > 255 {
			status = 255
		}
		fatal, err := c.verdict(ctx, complete, status, recorded)
		if err == nil {
			c.phaseMu.Lock()
			c.completed = true
			c.phaseMu.Unlock()
		}
		return fatal, err
	})
}
func (c *Channel) Release() error {
	if c == nil {
		return os.ErrClosed
	}
	c.releaseOnce.Do(func() {
		c.releaseErr = c.exchange(context.Background(), func() (bool, error) {
			c.released = true
			if err := c.write(context.Background(), []byte{setup}); err != nil {
				return true, err
			}
			var b [1]byte
			if err := c.read(context.Background(), b[:]); err != nil {
				return true, err
			}
			switch b[0] {
			case attached:
				return false, nil
			case detached:
				return false, ErrDetached
			default:
				return false, errors.New("controller did not acknowledge setup completion")
			}
		})
	})
	return c.releaseErr
}
func (c *Channel) Finish(isUncertain bool) error {
	return c.exchange(context.Background(), func() (bool, error) {
		kind := finish
		if isUncertain {
			kind = uncertain
		}
		err := c.write(context.Background(), []byte{kind})
		if err == nil {
			c.finished = true
		}
		return err != nil, err
	})
}

// Image/Advisory consume the entire bounded frame before releasing ownership.
// Typed controller decoding is OUTSIDE the channel gate: callers supply no
// decoder/callback and cannot leave a reply tail to impersonate opcode16.
// receivedJSON latches bytes ONLY after the entire bounded frame arrived.
// A final cancellation/Close does not erase that already observed parent
// outcome. Partial payload bytes are never exposed as a completed reply.
type receivedJSON struct{ body []byte }

func (r *receivedJSON) read(c *Channel, ctx context.Context, limit int) error {
	r.body = nil
	var length [4]byte
	if err := c.read(ctx, length[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(length[:])
	if n == 0 || n > uint32(limit) {
		return errors.New("invalid worker checkpoint size")
	}
	body := make([]byte, int(n))
	if err := c.read(ctx, body); err != nil {
		return err
	}
	r.body = body
	return nil
}
func (r *receivedJSON) outcome(err error) ([]byte, error) {
	if r.body == nil && err == nil {
		err = errors.New("complete private reply unavailable")
	}
	return r.body, err
}
func (c *Channel) jsonRequest(ctx context.Context, kind byte, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxFrame {
		return nil, errors.New("checkpoint exceeds size limit")
	}
	var received receivedJSON
	result := c.exchange(ctx, func() (bool, error) {
		var head [5]byte
		head[0] = kind
		binary.BigEndian.PutUint32(head[1:], uint32(len(raw)))
		if err := c.write(ctx, append(head[:], raw...)); err != nil {
			return true, err
		}
		err := received.read(c, ctx, MaxFrame)
		return err != nil, err
	})
	return received.outcome(result)
}
func (c *Channel) Image(ctx context.Context, body any) ([]byte, error) {
	return c.jsonRequest(ctx, image, body)
}
func (c *Channel) Advisory(ctx context.Context, body any) ([]byte, error) {
	return c.jsonRequest(ctx, advisory, body)
}

type boxRequest struct {
	Box string `json:"box"`
}
type nonceReply struct {
	Incarnation string `json:"incarnation,omitempty"`
	Error       string `json:"error,omitempty"`
	Uncertain   bool   `json:"uncertain,omitempty"`
}
type anchorReply struct {
	Error     string `json:"error,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

func (c *Channel) liveEffect() error {
	c.phaseMu.Lock()
	defer c.phaseMu.Unlock()
	if !c.gated || !c.begun || c.completed || c.released || c.finished {
		return errors.New("original live effect-gated worker phase unavailable")
	}
	return nil
}
func (c *Channel) NativeNonce(ctx context.Context, box string) (value string, result error) {
	result = c.exchange(ctx, func() (bool, error) {
		if err := c.liveEffect(); err != nil {
			return false, err
		}
		if layout.ValidateName("agentbox", box) != nil {
			return false, errors.New("invalid native incarnation box")
		}
		if err := c.send(ctx, nonce, boxRequest{box}, NativeFrameLimit); err != nil {
			return true, err
		}
		var reply nonceReply
		if err := c.receiveJSON(ctx, &reply, NativeFrameLimit, true); err != nil {
			return true, err
		}
		if reply.Error != "" {
			if reply.Incarnation != "" {
				return true, errors.New("native incarnation error contains contradictory identity")
			}
			return false, &ParentRefusal{reply.Error, reply.Uncertain}
		}
		raw, err := hex.DecodeString(reply.Incarnation)
		if err != nil || len(raw) != 16 || hex.EncodeToString(raw) != reply.Incarnation || reply.Uncertain {
			return true, errors.New("invalid private native incarnation reply")
		}
		value = reply.Incarnation
		return false, nil
	})
	if result != nil {
		return "", result
	}
	return value, nil
}

// CheckPIDAnchor is the ONLY public opcode16 operation. It always emits the
// real request and consumes the bounded canonical reply on THIS physical file.
// No injected success callback/alternate transport can stand in for this call.
func (c *Channel) CheckPIDAnchor(ctx context.Context, box string) error {
	return c.exchange(ctx, func() (bool, error) {
		if err := c.liveEffect(); err != nil {
			return false, err
		}
		if layout.ValidateName("agentbox", box) != nil {
			return false, errors.New("invalid original anchor box")
		}
		if err := c.send(ctx, pidAnchor, boxRequest{box}, NativeFrameLimit); err != nil {
			return true, err
		}
		var reply anchorReply
		if err := c.receiveJSON(ctx, &reply, NativeFrameLimit, true); err != nil {
			return true, err
		}
		if reply.Uncertain && reply.Error == "" {
			return true, errors.New("contradictory parent PID anchor reply")
		}
		if reply.Error != "" {
			return false, &ParentRefusal{reply.Error, reply.Uncertain}
		}
		return false, nil
	})
}
func (c *Channel) Observe(ctx context.Context) (out Identity, result error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	// No protocol bytes, public file getter, alternate owner or wait callback.
	out, err = c.observeOriginal()
	if err != nil {
		return Identity{}, err
	}
	if err := contextError(ctx); err != nil {
		return Identity{}, localCancellation(err)
	}
	return out, nil
}

type retireRequest struct {
	Box         string `json:"box"`
	Incarnation string `json:"incarnation"`
}

// RetireOperator asks the original parent execution to retire one native
// operator listener of box. It always emits the real request and consumes the
// bounded canonical reply on THIS physical file; a refusal is not an effect.
func (c *Channel) RetireOperator(ctx context.Context, box, incarnation string) error {
	return c.exchange(ctx, func() (bool, error) {
		if err := c.liveEffect(); err != nil {
			return false, err
		}
		raw, err := hex.DecodeString(incarnation)
		if layout.ValidateName("agentbox", box) != nil || err != nil || len(raw) != 16 || hex.EncodeToString(raw) != incarnation {
			return false, errors.New("invalid native operator retirement target")
		}
		if err := c.send(ctx, retireOp, retireRequest{box, incarnation}, NativeFrameLimit); err != nil {
			return true, err
		}
		var reply anchorReply
		if err := c.receiveJSON(ctx, &reply, NativeFrameLimit, true); err != nil {
			return true, err
		}
		if reply.Uncertain && reply.Error == "" {
			return true, errors.New("contradictory parent operator retirement reply")
		}
		if reply.Error != "" {
			return false, &ParentRefusal{reply.Error, reply.Uncertain}
		}
		return false, nil
	})
}
