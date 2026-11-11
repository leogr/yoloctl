package agentbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// WithdrawNativeForwards withdraws retained transports for an independently
// qualified native runtime under the caller's existing lifecycle admission.
// It obtains one Stop ACK per service, then checks complete inventory until
// collection finishes. It cannot settle an earlier lost Stop or grant admission.
func WithdrawNativeForwards(ctx context.Context, c *container.Client, name string) error {
	if c == nil {
		return errors.New("native forward withdrawal requires its client")
	}
	if err := layout.ValidateName("agentbox", name); err != nil {
		return err
	}
	known, err := port.InventoryBounded(ctx, c, name)
	if err != nil {
		return err
	}
	if len(known) == 0 {
		return nil
	}
	for _, service := range known {
		// Even a retained legacy transport needs an ACK here; no coarse State
		// fallback or legacy DNAT operation is appropriate in this namespace.
		if err := vmservice.StopAcknowledged(ctx, c, service.Unit); err != nil {
			// Only a lost reply (the call's own context ended) is uncertain;
			// an engine/service-manager error is a plain failure (old path).
			if ctx.Err() != nil {
				return &NativeEffectsUncertain{Cause: fmt.Errorf("native forward withdrawal was not acknowledged; no retry: %w", err)}
			}
			return fmt.Errorf("native forward withdrawal of %s failed: %w", service.Unit, err)
		}
		// A forwarder that exits non-zero on SIGTERM leaves a stopped unit
		// retained as failed (no --collect); collect it after the ACK.
		_ = vmservice.Collect(ctx, c, service.Unit)
	}
	// systemd may collect an acknowledged stopped transient a little later.
	// Repeat only complete read-only inventory, never another Stop mutation.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// The forwards were stopped and acknowledged; a unit still listed when the
	// bound ends is a named leftover, never an uncertain outcome (decision #3).
	leftover := func(current []port.ForwardService) error {
		units := make([]string, 0, len(current))
		for _, service := range current {
			units = append(units, service.Unit)
		}
		nativeForwardNotice(fmt.Errorf("box %s: stopped port forward units are still listed: %s", name, strings.Join(units, ", ")))
		return nil
	}
	last := known
	for {
		current, err := port.InventoryBounded(finish, c, name)
		if err != nil {
			if finish.Err() != nil {
				return leftover(last) // the bound ended during this read
			}
			return fmt.Errorf("native forward inventory after withdrawal: %w", err)
		}
		if len(current) == 0 {
			return nil
		}
		last = current
		select {
		case <-finish.Done():
			return leftover(current)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// nativeForwardNotice reports forward units left listed after an acknowledged
// stop; tests replace it.
var nativeForwardNotice = func(err error) {
	slog.Warn(fmt.Sprintf("%v", err), slog.String("actor", "box"), slog.String("executor", "openshell"))
}
