package agentbox

import (
	"context"
	"errors"
	"slices"

	"github.com/leogr/yoloctl/internal/operator"
)

// prepareOperatorRegistrationRemoval is the SDK's original-registration
// observation; tests replace it.
var prepareOperatorRegistrationRemoval = func(ctx context.Context, identity operator.RegistrationIdentity, owned func(context.Context) error) (operatorRegistrationWithdrawal, error) {
	p, err := operator.PrepareRegistrationRemoval(ctx, identity, owned)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("operator registration observation unavailable")
	}
	return p, nil
}

// operatorRegistrationWithdrawal is one retained original registration: a
// one-use withdrawal and its release.
type operatorRegistrationWithdrawal interface {
	Withdraw(context.Context, func(context.Context) error) error
	Close() error
}

// nativeOperatorRegistrationRemoval retains a removed box's original operator
// registrations, observed before any lifecycle effect, and withdraws exactly
// those after the original Stop and fixed-CID rm acknowledgements. It never
// re-observes, adopts a later registration or reports an empty success.
type nativeOperatorRegistrationRemoval struct {
	identities []operator.RegistrationIdentity
	prepared   []operatorRegistrationWithdrawal
	withdrawn  bool
	failed     bool
}

func prepareNativeOperatorRegistrationRemoval(ctx context.Context, identities []operator.RegistrationIdentity, owned func(context.Context) error) (_ *nativeOperatorRegistrationRemoval, err error) {
	if len(identities) == 0 || owned == nil {
		return nil, errors.New("native operator registration removal needs original identities and an owner check")
	}
	r := &nativeOperatorRegistrationRemoval{identities: slices.Clone(identities)}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	for _, identity := range identities {
		p, err := prepareOperatorRegistrationRemoval(ctx, identity, owned)
		if err != nil {
			return nil, err
		}
		r.prepared = append(r.prepared, p)
	}
	return r, nil
}

// retained reports whether found lists only the original registrations not
// yet withdrawn, so checks before the withdrawal can accept them.
func (r *nativeOperatorRegistrationRemoval) retained(found []operator.RegistrationIdentity) bool {
	if r == nil || r.withdrawn || r.failed {
		return false
	}
	for _, f := range found {
		if !slices.Contains(r.identities, f) {
			return false
		}
	}
	return true
}

// withdraw is one use, after the caller's original rm acknowledgement; current
// proves storage and the remaining inventories without these registrations.
func (r *nativeOperatorRegistrationRemoval) withdraw(ctx context.Context, removed bool, current func(context.Context) error) error {
	switch {
	case !removed:
		return errors.New("native operator registration withdrawal lacks the original rm acknowledgement")
	case r.withdrawn || r.failed:
		return errors.New("native operator registration withdrawal replayed")
	}
	for _, p := range r.prepared {
		if err := p.Withdraw(ctx, current); err != nil {
			r.failed = true
			return err
		}
	}
	r.withdrawn = true
	return nil
}

func (r *nativeOperatorRegistrationRemoval) Close() error {
	var result error
	for _, p := range r.prepared {
		result = errors.Join(result, p.Close())
	}
	r.prepared = nil
	return result
}

// liveNativeOperatorIdentity selects the enabled box's current native
// registration from the original inventory, or nil when there is none (for
// example a legacy or missing registration): then there is no controller
// listener to retire and removal continues as on the old path.
func liveNativeOperatorIdentity(identities []operator.RegistrationIdentity, endpoint operator.Endpoint) *operator.RegistrationIdentity {
	for i := range identities {
		if identities[i].Endpoint == endpoint && identities[i].Mode == operator.RegistrationOpenShellNative {
			id := identities[i]
			return &id
		}
	}
	return nil
}
