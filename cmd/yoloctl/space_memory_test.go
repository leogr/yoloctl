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

func TestAutomaticVMMemoryExplainsTheSizingConstraint(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		total, available, other uint64 // MiB
		size, line              string
	}{
		{"quarter", 48 << 10, 40 << 10, 0, "12GiB", "VM memory: 12 GiB (auto: a quarter of 48 GiB host memory; override with --memory)"},
		{"below a whole GiB", (32 << 10) - 8, 24 << 10, 0, "7GiB", "VM memory: 7 GiB (auto: a quarter of 31.9 GiB host memory, rounded down to whole GiB; override with --memory)"},
		{"cap", 128 << 10, 100 << 10, 0, "16GiB", "VM memory: 16 GiB (auto: a quarter of 128 GiB host memory, capped at 16 GiB; override with --memory)"},
		{"minimum", 8 << 10, 6 << 10, 0, "4GiB", "VM memory: 4 GiB (auto: a quarter of 8 GiB host memory, raised to the 4 GiB minimum; override with --memory)"},
		{"available", 32 << 10, 8 << 10, 0, "4GiB", "VM memory: 4 GiB (auto: 8 GiB free, 4 GiB kept for the host; override with --memory)"},
		{"other spaces", 32 << 10, 24 << 10, 24 << 10, "4GiB", "VM memory: 4 GiB (auto: other spaces use 24 GiB of 32 GiB, 4 GiB kept for the host; override with --memory)"},
		{"available beats cap", 128 << 10, 22 << 10, 0, "6GiB", "VM memory: 6 GiB (auto: 22 GiB free, 16 GiB kept for the host; override with --memory)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, _ := captureCLIRecords(t)
			host := resources.HostMemory{Total: tc.total << 20, Available: tc.available << 20}
			size, err := selectAutomaticSpaceMemory(host, tc.other<<20)
			records := readCLIRecords(t, wire)
			if err != nil || size != tc.size || len(records) != 1 || records[0].Message != tc.line {
				t.Fatalf("got %q, %v, log %+v; want %q and %q", size, err, records, tc.size, tc.line)
			}
		})
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
	wire, _ := captureCLIRecords(t)
	for _, tc := range []struct{ value, line string }{
		{"12GiB", "VM memory: 12 GiB"},
		{"24GiB", "VM memory: 24 GiB"},
		{"4097MiB", "VM memory: 4097 MiB"},
	} {
		if err := cmd.Flags().Set("memory", tc.value); err != nil {
			t.Fatal(err)
		}
		got, err := selectSpaceMemory(cmd)
		records := readCLIRecords(t, wire)
		if err != nil || got != tc.value || len(records) != 1 || records[0].Message != tc.line {
			t.Fatalf("explicit memory %s: %q, %v, log %+v", tc.value, got, err, records)
		}
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
