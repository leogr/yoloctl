package resources

import (
	"strings"
	"testing"
)

func TestVMDefaultRespectsCapacityHeadroomAndLiveBudgets(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		total, available, others uint64
		want                     uint64
	}{
		{"small host", 8, 6, 0, 4},
		{"16 GiB host", 16, 12, 0, 4},
		{"32 GiB host", 32, 24, 0, 8},
		{"64 GiB host", 64, 50, 0, 16},
		{"128 GiB host capped", 128, 100, 0, 16},
		{"busy large host", 128, 22, 0, 6},
		{"mostly lazy VMs", 32, 24, 24, 4},
		{"live budgets exhausted", 32, 24, 25, 0},
		{"available exhausted", 32, 7, 0, 0},
		{"host too small", 4, 4, 0, 0},
		{"invalid counters", 16, 17, 0, 0},
		{"unmeasured", 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VMDefault(HostMemory{tc.total * GiB, tc.available * GiB}, tc.others*GiB)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("expected refusal, got %d", got)
				}
			} else if err != nil || got != tc.want*GiB {
				t.Fatalf("got %d, %v; want %d GiB", got, err, tc.want)
			}
		})
	}
	got, err := VMDefault(HostMemory{32 * GiB, 11*GiB + GiB/2}, 0)
	if err != nil || got != 7*GiB {
		t.Fatalf("headroom must round down to whole GiB: %d, %v", got, err)
	}
}

func TestHostMemoryDecoders(t *testing.T) {
	t.Run("Linux counts reclaimable RAM", func(t *testing.T) {
		host, err := parseMemInfo("MemTotal: 16777216 kB\nMemFree: 200 kB\nMemAvailable: 8388608 kB\nCached: 999 kB\n")
		if err != nil || host != (HostMemory{16 * GiB, 8 * GiB}) {
			t.Fatalf("%+v, %v", host, err)
		}
		for _, raw := range []string{"", "MemTotal: 1 kB", "MemTotal: 1 GB\nMemAvailable: 1 kB", "MemTotal: 1 kB\nMemAvailable: 2 kB", "MemTotal: 1 kB\nMemAvailable: 0 kB\nMemTotal: 1 kB"} {
			if _, err := parseMemInfo(raw); err == nil {
				t.Errorf("accepted %q", raw)
			}
		}
	})
	t.Run("Darwin page size and overlapping counters", func(t *testing.T) {
		raw := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 131072.\nPages inactive: 196608.\nPages speculative: 65536.\nPages purgeable: 65536.\nPages occupied by compressor: 500000.\n"
		host, err := parseVMStat(raw, 16*GiB)
		if err != nil || host != (HostMemory{16 * GiB, 6 * GiB}) {
			t.Fatalf("%+v, %v", host, err)
		}
		for _, broken := range []string{"", strings.ReplaceAll(raw, "16384", "-1"), strings.ReplaceAll(raw, "196608", "18446744073709551615"), strings.ReplaceAll(raw, "Pages free:", "missing:"), raw + "Pages free: 0.\n"} {
			if _, err := parseVMStat(broken, 16*GiB); err == nil {
				t.Errorf("accepted %q", broken)
			}
		}
	})
}

func TestVMBytesExplicitUnits(t *testing.T) {
	for value, want := range map[string]uint64{"4GiB": 4 * GiB, "512MiB": GiB / 2, "1KiB": 1024} {
		got, err := VMBytes(value)
		if err != nil || got != want {
			t.Errorf("%q: %d, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "auto", "0GiB", "-1GiB", "4GB", "4g", "4294967296GiB"} {
		if _, err := VMBytes(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
