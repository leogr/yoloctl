package operator

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/leogr/yoloctl/internal/layout"
)

type nativeIncarnationKey struct{}
type nativeIncarnationAllocator struct {
	original context.Context
	call     func(context.Context, string) (string, error)
}

// WithNativeIncarnationAllocator connects an original controller worker to its
// inherited checkpoint channel without making component services import the
// controller. Install only on the actual worker context. This callback and its
// public nonce confer no reservation, publication, CID or operator authority;
// the parent must independently match its original allocation at publication.
func WithNativeIncarnationAllocator(ctx context.Context, call func(context.Context, string) (string, error)) context.Context {
	return context.WithValue(ctx, nativeIncarnationKey{}, nativeIncarnationAllocator{original: ctx, call: call})
}

// AllocateNativeIncarnation has no local-random or metadata fallback. The live
// original parent remains responsible for receipt, effect-phase and box scope.
// Preserve both cancellation lifetimes even if a downstream caller drops the
// context's cancellation with WithoutCancel; never cache a nonce for retries.
func AllocateNativeIncarnation(ctx context.Context, box string) (string, error) {
	if err := layout.ValidateName("agentbox", box); err != nil {
		return "", err
	}
	a, ok := ctx.Value(nativeIncarnationKey{}).(nativeIncarnationAllocator)
	if !ok || a.original == nil || a.call == nil {
		return "", errors.New("native operator creation requires its original controller incarnation allocator")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.original.Err(); err != nil {
		return "", err
	}
	value, err := a.call(ctx, box)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.original.Err(); err != nil {
		return "", err
	}
	data, err := hex.DecodeString(value)
	if err != nil || len(data) != 16 || hex.EncodeToString(data) != value {
		return "", errors.New("invalid original native operator incarnation")
	}
	return value, nil
}
