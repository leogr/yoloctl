package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"github.com/leogr/yoloctl/internal/layout"
)

type workerPIDAnchorRequest struct {
	Box string `json:"box"`
}
type workerPIDAnchorReply struct {
	Error     string `json:"error,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

// CheckPIDAnchor observes only this ORIGINAL parent's current launch/receipt
// join. It transfers no namespace, identity or authority over public frames.
// Loss/refusal closes proof, never supplies empty census or replays a mutation.
func (c *WorkerCheckpoints) CheckPIDAnchor(ctx context.Context, box string) error {
	return checkpointParentError(c.SharedChannel().CheckPIDAnchor(ctx, box))
}
func serveWorkerPIDAnchor(ctx context.Context, file io.Writer, call func(context.Context, string) error, request workerPIDAnchorRequest) error {
	reply := workerPIDAnchorReply{}
	if call == nil || layout.ValidateName("agentbox", request.Box) != nil {
		reply.Error = "original parent PID anchor checker unavailable or invalid box"
	} else if err := call(ctx, request.Box); err != nil {
		reply.Error = err.Error()
		var uncertain *EffectsUncertain
		reply.Uncertain = errors.As(err, &uncertain)
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if len(raw) > nativeNonceFrameLimit {
		return errors.New("parent PID anchor reply exceeds bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	return writeAll(file, append(header[:], raw...))
}

// Context-aware original source adapters retain the original request while
// queued. Existing non-context fixture sources keep their Check fallback;
// cancellation after either call still cannot become a healthy reply.
func checkWorkerPIDAnchor(ctx context.Context, source WorkerPIDAnchor) error {
	if ctx == nil || source == nil {
		return errors.New("original context/source unavailable")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	var err error
	if contextual, ok := source.(interface{ CheckContext(context.Context) error }); ok {
		err = contextual.CheckContext(ctx)
	} else {
		err = source.Check()
	}
	return errors.Join(err, ctx.Err(), context.Cause(ctx))
}

// The execution owner is minted only by actual ServeConn/Execute. No allocation
// nonce is needed for a read-only namespace check, but original receipt, box
// write scope, admission and current storage remain mandatory. Kernel/source
// checks happen OUTSIDE server/owner locks and are bracketed by original checks.
func (o *nativeNonceOwner) checkPIDAnchor(ctx context.Context, box string) error {
	if o == nil || o.server == nil || layout.ValidateName("agentbox", box) != nil {
		return errors.New("original worker PID anchor owner unavailable")
	}
	if err := o.live(ctx, box, ""); err != nil {
		return err
	}
	if err := o.verifyStorage(ctx); err != nil {
		return err
	}
	s := o.server
	s.mu.Lock()
	err := o.checkReceipt(ctx, box)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if o.pidAnchor == nil {
		return errors.New("original controller launch PID anchor unavailable")
	}
	if err := checkWorkerPIDAnchor(ctx, o.pidAnchor); err != nil {
		return err
	}
	if err := o.live(ctx, box, ""); err != nil {
		return err
	}
	if err := o.verifyStorage(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.checkReceipt(ctx, box); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.ctx.Err() != nil {
		return errors.New("original worker PID anchor execution expired")
	}
	return ctx.Err()
}
