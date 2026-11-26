package agentbox

import (
	"context"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
)

type ownedStartInventoryKey struct{}

// WithOwnedStartInventory bounds each native start inventory read during
// controller-owned setup, which can outlive the attach client.
func WithOwnedStartInventory(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownedStartInventoryKey{}, true)
}

// Qualification needs every box's forwards to detect published-port conflicts.
func nativeStartForwardInventory(ctx context.Context, client *container.Client) ([]port.ForwardService, error) {
	if ctx.Value(ownedStartInventoryKey{}) == true {
		return port.InventoryBounded(ctx, client, "")
	}
	return port.Inventory(ctx, client, "")
}
