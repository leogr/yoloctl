package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/leogr/yoloctl/internal/operator"
)

// Primary controller-lifetime bookkeeping ONLY, not registration/disk/native
// admission, original publisher custody, listener status, receipt or ACK. An
// expired/failed native owner remains a native claim; it is never called live
// or silently omitted. Empty claims cannot establish complete native absence.
type operatorPublicationObservation struct {
	endpoint operator.Endpoint
	native   bool
}
type operatorPublicationClaimSnapshot struct {
	claim    *operatorPublicationClaim
	endpoint operator.Endpoint
	native   *nativeNonceOwner
}
type operatorPublicationInventory struct {
	mu           sync.Mutex
	lifetime     context.Context
	table        *operatorPublicationClaims // SAME original table; no alternate lookup
	claims       map[string]operatorPublicationClaimSnapshot
	observations []operatorPublicationObservation
	failure      error
}

func (*operatorPublicationInventory) String() string {
	return "operator publication bookkeeping [not authority]"
}
func (p *operatorPublicationInventory) GoString() string { return p.String() }

// Private passive component; the future original parent/classifier must supply
// admitted storage/receipt/context and independently retain registration/runtime
// and native metadata observations through its aggregate decision. No worker
// opcode, public read, mutation or classifier is installed here.
func observeOperatorPublicationClaims(ctx context.Context, s *Server) (*operatorPublicationInventory, error) {
	if ctx == nil || s == nil {
		return nil, errors.New("operator claim inventory lacks original context/server")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &operatorPublicationInventory{lifetime: ctx, table: &s.operatorPublications, claims: make(map[string]operatorPublicationClaimSnapshot)}
	p.table.mu.Lock()
	defer p.table.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(p.table.entries) > 1024 {
		return nil, errors.New("operator claim inventory exceeds bound")
	}
	for key, c := range p.table.entries {
		if err := validateObservedOperatorClaim(p.table, key, c); err != nil {
			return nil, err
		}
		p.claims[key] = operatorPublicationClaimSnapshot{c, c.endpoint, c.native}
		p.observations = append(p.observations, operatorPublicationObservation{c.endpoint, c.native != nil})
	}
	slices.SortFunc(p.observations, func(a, b operatorPublicationObservation) int {
		return strings.Compare(a.endpoint.Incarnation, b.endpoint.Incarnation)
	})
	return p, ctx.Err()
}
func validateObservedOperatorClaim(table *operatorPublicationClaims, key string, c *operatorPublicationClaim) error {
	if c == nil || c.table != table || c.endpoint.Incarnation != key {
		return errors.New("operator claim inventory contains partial/foreign bookkeeping")
	}
	return operator.LegacyRegistrationIdentity(c.endpoint).Validate()
}

// Original claims are immutable after reserve. Actual release and a later
// same-key reserve change pointer identity; matching endpoint strings cannot
// repair that loss. No table lock spans external callbacks or listener I/O.
func (p *operatorPublicationInventory) check(ctx context.Context) error {
	if p == nil || ctx == nil || p.lifetime == nil || p.table == nil || p.claims == nil {
		return errors.New("operator claim inventory unavailable")
	}
	if err := p.lifetime.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.table.mu.Lock()
	defer p.table.mu.Unlock()
	if len(p.table.entries) != len(p.claims) || len(p.table.entries) > 1024 {
		return errors.New("operator publisher roster changed")
	}
	for key, c := range p.table.entries {
		if err := validateObservedOperatorClaim(p.table, key, c); err != nil {
			return err
		}
		original := p.claims[key]
		if original.claim != c || original.endpoint != c.endpoint || original.native != c.native {
			return errors.New("original operator publisher claim changed")
		}
	}
	// Cancellation can race a table-lock wait; never replace original lifetime.
	return errors.Join(p.lifetime.Err(), ctx.Err())
}
func (p *operatorPublicationInventory) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil operator claim inventory")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	p.failure = p.check(ctx)
	return p.failure
}
func (p *operatorPublicationInventory) Observation(ctx context.Context) ([]operatorPublicationObservation, error) {
	if p == nil {
		return nil, errors.New("nil operator claim inventory")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return nil, p.failure
	}
	if err := p.check(ctx); err != nil {
		p.failure = err
		return nil, err
	}
	return slices.Clone(p.observations), nil
}
