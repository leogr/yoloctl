package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// withdrawNativeHolder runs only after the native lifecycle caller has
// qualified the actual runtime under the controller's mutation admission. It
// preserves real service-stop replies, including on Lima, and never uses a
// later state observation to suppress a failed Stop. The SDK still separately
// proves current kernel consumers before deleting private runtime state.
func withdrawNativeHolder(ctx context.Context, c *container.Client, name string) error {
	if c == nil {
		return errors.New("native holder withdrawal requires its client")
	}
	if err := layout.ValidateName("agentbox", name); err != nil {
		return err
	}
	unit := session.UnitName(name)
	return withdrawNativeHolderWith(ctx, unit,
		func(check context.Context) ([]string, error) { return vmservice.InventoryHolders(check, c) },
		func(stop context.Context, selected string) error {
			return vmservice.StopAcknowledged(stop, c, selected)
		})
}

func withdrawNativeHolderWith(ctx context.Context, unit string, inventory func(context.Context) ([]string, error), stop func(context.Context, string) error) error {
	if inventory == nil || stop == nil {
		return errors.New("native holder withdrawal lacks complete inventory/acknowledgement wiring")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	known, err := inventory(ctx)
	if err != nil {
		return err
	}
	// This is a fresh pre-effect service observation, not historical Stop proof.
	// Outer admission prevents managed holder starts throughout this operation.
	if !slices.Contains(known, unit) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stop(ctx, unit); err != nil {
		return &agentbox.NativeEffectsUncertain{Cause: fmt.Errorf("native holder withdrawal was not acknowledged; no retry: %w", err)}
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for {
		current, err := inventory(finish)
		if err != nil {
			return &agentbox.NativeEffectsUncertain{Cause: err}
		}
		if !slices.Contains(current, unit) {
			return nil
		}
		select {
		case <-finish.Done():
			return &agentbox.NativeEffectsUncertain{Cause: errors.New("acknowledged native holder withdrawal left a retained service")}
		case <-time.After(20 * time.Millisecond):
		}
	}
}
