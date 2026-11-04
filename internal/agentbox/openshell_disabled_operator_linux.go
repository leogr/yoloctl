package agentbox

import (
	"context"
	"errors"
	"sync"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

type nativeOperatorRootLease struct {
	withdraw func(context.Context, func(context.Context) error) (openshell.OperatorRegistrationRootAbsenceResult, error)
	close    func() error
}

// nativeDisabledOperatorAbsence retains a genuine successful native metadata
// observation. It has no Client, so the post-rm suffix cannot query the removed
// CID. Actual owner/ACK/complete remaining consumers still belong to the caller.
type nativeDisabledOperatorAbsence struct {
	mu                sync.Mutex
	original          *container.NativeOperatorProjection
	cid, box          string
	lease             *nativeOperatorRootLease
	attempted, closed bool
}

func (*nativeDisabledOperatorAbsence) String() string {
	return "<original disabled-CID operator root absence>"
}
func (p *nativeDisabledOperatorAbsence) GoString() string { return p.String() }

func disabledOperatorSelection(original *container.NativeOperatorProjection, cid, box string) error {
	s, err := original.Selection() // nil/zero/uncaptured refuses in the actual producer
	if err != nil {
		return err
	}
	if s.ContainerID != cid || s.Box != box || s.Enabled || s.Endpoint.Box != "" || s.Endpoint.Incarnation != "" || s.Mount.Source != "" || s.Mount.Destination != "" {
		return errors.New("native operator absence lacks original matching disabled-CID observation")
	}
	return nil
}

func prepareNativeDisabledOperatorAbsenceWith(ctx context.Context, original *container.NativeOperatorProjection, cid, box string, verified, inactive func(context.Context) error, prepare func(context.Context, func(context.Context) error, func(context.Context) error) (*nativeOperatorRootLease, error)) (*nativeDisabledOperatorAbsence, error) {
	if verified == nil || inactive == nil || prepare == nil {
		return nil, errors.New("disabled-CID operator preparation requires actual owner/inactivity/root checks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := disabledOperatorSelection(original, cid, box); err != nil {
		return nil, err
	}
	boundOwned := func(c context.Context) error {
		if err := disabledOperatorSelection(original, cid, box); err != nil {
			return err
		}
		return verified(c)
	}
	if err := boundOwned(ctx); err != nil {
		return nil, err
	}
	if err := inactive(ctx); err != nil {
		return nil, err
	}
	lease, err := prepare(ctx, boundOwned, inactive)
	if err != nil || lease == nil || lease.withdraw == nil || lease.close == nil {
		if lease != nil && lease.close != nil {
			err = errors.Join(err, lease.close())
		}
		return nil, errors.Join(err, errors.New("disabled-CID operator root preparation did not complete"))
	}
	return &nativeDisabledOperatorAbsence{original: original, cid: cid, box: box, lease: lease}, nil
}

func (p *nativeDisabledOperatorAbsence) withdraw(ctx context.Context, remaining func(context.Context) error) error {
	if p == nil {
		return errors.New("original disabled-CID operator absence is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.attempted {
		return errors.New("disabled-CID operator absence is closed or consumed")
	}
	p.attempted = true // every failure consumes BEFORE metadata/current checks
	if p.lease == nil || p.lease.withdraw == nil || remaining == nil {
		return errors.New("disabled-CID operator suffix lacks original root/current proofs")
	}
	current := func(c context.Context) error {
		if err := disabledOperatorSelection(p.original, p.cid, p.box); err != nil {
			return err
		}
		return remaining(c)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := current(ctx); err != nil {
		return err
	}
	r, err := p.lease.withdraw(ctx, current)
	if err != nil {
		return err
	}
	if !r.ObservedRootAbsent || !r.ParentSynced {
		return errors.New("disabled-CID operator suffix lacks complete observed absence/fresh sync")
	}
	return nil
}

func (p *nativeDisabledOperatorAbsence) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.lease != nil && p.lease.close != nil {
		return p.lease.close()
	}
	return nil
}
