package main

import (
	"context"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Only an actual new allocation/replacement selects the new default. Existing
// containers continue through their inspected recorded-generation start path.
func defaultCreationContext(ctx context.Context, cfg *agentbox.Config) context.Context {
	if cfg != nil && !cfg.Privileged && spaceenv.Native() {
		return withNativeCreationImagePreparation(ctx)
	}
	return ctx
}

func qualifyDefaultNativeCreation(ctx context.Context, client *container.Client, cfg *agentbox.Config) (bool, error) {
	if cfg.Privileged || !spaceenv.Native() {
		return false, nil
	}
	if err := checkDefaultNativeCreation(cfg); err != nil {
		return true, err
	}
	a, err := defaultCreationApplier(client)
	if err != nil {
		return true, err
	}
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		return true, err
	}
	_, err = a.PreflightNativeCreate(ctx, cfg, policy)
	return true, err
}

// checkDefaultNativeCreation validates the operator grant of a native box.
// Operator-enabled boxes are created with a native registration served by the
// controller (leogr's relaxed decision), so the grant no longer refuses.
func checkDefaultNativeCreation(cfg *agentbox.Config) error {
	if cfg.Privileged || !spaceenv.Native() {
		return nil
	}
	_, err := agentbox.OperatorEnabled(cfg)
	return err
}
