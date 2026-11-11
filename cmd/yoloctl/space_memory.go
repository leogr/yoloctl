package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/spf13/cobra"
)

func selectSpaceMemory(cmd *cobra.Command) (string, error) {
	value, _ := cmd.Flags().GetString("memory")
	return resolveSpaceMemory(cmd.Context(), value, cmd.ErrOrStderr())
}

func resolveSpaceMemory(ctx context.Context, value string, errw io.Writer) (string, error) {
	if value != "auto" {
		if _, err := resources.VMBytes(value); err != nil {
			return "", err
		}
		return value, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, err := resources.ReadHostMemory(ctx)
	if err != nil {
		return "", fmt.Errorf("measure host memory (or pass --memory explicitly): %w", err)
	}
	reserved, err := liveSpaceMemory(ctx)
	if err != nil {
		return "", fmt.Errorf("measure existing VM allocations (or pass --memory explicitly): %w", err)
	}
	allocation, err := resources.VMDefault(host, reserved)
	if err != nil {
		return "", err
	}
	value = fmt.Sprintf("%dGiB", allocation/resources.GiB)
	cliInfo(logging.Host, "VM memory: %s (automatic; override with --memory)", value)
	return value, nil
}

func liveSpaceMemory(ctx context.Context) (uint64, error) {
	names, err := spaceconfig.List()
	if err != nil {
		return 0, err
	}
	// Consult each represented adapter once. A Firecracker-only installation
	// must not need limactl just to choose a new VM's memory.
	seen := map[string]bool{}
	var rows []vmapi.Instance
	for _, name := range names {
		driver, err := vmhost.ForSpace(name)
		if err != nil {
			return 0, err
		}
		if seen[driver.Kind()] {
			continue
		}
		seen[driver.Kind()] = true
		instances, err := driver.List(ctx)
		if err != nil {
			return 0, err
		}
		rows = append(rows, instances...)
	}
	return allocatedVMMemory(rows)
}

func allocatedVMMemory(rows []vmapi.Instance) (uint64, error) {
	var allocated uint64
	for _, row := range rows {
		if row.Status == "Stopped" {
			continue
		}
		// Paused/transitional VMs still consume RAM. Unknown state cannot prove
		// their configured budget is free, so account for it conservatively.
		if row.Memory == 0 || row.Memory > ^uint64(0)-allocated {
			return 0, fmt.Errorf("space %s has an invalid VM memory allocation", row.Name)
		}
		allocated += row.Memory
	}
	return allocated, nil
}
