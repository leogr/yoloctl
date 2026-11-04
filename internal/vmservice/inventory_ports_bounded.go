package vmservice

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// InventoryPortsBounded preserves the complete retained-service view of both
// port transports with limits during actual capture and a finite read deadline.
// Native lifecycle callers use this instead of the legacy unbounded transport;
// failed/oversized/unfinished responses never supply an empty roster.
func InventoryPortsBounded(ctx context.Context, c *container.Client) (string, error) {
	if c == nil {
		return "", errors.New("bounded port inventory requires its container client")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	native, err := microvm(c)
	if err != nil {
		return "", err
	}
	return readServiceInventory(ctx, c, native, vmwire.LegacyPortUnitPrefix, vmwire.LegacyPortUnitPrefix+"*", vmwire.OpenShellPortUnitPrefix+"*")
}
