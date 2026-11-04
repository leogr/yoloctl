package openshell

import (
	"net/netip"
	"strings"
	"testing"
)

func TestEgressRequiresInfrastructureAndNarrowResolverSelection(t *testing.T) {
	valid := EgressInputs{TrustedResolvers: []netip.Addr{netip.MustParseAddr("192.168.127.1")}, LocalPrefixes: []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24")}}
	valid.OwnerSHA256 = strings.Repeat("a", 64)
	for _, owner := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), "\"; flush ruleset"} {
		input := valid
		input.OwnerSHA256 = owner
		if policy, err := GenerateEgressPolicy(input); err == nil || policy != "" {
			t.Fatal("unowned egress inputs produced a policy")
		}
	}
	for _, input := range []EgressInputs{
		{}, {TrustedResolvers: valid.TrustedResolvers}, {LocalPrefixes: valid.LocalPrefixes},
		{TrustedResolvers: []netip.Addr{netip.MustParseAddr("127.0.0.53")}, LocalPrefixes: valid.LocalPrefixes},
		{TrustedResolvers: []netip.Addr{netip.MustParseAddr("ff02::1")}, LocalPrefixes: valid.LocalPrefixes},
		{TrustedResolvers: []netip.Addr{netip.MustParseAddr("::")}, LocalPrefixes: valid.LocalPrefixes},
		{TrustedResolvers: valid.TrustedResolvers, LocalPrefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
		{TrustedResolvers: valid.TrustedResolvers, LocalPrefixes: []netip.Prefix{netip.MustParsePrefix("::/0")}},
		{TrustedResolvers: valid.TrustedResolvers, LocalPrefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.2/24")}},
	} {
		input.OwnerSHA256 = valid.OwnerSHA256
		if policy, err := GenerateEgressPolicy(input); err == nil || policy != "" {
			t.Fatal("unsafe egress inputs produced a policy")
		}
	}
	policy, err := GenerateEgressPolicy(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(policy, "create table inet "+EgressTable+" { comment \"yoloctl:"+valid.OwnerSHA256+"\"; }\ntable inet "+EgressTable+" {\n") || !strings.Contains(policy, "comment \"yoloctl:"+valid.OwnerSHA256+"\";") {
		t.Fatal("policy can adopt an existing table or lacks its exact runtime owner")
	}
	order := []string{"oifname \"lo\"", "ct direction reply ct state established,related accept", "ip daddr @resolver_v4 udp dport 53 accept", "ip daddr @resolver_v4 tcp dport 53 accept", "ip daddr @denied_v4 counter name denied_ipv4 drop", "meta nfproto ipv4 accept"}
	last := -1
	for _, rule := range order {
		next := strings.Index(policy, rule)
		if next <= last {
			t.Fatal("egress rule ordering lost:", rule)
		}
		last = next
	}
	if strings.Contains(policy, "flush") || strings.Contains(policy, "ct state established accept") {
		t.Fatal("policy flushes foreign state or broadly admits outbound established flows")
	}
	if !strings.Contains(policy, "elements = { 192.168.127.1 };") {
		t.Fatal("resolver exemption widened beyond one address")
	}
}

func TestEgressCompactsInfrastructurePrefixesWithoutDroppingGlobalPeer(t *testing.T) {
	input := EgressInputs{
		OwnerSHA256:      strings.Repeat("a", 64),
		TrustedResolvers: []netip.Addr{netip.MustParseAddr("192.168.127.1"), netip.MustParseAddr("2001:db8::53")},
		LocalPrefixes:    []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24"), netip.MustParsePrefix("10.4.0.1/32"), netip.MustParsePrefix("2001:db8::1/128")},
	}
	policy, err := GenerateEgressPolicy(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(policy, "10.4.0.0/24") || strings.Contains(policy, "10.4.0.1/32") || !strings.Contains(policy, "10.0.0.0/8") {
		t.Fatal("overlapping IPv4 interval ranges were not compacted")
	}
	for _, rule := range []string{"2001:db8::1/128", "::ffff:0.0.0.0/96", "fc00::/7", "fe80::/10", "ff00::/8", "ip6 daddr @resolver_v6 udp dport 53 accept", "ip6 daddr @resolver_v6 tcp dport 53 accept"} {
		if !strings.Contains(policy, rule) {
			t.Fatal("required IPv6 denial/resolver rule absent:", rule)
		}
	}
}
