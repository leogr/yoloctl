package openshell

import (
	"context"
	"errors"
	"os"
)

// PreparedOperatorRegistrationRootAbsence observes ONLY the literal operators
// prefix. In particular it invents no operator incarnation or box identity.
// An actual lifecycle caller must bind a successfully captured disabled CID,
// original box/storage/admission and initial inactivity before using it.
type PreparedOperatorRegistrationRootAbsence struct {
	original *PreparedPublicSSHDirectoryAbsence
}

type OperatorRegistrationRootAbsenceResult struct {
	ObservedRootAbsent bool
	ParentSynced       bool
}

func (*PreparedOperatorRegistrationRootAbsence) String() string {
	return "<original literal operator root absence>"
}
func (p *PreparedOperatorRegistrationRootAbsence) GoString() string { return p.String() }

func PrepareOperatorRegistrationRootAbsence(ctx context.Context, verifyOwned, verifyInactive func(context.Context) error) (_ *PreparedOperatorRegistrationRootAbsence, err error) {
	var p *PreparedOperatorRegistrationRootAbsence
	err = withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		var captureErr error
		p, captureErr = snapshotOperatorRegistrationRootAbsence(ctx, openPublicSSHParent, 0, 0, verifyOwned, verifyInactive, publicSSHKernelObserver(k))
		return captureErr
	})
	if err != nil {
		if p != nil {
			err = errors.Join(err, p.Close())
		}
		return nil, err
	}
	return p, nil
}

// The private fixed-child absence/sync implementation is shared with cold
// SSH. Its child is the literal operators prefix here, never a fabricated
// endpoint. Genuine namespace/boot admission and every ancestor check remain.
func snapshotOperatorRegistrationRootAbsence(ctx context.Context, openBase func() (*os.File, error), uid, gid int, verifyOwned, verifyInactive func(context.Context) error, observeKernel func(context.Context) (publicSSHKernelStamp, error)) (*PreparedOperatorRegistrationRootAbsence, error) {
	p, err := snapshotFixedPublicDirectoryAbsence(ctx, openBase, "operators", uid, gid, verifyOwned, verifyInactive, observeKernel)
	if err != nil {
		return nil, err
	}
	return &PreparedOperatorRegistrationRootAbsence{original: p}, nil
}

// Withdraw is an original Stop/inactive/full-CIDrm/namespace-ACK suffix only.
// current must prove independent complete admission/storage/consumer/listener/
// stream/projection state, without rereading the removed CID. Root absence is
// neither an unlink/history ACK nor permission to skip a present-root roster.
func (p *PreparedOperatorRegistrationRootAbsence) Withdraw(ctx context.Context, current func(context.Context) error) (OperatorRegistrationRootAbsenceResult, error) {
	if p == nil || p.original == nil {
		return OperatorRegistrationRootAbsenceResult{}, errors.New("original operator root observation is unavailable")
	}
	r, err := p.original.Withdraw(ctx, current)
	if err != nil {
		return OperatorRegistrationRootAbsenceResult{}, err
	}
	return OperatorRegistrationRootAbsenceResult{ObservedRootAbsent: r.ObservedDirectoryAbsent, ParentSynced: r.ParentSynced}, nil
}

func (p *PreparedOperatorRegistrationRootAbsence) Close() error {
	if p == nil || p.original == nil {
		return nil
	}
	return p.original.Close()
}
