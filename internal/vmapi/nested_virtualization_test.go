package vmapi

import "testing"

func TestNestedHostEligibility(t *testing.T) {
	for _, tt := range []struct {
		chip, version string
		want          bool
	}{
		{"Apple M3 Max", "26.7.1", true}, {"Apple M4", "15.0", true},
		{"Apple M2", "26.7.1", false}, {"Apple M3", "14.6", false},
		{"Intel", "26.7.1", false}, {"", "15", false}, {"Future chip", "26", false},
	} {
		if got := (NestedHostFacts{OS: "darwin", Chip: tt.chip, Version: tt.version}).Eligible(); got != tt.want {
			t.Errorf("%+v: %v", tt, got)
		}
	}
}

func TestNestedResolutionUsesActualRouteAndExplicitChoice(t *testing.T) {
	host := NestedHostFacts{OS: "darwin", Chip: "Apple M3 Max", Version: "26.7.1"}
	for _, tt := range []struct {
		backend, os, driver, arch, pref string
		managed, enabled, refused       bool
	}{
		{"lima", "darwin", "vz", "aarch64", "auto", true, true, false},
		{"lima", "darwin", "qemu", "aarch64", "auto", true, false, false},
		{"lima", "darwin", "vz", "x86_64", "auto", true, false, false},
		{"lima", "darwin", "vz", "", "on", true, false, true},
		{"lima", "darwin", "qemu", "aarch64", "on", true, false, true},
		{"lima", "linux", "qemu", "aarch64", "auto", false, false, false},
		{"lima", "linux", "qemu", "aarch64", "on", false, false, true},
		{"firecracker", "darwin", "vz", "aarch64", "auto", false, false, false},
		{"firecracker", "linux", "", "", "on", false, false, true},
		{"lima", "darwin", "vz", "aarch64", "off", true, false, false},
	} {
		h := host
		h.OS = tt.os
		got, err := ResolveNestedVirtualization(tt.pref, tt.backend, Instance{VMType: tt.driver, Arch: tt.arch}, h)
		if (err != nil) != tt.refused || got.Managed != tt.managed || got.Enabled != tt.enabled {
			t.Errorf("%+v: %+v %v", tt, got, err)
		}
	}
	host.Chip = ""
	got, err := ResolveNestedVirtualization("on", "lima", Instance{VMType: "vz", Arch: "aarch64"}, host)
	if err != nil || !got.Enabled {
		t.Fatalf("explicit on overrides unknown eligibility: %+v %v", got, err)
	}
}
