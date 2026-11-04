package openshell

import (
	"context"
	"errors"
	"os"
)

// nativeContainerOwnership is a PRIVATE pre-Stop view, deliberately distinct
// from the public complete inventory. It captures every immutable runtime
// record/anchor and stable forward lock, without opening volatile observation
// files of a still-live runtime. It grants no admission/process/kernel/ACK or
// deletion authority. Skipped files require complete mount/schema validation
// after original Stop/inactivity; actual pre-effect kernel checks are separate.
type nativeContainerOwnership struct{ view *NativeContainerInventory }

func (s *nativeContainerOwnership) Close() {
	if s == nil {
		return
	}
	if s.view != nil {
		_ = s.view.Close()
		s.view = nil
	}
}
func (s *nativeContainerOwnership) records() []RuntimeRecord {
	if s == nil || s.view == nil {
		return nil
	}
	return s.view.Records()
}
func captureNativeContainerOwnershipAt(ctx context.Context, root *os.File, selected RuntimeRecord, release QualifiedRelease, owners runtimeOwners) (*nativeContainerOwnership, error) {
	view, err := readNativeContainerInventoryAt(ctx, root, selected, release, owners, true)
	if err != nil {
		return nil, err
	}
	s := &nativeContainerOwnership{view: view}
	if err := s.check(ctx, release); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *nativeContainerOwnership) check(ctx context.Context, release QualifiedRelease) error {
	if s == nil || s.view == nil {
		return errors.New("native ownership view is closed")
	}
	current, err := s.view.namedRoot(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	next, err := readNativeContainerInventoryAt(ctx, current, s.view.current, release, s.view.owners, true)
	if err != nil {
		return err
	}
	defer func() { _ = next.Close() }()
	return compareRuntimeInventories(s.view, next)
}

// A live original protocol token is mandatory to cross from immutable capture
// to a FULL role/mode/no-follow/mount-checked post-Stop inventory. The result is
// still observation, and metadata deletion additionally requires its distinct
// original RemoveID ACK and actual remaining-consumer/storage proof.
func (s *nativeContainerOwnership) afterStop(ctx context.Context, release QualifiedRelease, cid string, ack *nativeContainerStopACK) (*NativeContainerInventory, error) {
	if s == nil || s.view == nil {
		return nil, errors.New("native ownership refresh is closed")
	}
	return refreshNativeContainerAfterStop(ctx, s.view, release, cid, ack)
}
