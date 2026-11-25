package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
)

const nativeNonceFrameLimit = 4096

type NativeNonceCall func(context.Context, string) (string, error)
type nativeNonceRequest struct {
	Box string `json:"box"`
}
type nativeNonceReply struct {
	Incarnation string `json:"incarnation,omitempty"`
	Error       string `json:"error,omitempty"`
	Uncertain   bool   `json:"uncertain,omitempty"`
}

func canonicalNativeNonce(data []byte, dst any) error {
	if len(data) == 0 || len(data) > nativeNonceFrameLimit {
		return errors.New("invalid private native incarnation frame size")
	}
	if err := decodeStrict(data, dst); err != nil {
		return err
	}
	raw, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(raw, data) {
		return errors.New("noncanonical or aliased private native incarnation frame")
	}
	return nil
}
func readNativeNonceBody(file *os.File, dst any) error {
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > nativeNonceFrameLimit {
		return errors.New("invalid private native incarnation body size")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(file, raw); err != nil {
		return err
	}
	return canonicalNativeNonce(raw, dst)
}
func writeNativeNonceBody(file io.Writer, reply nativeNonceReply) error {
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if len(raw) > nativeNonceFrameLimit {
		return errors.New("native incarnation reply exceeds bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	return writeAll(file, append(header[:], raw...))
}
func validNativeIncarnation(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == value
}

// NativeIncarnation uses only this original worker's inherited checkpoint FD.
// It returns a public identity, NOT a credential, reservation/creation ACK or
// publication capability. Missing/failed transport never falls back to a local
// nonce. Native SDK/actual publication wiring is a separate caller obligation.
func (c *WorkerCheckpoints) NativeIncarnation(ctx context.Context, box string) (string, error) {
	value, err := c.SharedChannel().NativeNonce(ctx, box)
	return value, checkpointParentError(err)
}
func serveNativeNonce(ctx context.Context, file io.Writer, call NativeNonceCall, r nativeNonceRequest) error {
	reply := nativeNonceReply{}
	if call == nil || layout.ValidateName("agentbox", r.Box) != nil {
		reply.Error = "native incarnation allocator unavailable or invalid box"
	} else {
		value, err := call(ctx, r.Box)
		if err != nil {
			reply.Error = err.Error()
			var uncertain *EffectsUncertain
			reply.Uncertain = errors.As(err, &uncertain)
		} else if !validNativeIncarnation(value) {
			reply.Error = "parent returned invalid native incarnation"
		} else {
			reply.Incarnation = value
		}
	}
	return writeNativeNonceBody(file, reply)
}

// nativeNonceOwner belongs to ONE admitted Execute lifetime. The producer is
// installed only in that execution's Streams, delivered to its actual RunWorker
// private descriptor. Public frames/stdout/UID or copied nonce strings do not
// acquire this map. This component owns only allocation metadata; no publication
// or fresh reservation is inferred from it. Future parent prepare must MATCH
// this original box/incarnation BEFORE observing the worker's origin envelope.
type nativeNonceOwner struct {
	handoffLifetime sync.RWMutex // expires only outside an original source borrow
	server          *Server
	// Borrow the SAME immutable original source for transfer and every
	// checkpoint. Controller lifetime owns Close; no later lookup/adoption.
	pidAnchor                 WorkerPIDAnchor
	receipt                   *Receipt
	ctx                       context.Context
	hello                     Hello
	diskID, requestID, method string
	principal                 Principal
	mu                        sync.Mutex
	active                    bool
	allocated                 map[string]string
}

func (s *Server) newNativeNonceOwner(ctx context.Context, p Principal, req Request, r *Receipt, op Operation) *nativeNonceOwner {
	if ctx == nil || r == nil || !op.Mutates || !op.EffectGated {
		return nil
	}
	return &nativeNonceOwner{server: s, pidAnchor: s.PIDAnchor, receipt: r, ctx: ctx, hello: s.Hello, diskID: s.DiskID, requestID: req.ID, method: req.Method, principal: p, active: true, allocated: map[string]string{}}
}
func (o *nativeNonceOwner) close() {
	if o == nil {
		return
	}
	o.handoffLifetime.Lock()
	defer o.handoffLifetime.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active = false
	clear(o.allocated)
}

// checkReceipt runs under Server.mu. Server -> owner is the only nested lock
// order. Storage verification happens outside both, and no worker/drain wait or
// publisher effect is performed while either mutex is held.
func (o *nativeNonceOwner) checkReceipt(ctx context.Context, box string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	s, r := o.server, o.receipt
	if s.Hello != o.hello || s.DiskID == "" || s.DiskID != o.diskID || r == nil || r.ID != o.requestID || r.Method != o.method || r.Principal != o.principal || r.Generation != o.hello.Generation || r.DiskID != o.diskID || r.State != "pending" || r.EffectPhase != EffectStarted || r.SetupComplete || r.Completion != nil || r.ProofVersion < documentProofVersion || r.ProofVersion > ProofVersion || r.Scope == nil || r.Scope.validate() != nil {
		return errors.New("native incarnation lacks original active scoped receipt permission")
	}
	if !r.Scope.Global && !containsResource(r.Scope.Writes, Resource{Kind: ResourceBox, Name: box}) {
		return errors.New("native incarnation box is outside original write scope")
	}
	if s.nativeStorageFailure != nil {
		return &EffectsUncertain{Cause: s.nativeStorageFailure}
	}
	entry := s.executions[r.ID]
	if entry == nil || entry.receipt != r || entry.settled || !sameAdmission(*entry.receipt, *r) {
		return errors.New("native incarnation original live execution is unavailable")
	}
	// Durable bytes are diagnostic only; permission remains bound to this
	// original worker, principal, generation, scope and nonce lifetime.
	if !entry.durableWarned {
		saved, err := s.Receipts.Read(r.ID)
		expected := *r
		expected.RecordedLate = saved.RecordedLate
		if err != nil || !sameReceipt(saved, expected) {
			logging.L().Warn("Live operation record is unavailable or older than its execution", "actor", logging.Controller, "request", r.ID, "method", r.Method)
			entry.durableWarned = true
		}
	}
	current := s.loadHolds(r.ID)
	if resources, held, _ := current.conflict(&Scope{Writes: []Resource{{Kind: ResourceBox, Name: box}}}); len(held) != 0 {
		return heldError(s.Hello.Space, o.principal.Admin(), resources, held)
	}
	return nil
}

// live checks expiration before any storage callback and again at commit.
// Its mutex is released before acquiring Server.mu: nested order stays server
// then owner. A copied/closed map cannot trigger another original operation.
func (o *nativeNonceOwner) live(ctx context.Context, box, incarnation string) error {
	if o == nil || o.ctx == nil {
		return errors.New("native incarnation original execution unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.allocated == nil || incarnation != "" && o.allocated[incarnation] != box {
		return errors.New("native incarnation original execution expired or does not own identity")
	}
	return nil
}

func nativeNonceCancellationOnly(err error) bool {
	budget := 32
	var only func(error, int) bool
	only = func(err error, depth int) bool {
		budget--
		if err == nil || depth > 8 || budget < 0 {
			return false
		}
		if err == context.Canceled || err == context.DeadlineExceeded {
			return true
		}
		if mixed, ok := err.(interface{ Unwrap() []error }); ok {
			children := mixed.Unwrap()
			if len(children) == 0 {
				return false
			}
			for _, child := range children {
				if !only(child, depth+1) {
					return false
				}
			}
			return true
		}
		if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			return only(wrapped.Unwrap(), depth+1)
		}
		return false
	}
	return only(err, 0)
}

// Loss of actual storage verification closes admission; it is not a known
// no-effect completion inferred from metadata. Pure cancellation of this exact
// already-canceled execution is distinct from failed/mixed storage evidence.
func (o *nativeNonceOwner) verifyStorage(ctx context.Context) error {
	if o.server.VerifyStorage == nil {
		return errors.New("native incarnation parent storage verifier unavailable")
	}
	err := o.server.VerifyStorage(ctx)
	if err == nil {
		return nil
	}
	if (ctx.Err() != nil || o.ctx.Err() != nil) && nativeNonceCancellationOnly(err) {
		return err
	}
	o.server.mu.Lock()
	o.server.noteNativeStorageFailure(err)
	o.server.mu.Unlock()
	return &EffectsUncertain{Cause: err}
}

func (o *nativeNonceOwner) allocate(ctx context.Context, box string) (string, error) {
	if o == nil || o.server == nil || layout.ValidateName("agentbox", box) != nil {
		return "", errors.New("native incarnation original owner unavailable or invalid box")
	}
	if err := o.live(ctx, box, ""); err != nil {
		return "", err
	}
	if err := o.verifyStorage(ctx); err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	value := hex.EncodeToString(nonce[:])
	s := o.server
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.checkReceipt(ctx, box); err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.allocated == nil || o.ctx.Err() != nil {
		return "", errors.New("native incarnation original execution expired")
	}
	if _, exists := o.allocated[value]; exists {
		return "", errors.New("native incarnation collision; no retry or adoption")
	}
	o.allocated[value] = box
	return value, nil
}

// match is a read-only original-call check for the future parent prepare
// stage. It must accompany actual receipt/storage/kernel/origin checks; it
// is not permission to publish. Foreign/copied values and closed owners refuse.
func (o *nativeNonceOwner) match(ctx context.Context, box, incarnation string) error {
	if o == nil || o.server == nil || !validNativeIncarnation(incarnation) || layout.ValidateName("agentbox", box) != nil {
		return errors.New("invalid original native incarnation lookup")
	}
	if err := o.live(ctx, box, incarnation); err != nil {
		return err
	}
	if err := o.verifyStorage(ctx); err != nil {
		return err
	}
	s := o.server
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.checkReceipt(ctx, box); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.allocated == nil || o.allocated[incarnation] != box {
		return errors.New("native incarnation does not belong to original execution and box")
	}
	return nil
}
