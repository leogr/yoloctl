package controller

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/operator"
)

// This controller-lifetime table serializes only competing publisher claims.
// It is not disk/origin admission, a listener/receipt ACK, a retirement lease,
// or restart recovery. Failed and native claims remain occupied: owner loss
// must not let the generic scanner reinterpret native preparation as legacy.
type operatorPublicationClaims struct {
	mu      sync.Mutex
	entries map[string]*operatorPublicationClaim
}

type operatorPublicationClaim struct {
	table    *operatorPublicationClaims
	endpoint operator.Endpoint
	native   *nativeNonceOwner
	// scanned marks a native registration the controller's scanner serves
	// (leogr's relaxed decision); like native, it is never released.
	scanned bool
	// Set once by the native publisher; retiring is sticky so a retired
	// claim is never released and this controller never republishes it.
	listener   operator.NativeListenerOwner
	retiring   bool
	retirement operator.NativeRetirement
}

func (p *operatorPublicationClaims) reserve(endpoint operator.Endpoint, native *nativeNonceOwner) (*operatorPublicationClaim, error) {
	if err := operator.LegacyRegistrationIdentity(endpoint).Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old := p.entries[endpoint.Incarnation]; old != nil {
		if old.native != nil || old.scanned {
			return nil, operator.ErrNativeRegistration
		}
		return nil, errors.New("operator incarnation already has its original publisher")
	}
	if len(p.entries) >= 1024 {
		return nil, errors.New("operator publication claim inventory exceeds bound")
	}
	if p.entries == nil {
		p.entries = make(map[string]*operatorPublicationClaim)
	}
	c := &operatorPublicationClaim{table: p, endpoint: endpoint, native: native}
	p.entries[endpoint.Incarnation] = c
	return c, nil
}

// ListenLegacyOperator claims before calling the existing legacy creator.
// No table/server lock spans Listen, Accept, Close or a worker/handler wait.
// The caller must still strictly decode/revalidate the legacy registration and
// hold its ordinary admission; a supplied Endpoint alone grants no authority.
// Generic Listen's unlink policy is unchanged. Native publication must use its
// separately qualified retained-listener path, never this wrapper.
func (s *Server) ListenLegacyOperator(ctx context.Context, endpoint operator.Endpoint, create func() (net.Listener, error)) (net.Listener, error) {
	if s == nil || create == nil {
		return nil, errors.New("legacy operator publisher unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := s.operatorPublications.reserve(endpoint, nil)
	if err != nil {
		return nil, err
	}
	l, err := create()
	if err != nil || l == nil {
		// Preserve the existing scanner's disappeared-directory branch. This
		// releases only our unused legacy claim, not a listener/effect ACK;
		// any partial handle or other failure retains the claim.
		if l == nil && errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(err, c.releaseLegacy())
		}
		// A partial actual handle has one Close owner. Neither later absence
		// nor successful Close erases the original creation failure/claim.
		if l != nil {
			err = errors.Join(err, l.Close())
		}
		return nil, errors.Join(err, errors.New("legacy operator listener creation failed"))
	}
	return &claimedLegacyOperatorListener{Listener: l, claim: c}, nil
}

type claimedLegacyOperatorListener struct {
	net.Listener
	claim *operatorPublicationClaim
	once  sync.Once
	err   error
}

func (l *claimedLegacyOperatorListener) Close() error {
	l.once.Do(func() {
		l.err = l.Listener.Close() // exact original FD operation, outside locks
		if l.err != nil {
			return // sticky failed Close; no release/retry from current state
		}
		c := l.claim
		l.err = c.releaseLegacy()
	})
	return l.err
}

func (c *operatorPublicationClaim) releaseLegacy() error {
	c.table.mu.Lock()
	defer c.table.mu.Unlock()
	if c.native != nil || c.table.entries[c.endpoint.Incarnation] != c {
		return errors.New("original legacy operator publisher claim changed")
	}
	delete(c.table.entries, c.endpoint.Incarnation)
	return nil
}

// reserveOperatorPublication is deliberately private and not yet wired to a
// publication opcode. It joins the actual original allocator/receipt before
// scanner exclusion. It does NOT validate an origin envelope, publish files,
// create a native listener or authorize any request/removal. Those independent
// proofs and durable publication/retirement transitions remain required.
func (o *nativeNonceOwner) reserveOperatorPublication(ctx context.Context, endpoint operator.Endpoint) (*operatorPublicationClaim, error) {
	if o == nil || o.server == nil {
		return nil, errors.New("original native publisher execution unavailable")
	}
	if err := o.match(ctx, endpoint.Box, endpoint.Incarnation); err != nil {
		return nil, err
	}
	s := o.server
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.checkReceipt(ctx, endpoint.Box); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.ctx.Err() != nil || o.allocated[endpoint.Incarnation] != endpoint.Box {
		return nil, errors.New("original native publisher allocation expired")
	}
	// Lock order: server -> original owner -> claim table. Generic publication
	// takes only the table, and never calls back while holding it.
	return s.operatorPublications.reserve(endpoint, o)
}
