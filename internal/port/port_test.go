package port

import (
	"slices"
	"strings"
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

func TestDNATExecArgs(t *testing.T) {
	id := strings.Repeat("a", 64)
	wantAdd := []string{
		"sudo", "nerdctl", "exec", "--env", wantDNATExecPath, id,
		"iptables", "-t", "nat", "-A", "PREROUTING",
		"-i", "eth0", "-p", "tcp",
		"--dport", "3000",
		"-j", "DNAT",
		"--to-destination", "10.200.0.2:3000",
	}
	if add := dnatExecArgs(id, "-A", 3000); !slices.Equal(add, wantAdd) {
		t.Errorf("add DNAT argv = %q, want %q", add, wantAdd)
	}
	// The two forms must match apart from the operation for kernel deletion.
	wantDelete := slices.Clone(wantAdd)
	wantDelete[9] = "-D"
	if del := dnatExecArgs(id, "-D", 3000); !slices.Equal(del, wantDelete) {
		t.Errorf("delete DNAT argv = %q, want %q", del, wantDelete)
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
