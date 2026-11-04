package operator

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/layout"
)

type nativeRetirerKey struct{}
type nativeRetirer struct {
	original context.Context
	call     func(context.Context, string, string) error
}

// WithNativeListenerRetirer installs the original effect-gated worker's
// private parent request (worker checkpoint opcode 17). Public input cannot
// select it.
func WithNativeListenerRetirer(ctx context.Context, call func(context.Context, string, string) error) context.Context {
	return context.WithValue(ctx, nativeRetirerKey{}, nativeRetirer{original: ctx, call: call})
}

// RetireNativeListener asks the controller that owns box's native listener to
// retire it before the box's Stop. There is no local fallback: without the
// original parent request, enabled-operator removal refuses before effects.
func RetireNativeListener(ctx context.Context, identity RegistrationIdentity) error {
	if identity.Mode != RegistrationOpenShellNative || identity.Validate() != nil || layout.ValidateName("agentbox", identity.Box) != nil {
		return errors.New("native listener retirement requires a native registration identity")
	}
	r, ok := ctx.Value(nativeRetirerKey{}).(nativeRetirer)
	if !ok || r.original == nil || r.call == nil {
		return errors.New("native operator removal requires its original controller retirement request")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.original.Err(); err != nil {
		return err
	}
	return r.call(ctx, identity.Box, identity.Incarnation)
}
