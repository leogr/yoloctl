package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

const (
	EffectWaiting = "waiting"
	EffectStarted = "started"

	checkpointSetup       byte = 1
	checkpointBegin       byte = 2
	checkpointPermit      byte = 3
	checkpointDeny        byte = 4
	checkpointFinish      byte = 5
	checkpointUncertain   byte = 6
	checkpointAttached    byte = 7
	checkpointDetached    byte = 8
	checkpointExpand      byte = 9
	checkpointProof       byte = 10
	checkpointComplete    byte = 11
	checkpointRecorded    byte = 12
	checkpointImage       byte = 13
	checkpointAdvisory    byte = 14
	checkpointNativeNonce byte = 15
	checkpointPIDAnchor   byte = 16
	checkpointRetireOp    byte = 17
)

type effectGateKey struct{}
type effectGate struct {
	once  sync.Once
	begin func(context.Context, EffectIntent) error
	err   error
}

// WithEffectGate installs the trusted worker's effect checkpoint. Public input
// cannot select or supply a gate. Direct service tests must install one explicitly.
func WithEffectGate(ctx context.Context, begin func(context.Context, EffectIntent) error) context.Context {
	return context.WithValue(ctx, effectGateKey{}, &effectGate{begin: begin})
}

// BeginEffects must succeed before an opted-in operation can change native or
// external state. A missing gate is an error, never permission to proceed.
// Without a declared scope the operation's outcome holds the whole space.
func BeginEffects(ctx context.Context) error {
	return BeginScopedEffects(ctx, EffectIntent{})
}

// BeginScopedEffects durably records the footprint and verifier before the
// first effect. Only the first call of an execution declares its intent.
func BeginScopedEffects(ctx context.Context, intent EffectIntent) error {
	gate, ok := ctx.Value(effectGateKey{}).(*effectGate)
	if !ok || gate.begin == nil {
		return errors.New("trusted controller effect checkpoint is unavailable")
	}
	gate.once.Do(func() { gate.err = gate.begin(ctx, intent) })
	return gate.err
}

// EffectRecorder is the trusted worker's evidence channel after permission.
type EffectRecorder interface {
	ExpandEffects(context.Context, []Resource) error
	RecordDocument(context.Context, DocumentIntent) error
	CompleteEffects(context.Context, int) error
}

type effectRecorderKey struct{}

// WithEffectRecorder installs the worker's private evidence channel.
func WithEffectRecorder(ctx context.Context, r EffectRecorder) context.Context {
	return context.WithValue(ctx, effectRecorderKey{}, r)
}

func effectRecorder(ctx context.Context) EffectRecorder {
	r, _ := ctx.Value(effectRecorderKey{}).(EffectRecorder)
	return r
}

// ExpandEffects durably adds written resources before a late effect, such as
// an image build or role publication, may touch them. Outside a controller
// worker there is no receipt to extend.
func ExpandEffects(ctx context.Context, writes ...Resource) error {
	if r := effectRecorder(ctx); r != nil {
		return r.ExpandEffects(ctx, writes)
	}
	return nil
}

// RecordDocument registers the exact postimage of an atomic publication
// before it is written. The controller keeps only a keyed fingerprint.
func RecordDocument(ctx context.Context, d DocumentIntent) error {
	if r := effectRecorder(ctx); r != nil {
		return r.RecordDocument(ctx, d)
	}
	return nil
}

// CompleteEffects records, durably and acknowledged, that every effect and
// mutating cleanup ended with this known result. It is metadata only and
// never re-invokes the operation. Callers must not use it after a panic,
// cancellation or EffectsUncertain, or while a helper can still mutate.
func CompleteEffects(ctx context.Context, status int) error {
	if r := effectRecorder(ctx); r != nil {
		return r.CompleteEffects(ctx, status)
	}
	return nil
}

// WorkerCheckpoints uses only the private inherited descriptor. Neither command
// stdout nor public request frames can authorize effects or complete setup.
// WorkerCheckpoints shares ONE original physical owner with the optional
// genuine WorkerHandoff. A pointer to this transport alone grants no SDK source.
type WorkerCheckpoints struct{ channel *workercheckpoint.Channel }

