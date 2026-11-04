package spaceconfig

import (
	"strings"
	"testing"
)

// Without an explicit size a space gets half of the host's free space,
// whole GiB, between 30 and 50 GiB.
func TestAutoVMDiskIsHalfTheFreeSpaceWithinBounds(t *testing.T) {
	for _, tc := range []struct{ free, want uint64 }{
		{80, 40}, {120, 50}, {50, 30}, {20, 30}, {200, 50}, {0, 30}, {81, 40}, {99, 49},
	} {
		if got := AutoVMDisk(tc.free * GiB); got != tc.want*GiB {
			t.Errorf("%d GiB free: %d GiB, want %d", tc.free, got/GiB, tc.want)
		}
	}
	if got := AutoVMDisk(81*GiB + GiB/2); got != 40*GiB {
		t.Errorf("rounds down to whole GiB: %d", got)
	}
}

func TestVMDiskBytesAcceptsWholeGiBAboveTheFloor(t *testing.T) {
	for _, ok := range []string{"20GiB", "30GiB", "50GiB", "500GiB"} {
		if n, err := VMDiskBytes(ok); err != nil || DiskSize(n) != ok {
			t.Errorf("%s: %d %v", ok, n, err)
		}
	}
	for _, bad := range []string{"", "0GiB", "19GiB", "20480MiB0", "30720MiB1", "25601MiB", "40G", "auto", "-1GiB", "30.5GiB"} {
		if _, err := VMDiskBytes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if n, err := VMDiskBytes("30720MiB"); err != nil || n != 30*GiB {
		t.Errorf("a whole-GiB size in MiB is that size: %d %v", n, err)
	}
}

func TestBootstrapValidatesTheSystemDisk(t *testing.T) {
	cfg, err := New("dev", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskUUID = "uuid"
	b, _, err := Split(cfg, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if b.VM.Disk != "30GiB" {
		t.Fatalf("the saved floor stays 30GiB: %q", b.VM.Disk)
	}
	b.VM.Disk = "10GiB"
	if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Fatalf("a disk below the floor validated: %v", err)
	}
}

func TestDataDiskSizeAndGrowCheckpoint(t *testing.T) {
	for in, want := range map[string]uint64{"": 10 * GiB, "4GiB": 4 * GiB, "20GiB": 20 * GiB} {
		if n, err := DataDiskBytes(in); err != nil || n != want {
			t.Errorf("%q: %d %v", in, n, err)
		}
	}
	for _, bad := range []string{"3GiB", "4097MiB", "20G", "0GiB"} {
		if _, err := DataDiskBytes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	cfg, err := New("dev", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskUUID = "uuid"
	b, _, err := Split(cfg, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	b.VM.DataDisk = "20GiB"
	b.DataDiskGrow = &DataDiskGrow{From: 10 * GiB, To: 20 * GiB, Phase: GrowExtended}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Bootstrap){
		"phase":                   func(b *Bootstrap) { b.DataDiskGrow.Phase = "done" },
		"shrink":                  func(b *Bootstrap) { b.DataDiskGrow.To = 5 * GiB },
		"not the saved size":      func(b *Bootstrap) { b.VM.DataDisk = "30GiB" },
		"uninitialized disk":      func(b *Bootstrap) { b.Initializing, b.DiskUUID = true, "" },
		"partial GiB":             func(b *Bootstrap) { b.DataDiskGrow.From = 10*GiB + 1 },
		"below the data disk min": func(b *Bootstrap) { b.DataDiskGrow.From = GiB },
	} {
		c := *b
		g := *b.DataDiskGrow
		c.DataDiskGrow = &g
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s validated", name)
		}
	}
}
