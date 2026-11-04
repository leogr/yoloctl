package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

// preparedBoxRemoval is the retained native adapter, prepared read-only before
// holder or forward effects. Its Remove owns the one original Stop/rm and the
// complete namespace/endpoint/private-data suffix; callers must never follow it
// with generic operator or native-path cleanup.
type preparedBoxRemoval interface {
	Remove(context.Context) error
	Close() error
}

// removePreparedNativeBox joins the acknowledged session/forward prefix with
// the original native removal. Closing retained observations is part of this
// result: callers may delete declarations or other box data only on nil. This
// helper does not select a runtime or manufacture an SDK acknowledgement.
// Like the old path, each step's own result decides: an ordinary step error is
// a known failed destroy, and only a step's own typed lost-result error
// (NativeEffectsUncertain) leaves the outcome uncertain.
func removePreparedNativeBox(ctx context.Context, removal preparedBoxRemoval, stopHolder, unforward func(context.Context) error) (result error) {
	if removal == nil {
		return errors.New("native destroy requires its prepared removal")
	}
	defer func() { result = errors.Join(result, removal.Close()) }()
	if stopHolder == nil || unforward == nil {
		return errors.New("native destroy requires acknowledged holder and forward withdrawal")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stopHolder(ctx); err != nil {
		return fmt.Errorf("native destroy session holder withdrawal: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unforward(ctx); err != nil {
		return fmt.Errorf("native destroy port forward withdrawal: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return removal.Remove(ctx)
}

// nativeDestroyForwardWithdrawal is the native destroy callsite's callback.
// Share the complete inventory/ACK/collection path with native stop and recreate.
func nativeDestroyForwardWithdrawal(c *container.Client, name string) func(context.Context) error {
	return func(ctx context.Context) error { return agentbox.WithdrawNativeForwards(ctx, c, name) }
}
