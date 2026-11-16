package main

import (
	"context"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
)

// This is an original current command's precondition, never proof that an
// earlier lost Stop completed. The controller's receipt/holder holds remain
// independent. Only the qualified native caller uses this path; legacy launch
// and its historical cleanup are unchanged.
func withdrawNativeStartForwards(ctx context.Context, c *container.Client, name string) error {
	return agentbox.WithdrawNativeForwards(ctx, c, name)
}

// The actual constructor has no caller-controlled launcher or qualification
// bypass. It qualifies the box's saved policy and captures observed ports
// before any caller withdraws them.
type nativePreparedStart interface {
	Check(context.Context, *agentbox.Config) error
	Start(context.Context, *agentbox.Config) error
	Close() error
}

var prepareNativeExistingStart = func(ctx context.Context, c *container.Client, cfg *agentbox.Config, observed bool) (nativePreparedStart, bool, error) {
	prepared, native, err := (agentbox.LiveApplier{Client: c, VerifyStorage: controller.VerifyRuntimeStorage}).PrepareNativeStart(ctx, cfg, observed)
	if prepared == nil {
		return nil, native, err
	}
	return prepared, native, err
}

func closeNativeStart(prepared nativePreparedStart) {
	if prepared != nil {
		_ = prepared.Close()
	}
}

// Explicit start/restart alone follows the saved-policy start with its desired
// reconciliation (forwards; the policy already matches). Revive/chat use the
// shared start directly and never call this suffix. Return immediately on
// startup or policy/cleanup uncertainty.
func startAndReconcileExistingAgentbox(ctx context.Context, client *container.Client, cfg *agentbox.Config, prepared nativePreparedStart, inspector agentbox.StateInspector, applier agentbox.Applier) error {
	if err := startExistingAgentboxContainerPrepared(ctx, client, cfg, prepared); err != nil {
		if isContainerGone(err) {
			return asUserError(fmt.Errorf("box %q has no container to start in space %q — materialize it from its declaration: yoloctl box start %s", cfg.Name, cfg.Space, cfg.Name))
		}
		return err
	}
	if _, err := agentbox.ReconcileForStart(ctx, cfg, inspector, applier); err != nil {
		return fmt.Errorf("post-start reconcile of %s (container retained): %w", cfg.Name, err)
	}
	return nil
}
