package port

import (
	"context"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// InventoryBounded combines the matching-init/Lima bounded complete capture
// with the existing strict forward identity parser. An unavailable, oversized
// or malformed inventory supplies no partial roster or absence permission.
func InventoryBounded(ctx context.Context, c *container.Client, agentbox string) ([]ForwardService, error) {
	raw, err := vmservice.InventoryPortsBounded(ctx, c)
	if err != nil {
		return nil, err
	}
	return parseForwardInventory(raw, agentbox)
}