func NewWorkerCheckpoints(file *os.File) *WorkerCheckpoints {
	return &WorkerCheckpoints{channel: workercheckpoint.Take(file)}
}
func (c *WorkerCheckpoints) SharedChannel() *workercheckpoint.Channel {
	if c == nil {
		return nil
	}
	return c.channel
}
func (c *WorkerCheckpoints) Close() error { return c.SharedChannel().Close() }
func (c *WorkerCheckpoints) Gate()        { c.SharedChannel().Gate() }
func (c *WorkerCheckpoints) BeginEffects(ctx context.Context, intent EffectIntent) error {
	err := c.SharedChannel().Begin(ctx, intent)
	if errors.Is(err, workercheckpoint.ErrRefused) {
		return errors.New("controller did not authorize operation effects")
	}
	return err
}
func (c *WorkerCheckpoints) ExpandEffects(ctx context.Context, writes []Resource) error {
	return c.SharedChannel().Expand(ctx, writes)
}
func (c *WorkerCheckpoints) RecordDocument(ctx context.Context, d DocumentIntent) error {
	return c.SharedChannel().Proof(ctx, d)
}
func (c *WorkerCheckpoints) CompleteEffects(ctx context.Context, status int) error {
	return c.SharedChannel().Complete(ctx, status)
}
func (c *WorkerCheckpoints) Release() error {
	err := c.SharedChannel().Release()
	if errors.Is(err, workercheckpoint.ErrDetached) {
		return ErrClientDetached
	}
	return err
}
func (c *WorkerCheckpoints) Finish() error          { return c.SharedChannel().Finish(false) }
func (c *WorkerCheckpoints) FinishUncertain() error { return c.SharedChannel().Finish(true) }
func checkpointParentError(err error) error {
	if errors.Is(err, workercheckpoint.ErrUncertain) {
		return &EffectsUncertain{Cause: err}
	}
	return err
}

// errUncertainAfterCompletion marks a worker that contradicted its own
// completion checkpoint; finalization then disregards that checkpoint.
var errUncertainAfterCompletion = errors.New("worker reported uncertain effects after completing them")

func decodeStrict(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing checkpoint data")
	}
	return nil
}

