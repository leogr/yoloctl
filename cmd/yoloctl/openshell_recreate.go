package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

type nativeReplacementRemoval interface {
	Remove(context.Context) error
	Close() error
}

func (a managedApplier) recreateNative(ctx context.Context, cfg *agentbox.Config, policy []byte, details *container.Details) error {
	inner, ok := a.inner.(agentbox.LiveApplier)
	if !ok || details == nil || details.ID == "" {
		return errors.New("native recreation requires its concrete inspected runtime applier")
	}
	// Recreate's --force bypasses confirmation; it never requested destroy's
	// immediate SIGKILL stop. Qualification captures the original before holder
	// or forward effects and keeps the handle through its actual settlement.
	removal, native, err := inner.PrepareNativeContainerRemoval(ctx, cfg.Name, false)
	if err != nil {
		return err
	}
	if !native || removal == nil {
		return errors.New("native recreation lacks its prepared removal")
	}
	return completeNativeReplacement(ctx, removal,
		func(check context.Context) error {
			if err := a.endNativeReplacementSession(check, cfg); err != nil {
				return err
			}
			return agentbox.WithdrawNativeForwards(check, a.client, cfg.Name)
		},
		func(check context.Context) error { return a.inner.Create(check, cfg, policy) })
}

// The native holder adapter requires the actual Stop ACK and complete bounded
// inventory. Keep conversation retirement before container effects, as in the
// existing managed recreate path, without its warning-only legacy holder seam.
func (a managedApplier) endNativeReplacementSession(ctx context.Context, cfg *agentbox.Config) error {
	if err := withdrawNativeHolder(ctx, a.client, cfg.Name); err != nil {
		return err
	}
	if err := lifecycleCleanupStoppedSessionState(ctx, a.client, cfg.Name); err != nil {
		return err
	}
	if a.onSessionEnded != nil {
		if err := a.onSessionEnded(sessionEndedByRecreate); err != nil {
			return fmt.Errorf("the session of box %s ended, but recording that for its conversation failed: %w", cfg.Name, err)
		}
	}
	return nil
}

func completeNativeReplacement(ctx context.Context, removal nativeReplacementRemoval, prefix, create func(context.Context) error) (result error) {
	if removal == nil || prefix == nil || create == nil {
		return errors.New("native recreation requires its complete prepared removal and launch callbacks")
	}
	closed := false
	defer func() {
		if !closed {
			result = errors.Join(result, removal.Close())
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := prefix(ctx); err != nil {
		return err
	}
	if err := removal.Remove(ctx); err != nil {
		return err
	}
	closed = true
	if err := removal.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return create(ctx)
}
