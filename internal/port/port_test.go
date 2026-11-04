package port

import (
	"reflect"
	"testing"
)

func TestParseMapping(t *testing.T) {
	tests := []struct {
		in        string
		wantHost  int
		wantCont  int
		expectErr bool
	}{
		{"3000:3000", 3000, 3000, false},
		{"8080:80", 8080, 80, false},
		{"3000", 3000, 3000, false}, // bare port maps PORT:PORT
		{"", 0, 0, true},
		{"abc:80", 0, 0, true},
		{"80:abc", 0, 0, true},
		{"0:80", 0, 0, true},     // out-of-range host
		{"80:70000", 0, 0, true}, // out-of-range container
		{"-1:80", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			host, cont, err := ParseMapping(tt.in)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("ParseMapping(%q) = (%d,%d), want error", tt.in, host, cont)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMapping(%q) error: %v", tt.in, err)
			}
			if host != tt.wantHost || cont != tt.wantCont {
				t.Errorf("ParseMapping(%q) = (%d,%d), want (%d,%d)",
					tt.in, host, cont, tt.wantHost, tt.wantCont)
			}
		})
	}
}

func TestUnitNameRoundTrip(t *testing.T) {
	tests := []Spec{
		{Agentbox: "coder", HostPort: 3000, ContainerPort: 3000},
		{Agentbox: "tester", HostPort: 8080, ContainerPort: 80},
		// Hyphenated agentbox names must survive: ParseUnitName splits
		// from the right so the agentbox can keep its hyphens.
		{Agentbox: "web-server", HostPort: 1234, ContainerPort: 1234},
		{Agentbox: "many-hyphens-here", HostPort: 9000, ContainerPort: 5000},
	}
	for _, spec := range tests {
		t.Run(spec.Agentbox, func(t *testing.T) {
			name := UnitName(spec)
			got, ok := ParseUnitName(name)
			if !ok {
				t.Fatalf("ParseUnitName(%q) = ok=false", name)
			}
			if got != spec {
				t.Errorf("round-trip = %+v, want %+v", got, spec)
			}
		})
	}
}

func TestDNATRuleArgs(t *testing.T) {
	// The -A / -D forms must be byte-identical except for the op so
	// the kernel can match the install and delete rules. Lock that in.
	add := dnatRuleArgs("-A", 3000)
	del := dnatRuleArgs("-D", 3000)
	wantAdd := []string{
		"iptables", "-t", "nat", "-A", "PREROUTING",
		"-i", "eth0", "-p", "tcp",
		"--dport", "3000",
		"-j", "DNAT",
		"--to-destination", "10.200.0.2:3000",
	}
	if !reflect.DeepEqual(add, wantAdd) {
		t.Errorf("dnatRuleArgs(-A, 3000) = %v, want %v", add, wantAdd)
	}
	if len(add) != len(del) {
		t.Fatalf("add/del arg lengths differ: %d vs %d", len(add), len(del))
	}
	for i := range add {
		if i == 3 {
			continue // the op slot
		}
		if add[i] != del[i] {
			t.Errorf("add[%d]=%q != del[%d]=%q (only op slot may differ)", i, add[i], i, del[i])
		}
	}
}

func TestParseUnitNameRejectsForeign(t *testing.T) {
	for _, name := range []string{
		"",
		"foo.service",
		"yoloctl-port-.service",             // empty agentbox
		"yoloctl-port-coder.service",        // missing both ports
		"yoloctl-port-coder-80.service",     // missing container port
		"yoloctl-port-coder-abc-80.service", // host port not numeric
	} {
		if _, ok := ParseUnitName(name); ok {
			t.Errorf("ParseUnitName(%q) returned ok=true, want false", name)
		}
	}
}
