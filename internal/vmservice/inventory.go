package vmservice

import (
	"context"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// InventoryPorts includes known activating, failed, inactive and stopping
// units, which can retain their name or restart after an active-only list.
// A new native forward requires matching init support before any effect.
// Unsupported/unavailable inventory is an error, never an empty inventory.
func InventoryPorts(ctx context.Context, c *container.Client) (string, error) {
	native, err := microvm(c)
	if err != nil {
		return "", err
	}
	if native {
		return request(ctx, c, vmwire.UnitRequest{Action: "inventory", Unit: Unit{Name: vmwire.LegacyPortUnitPrefix}})
	}
	return c.ShellOutput(ctx, "sudo", "systemctl", "list-units", "--all", "--no-legend", "--plain", "--type=service", vmwire.LegacyPortUnitPrefix+"*", vmwire.OpenShellPortUnitPrefix+"*")
}
