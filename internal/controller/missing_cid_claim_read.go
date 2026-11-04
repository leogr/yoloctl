package controller

import (
	"context"
	"errors"
	"sync"

	"github.com/leogr/yoloctl/internal/operator"
)

// Copied bookkeeping only. Native remains true after its original owner's
// failure/expiry; no endpoint identity, claim count or bytes are an ACK.
type MissingCIDClaimObservation struct {
	Endpoint operator.Endpoint
	Native   bool
}
type MissingCIDClaimRead struct {
	mu       sync.Mutex
	original *operatorPublicationInventory //SAME private original table snapshot
	owner    *nativeNonceOwner             //BORROW actual original admitted Execute; no new factory
	lifetime context.Context
	box      string
	failure  error
}

func (r *MissingCIDClaimRead) Observation(ctx context.Context, box string) ([]MissingCIDClaimObservation, error) {
	if r == nil {
		return nil, errors.New("nil original claim read")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil {
		return nil, r.failure
	}
	fail := func(err error) ([]MissingCIDClaimObservation, error) { r.failure = err; return nil, err }
	if ctx == nil || r.lifetime == nil || r.original == nil || r.owner == nil || box != r.box {
		return fail(errors.New("claim observation lacks SAME original parent/box"))
	}
	if err := errors.Join(r.lifetime.Err(), ctx.Err()); err != nil {
		return fail(err)
	}
	if err := r.owner.live(ctx, box, ""); err != nil {
		return fail(err)
	}
	rows, err := r.original.Observation(ctx)
	if err != nil {
		return fail(err)
	}
	var result []MissingCIDClaimObservation
	for _, row := range rows {
		if row.endpoint.Box == box {
			result = append(result, MissingCIDClaimObservation{row.endpoint, row.native})
		}
	}
	if err := r.owner.live(ctx, box, ""); err != nil {
		return fail(err)
	}
	return result, nil
}
