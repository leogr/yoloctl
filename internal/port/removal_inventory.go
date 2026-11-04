package port

import (
	"context"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// InventoryForRemoval keeps the strict parser with finite capture before
// allocation. Unsupported or failed inventories never become absence.
func InventoryForRemoval(ctx context.Context, c *container.Client, box string) ([]ForwardService, error) {
	raw, err := vmservice.InventoryPortsBounded(ctx, c)
	if err != nil {
		return nil, err
	}
	return parseForwardInventory(raw, box)
}
