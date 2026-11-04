package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/operator"
)

// OperatorRetireCall is the original effect-gated execution's private request
// to retire one native operator listener before the box's Stop.
type OperatorRetireCall func(ctx context.Context, box, incarnation string) error

// attachNativeListener binds the native publisher's started listener to its
// original native claim. Only that claim may later be retired; a legacy claim
// or a replaced/retiring entry refuses.
func (c *operatorPublicationClaim) attachNativeListener(l operator.NativeListenerOwner) error {
	if c == nil || c.table == nil || (c.native == nil && !c.scanned) || l == nil {
		return errors.New("native operator listener requires its original native claim")
	}
	id := l.Identity()
	if id.Mode != operator.RegistrationOpenShellNative || id.Endpoint != c.endpoint || id.Validate() != nil {
		return errors.New("native operator listener identity differs from its claim")
	}
	c.table.mu.Lock()
	defer c.table.mu.Unlock()
	if c.table.entries[c.endpoint.Incarnation] != c || c.listener != nil || c.retiring {
		return errors.New("native operator claim changed or already has a listener")
	}
	c.listener = l
	return nil
}

// retireOperatorListener runs for the original removal execution only: its
// live scoped receipt must cover box. It marks the claim retiring (sticky, so
// no republication in this controller lifetime) BEFORE the listener Close, then
// settles the listener, its pending Accept and every other accepted request.
// When this execution targets its own box, self is its exact connection: it is
// exempted and selfRetired lets ServeConn tell the expected self-retirement
// from an unrelated revocation. Any failure after marking is uncertain.
func (o *nativeNonceOwner) retireOperatorListener(ctx context.Context, box, incarnation string, self net.Conn, selfRetired func()) error {
	if o == nil || o.server == nil || o.ctx == nil {
		return errors.New("original native removal execution unavailable")
	}
	if layout.ValidateName("agentbox", box) != nil || !validNativeIncarnation(incarnation) {
		return errors.New("invalid native operator retirement target")
	}
	s := o.server
	s.mu.Lock()
	err := o.checkReceipt(ctx, box)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	o.mu.Lock()
	active := o.active && o.ctx.Err() == nil
	o.mu.Unlock()
	if !active {
		return errors.New("original native removal execution expired")
	}
	t := &s.operatorPublications
	t.mu.Lock()
	c := t.entries[incarnation]
	if c == nil {
		// No claim: this controller never published or is publishing this
		// incarnation (a stopped box, or after a controller restart), and an
		// earlier controller's listener ended with its process. Occupy the
		// table with a retired native entry so no later publication of the
		// incarnation can start; there is no listener Close to acknowledge.
		if len(t.entries) >= 1024 {
			t.mu.Unlock()
			return errors.New("operator publication claim inventory exceeds bound")
		}
		if t.entries == nil {
			t.entries = make(map[string]*operatorPublicationClaim)
		}
		t.entries[incarnation] = &operatorPublicationClaim{table: t, endpoint: operator.Endpoint{Box: box, Incarnation: incarnation}, native: o, retiring: true}
		t.mu.Unlock()
		return nil
	}
	if (c.native == nil && !c.scanned) || c.endpoint.Box != box || c.listener == nil || c.retiring {
		// A legacy, foreign, still-publishing (no listener yet) or already
		// retiring claim is never settled from here.
		t.mu.Unlock()
		return errors.New("native operator listener is not in a retirable state in this controller")
	}
	c.retiring = true
	l := c.listener
	t.mu.Unlock()
	selfTarget := o.principal.Box == box && o.principal.Incarnation == incarnation
	var exempt net.Conn
	if selfTarget {
		exempt = self
	}
	r, err := l.RetireListener(ctx, exempt)
	if err != nil {
		return &EffectsUncertain{Cause: err}
	}
	t.mu.Lock()
	c.retirement = r
	t.mu.Unlock()
	if selfTarget && selfRetired != nil {
		selfRetired()
	}
	return nil
}

type operatorRetireRequest struct {
	Box         string `json:"box"`
	Incarnation string `json:"incarnation"`
}
type operatorRetireReply struct {
	Error     string `json:"error,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

func serveOperatorRetire(ctx context.Context, file io.Writer, call OperatorRetireCall, r operatorRetireRequest) error {
	reply := operatorRetireReply{}
	if err := call(ctx, r.Box, r.Incarnation); err != nil {
		reply.Error = err.Error()
		var uncertain *EffectsUncertain
		reply.Uncertain = errors.As(err, &uncertain)
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if len(raw) > nativeNonceFrameLimit {
		return errors.New("operator retirement reply exceeds bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	return writeAll(file, append(header[:], raw...))
}

// RetireOperatorListener uses only this original worker's inherited
// checkpoint FD; there is no local fallback.
func (c *WorkerCheckpoints) RetireOperatorListener(ctx context.Context, box, incarnation string) error {
	return checkpointParentError(c.SharedChannel().RetireOperator(ctx, box, incarnation))
}

// ErrNativeOperatorPublicationHeld marks a refusal that ends once the host
// acknowledges the holding outcome; the scanner retries it.
var ErrNativeOperatorPublicationHeld = errors.New("native operator publication is held by an unresolved operation")

// CheckNativeOperatorPublication refuses to (re)publish box's native endpoint
// while an unresolved receipt holds box or the space, for example a removal
// interrupted after its listener retirement. The native scanner calls
// it before every publication, including stale-socket restoration at startup.
// Surviving identity/client files alone never authorize republication.
func (s *Server) CheckNativeOperatorPublication(box string) error {
	if s == nil || layout.ValidateName("agentbox", box) != nil {
		return errors.New("invalid native operator publication target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receiptFailure != nil {
		return errors.Join(errors.New("native operator publication requires healthy receipt storage"), s.receiptFailure)
	}
	current, err := s.loadHolds("")
	if err != nil {
		return err
	}
	// Only a finished unresolved outcome holds publication. A running
	// execution, such as the create that is waiting for this very socket,
	// never does.
	resources, held, global := current.conflict(&Scope{Writes: []Resource{{Kind: ResourceBox, Name: box}}})
	var unresolved []Receipt
	for _, r := range held {
		if r.unresolved() {
			unresolved = append(unresolved, r)
		}
	}
	if len(unresolved) != 0 {
		return errors.Join(ErrNativeOperatorPublicationHeld, heldError(s.Hello.Space, true, global, resources, unresolved))
	}
	return nil
}