// readCheckpointBody reads one framed checkpoint payload strictly.
func readCheckpointBody(file *os.File, dst any) error {
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxFrame {
		return errors.New("invalid worker checkpoint size")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(file, raw); err != nil {
		return err
	}
	if err := decodeStrict(raw, dst); err != nil {
		return errors.New("invalid worker checkpoint")
	}
	return nil
}

func serveWorkerCheckpoints(ctx context.Context, file *os.File, streams Streams) error {
	begun, released, finished, uncertain, completed := false, false, false, false, false
	for {
		var message [1]byte
		if _, err := io.ReadFull(file, message[:]); err != nil {
			// A worker that leaves the setup acknowledgment unread resets the
			// socket when it exits; after its completion checkpoint that is EOF.
			if errors.Is(err, io.EOF) || finished && errors.Is(err, syscall.ECONNRESET) {
				if finished {
					if uncertain {
						return errors.New("worker reported an uncertain effect outcome")
					}
					return nil
				}
				return errors.New("worker ended without a completion checkpoint")
			}
			return err
		}
		if finished {
			return errors.New("worker checkpoint after completion")
		}
		switch message[0] {
		case checkpointAdvisory:
			var notice AdvisoryNotice
			if err := readCheckpointBody(file, &notice); err != nil {
				return err
			}
			if err := serveAdvisory(ctx, file, streams.Advisory, notice); err != nil {
				return err
			}
		case checkpointNativeNonce:
			var request nativeNonceRequest
			if err := readNativeNonceBody(file, &request); err != nil {
				return err
			}
			if !begun || released || completed || streams.NativeNonce == nil {
				return errors.New("invalid original native incarnation checkpoint phase")
			}
			if err := serveNativeNonce(ctx, file, streams.NativeNonce, request); err != nil {
				return err
			}
		case checkpointPIDAnchor:
			var request workerPIDAnchorRequest
			if err := readNativeNonceBody(file, &request); err != nil {
				return err
			}
			if !begun || released || completed || streams.PIDAnchorCheck == nil {
				return errors.New("invalid original worker PID anchor checkpoint phase")
			}
			if err := serveWorkerPIDAnchor(ctx, file, streams.PIDAnchorCheck, request); err != nil {
				return err
			}
		case checkpointRetireOp:
			var request operatorRetireRequest
			if err := readNativeNonceBody(file, &request); err != nil {
				return err
			}
			if !begun || released || completed || streams.OperatorRetire == nil {
				return errors.New("invalid original operator retirement checkpoint phase")
			}
			if err := serveOperatorRetire(ctx, file, streams.OperatorRetire, request); err != nil {
				return err
			}
		case checkpointImage:
			var request ImageRequest
			if err := readCheckpointBody(file, &request); err != nil {
				return err
			}
			if err := serveImageCheckpoint(ctx, file, streams.Image, request); err != nil {
				return err
			}
		case checkpointBegin:
			var intent EffectIntent
			if err := readCheckpointBody(file, &intent); err != nil {
				return err
			}
			if streams.BeginEffects == nil || begun || released || completed {
				return errors.New("invalid worker effect checkpoint")
			}
			begun = true
			err := intent.validate()
			if err == nil {
				err = streams.BeginEffects(ctx, intent)
			}
			reply := checkpointPermit
			if err != nil {
				reply = checkpointDeny
			}
			if _, writeErr := file.Write([]byte{reply}); writeErr != nil {
				return writeErr
			}
			if err != nil {
				return err
			}
		case checkpointSetup:
			if released || (streams.BeginEffects != nil && !begun) {
				return errors.New("invalid worker setup checkpoint")
			}
			released = true
			reply := checkpointAttached
			if streams.Release != nil {
				if err := streams.Release(); errors.Is(err, ErrClientDetached) {
					reply = checkpointDetached
				} else if err != nil {
					return err
				}
			}
			// A worker that exits right after announcing setup cannot read
			// the reply; its exit and finish checkpoint still decide the outcome.
			_, _ = file.Write([]byte{reply})
		case checkpointExpand, checkpointProof, checkpointComplete:
			kind := message[0]
			var writes []Resource
			var document DocumentIntent
			var status int
			var body any = &status
			switch kind {
			case checkpointExpand:
				body = &writes
			case checkpointProof:
				body = &document
			}
			if err := readCheckpointBody(file, body); err != nil {
				return err
			}
			// Evidence follows permission (or an ungated start) and precedes
			// completion; completion is recorded once.
			if completed || streams.BeginEffects != nil && !begun || streams.RecordEvidence == nil {
				return errors.New("invalid worker evidence checkpoint")
			}
			var err error
			switch kind {
			case checkpointExpand:
				err = streams.RecordEvidence.ExpandEffects(ctx, writes)
			case checkpointProof:
				err = streams.RecordEvidence.RecordDocument(ctx, document)
			default:
				if status < 0 || status > 255 {
					return errors.New("invalid worker completion status")
				}
				err = streams.RecordEvidence.CompleteEffects(ctx, status)
				completed = err == nil
			}
			// A refusal is not a protocol fault: the worker must not perform
			// that effect and ends through its ordinary error path.
			reply := checkpointRecorded
			if err != nil {
				reply = checkpointDeny
			}
			if _, writeErr := file.Write([]byte{reply}); writeErr != nil {
				return writeErr
			}
		case checkpointFinish:
			finished = true
		case checkpointUncertain:
			if completed {
				return errUncertainAfterCompletion
			}
			finished, uncertain = true, true
		default:
			return errors.New("unknown worker checkpoint")
		}
	}
}

// WorkerInterrupted records loss of the worker's normal completion path even
// when the client/controller context is still live (for example SIGKILL/OOM).
type WorkerInterrupted struct{ Cause error }

func (e *WorkerInterrupted) Error() string {
	return fmt.Sprintf("controller worker interrupted: %v", e.Cause)
}
func (e *WorkerInterrupted) Unwrap() error { return e.Cause }
