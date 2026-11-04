package main

import (
	"context"
	"testing"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func TestVMAllocationCountsPausedAndUncertainInstances(t *testing.T) {
	rows := []vmapi.Instance{
		{Name: "running", Status: "Running", Memory: 4 * resources.GiB},
		{Name: "paused", Status: "Paused", Memory: 8 * resources.GiB},
		{Name: "starting", Status: "Starting", Memory: 4 * resources.GiB},
		{Name: "stopped", Status: "Stopped", Memory: 16 * resources.GiB},
	}
	got, err := allocatedVMMemory(rows)
	if err != nil || got != 16*resources.GiB {
		t.Fatalf("%d, %v", got, err)
	}
	for _, memory := range []uint64{0, ^uint64(0)} {
		rows[0].Memory = memory
		if _, err := allocatedVMMemory(rows); err == nil {
			t.Fatal("accepted unknown or overflowing live VM budget")
		}
	}
}

func TestExplicitVMMemoryDoesNotProbeHostOrVMs(t *testing.T) {
	cmd := newSpaceCreateCmd()
	if cmd.Flag("memory").DefValue != "auto" {
		t.Fatal("new VMs must default to host-aware selection")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd.SetContext(ctx)
	if err := cmd.Flags().Set("memory", "24GiB"); err != nil {
		t.Fatal(err)
	}
	got, err := selectSpaceMemory(cmd)
	if err != nil || got != "24GiB" {
		t.Fatalf("explicit override must not be capped/probed: %q, %v", got, err)
	}
	if err := cmd.Flags().Set("memory", "typo"); err != nil {
		t.Fatal(err)
	}
	if _, err := selectSpaceMemory(cmd); err == nil {
		t.Fatal("accepted invalid explicit VM memory")
	}
}

func TestEmptySpaceInventoryNeedsNoHypervisor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	got, err := liveSpaceMemory(t.Context())
	if err != nil || got != 0 {
		t.Fatalf("empty inventory: %d, %v", got, err)
	}
}
