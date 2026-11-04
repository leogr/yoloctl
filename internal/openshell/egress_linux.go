package openshell

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// EgressInputs are selected by VM infrastructure, never resolv.conf or box
// environment. LocalPrefixes includes verified VM/host/CNI peers, connected
// infrastructure routes and directed broadcast addresses, excluding defaults.
// Discovery and installation remain separate from this pure rule generator.
type EgressInputs struct {
	OwnerSHA256      string         `json:"owner_sha256"`
	TrustedResolvers []netip.Addr   `json:"trusted_resolvers"`
	LocalPrefixes    []netip.Prefix `json:"local_prefixes"`
}

const EgressTable = "yoloctl_egress"

type egressPlan struct {
	owner                                    string
	resolvers4, resolvers6, denied4, denied6 []string
}

func prepareEgress(in EgressInputs) (egressPlan, error) {
	if !lowerHex(in.OwnerSHA256, 32) {
		return egressPlan{}, errors.New("unconfined egress requires an exact runtime ownership marker")
	}
	if len(in.TrustedResolvers) == 0 || len(in.TrustedResolvers) > 16 || len(in.LocalPrefixes) == 0 || len(in.LocalPrefixes) > 4096 {
		return egressPlan{}, errors.New("unconfined egress requires bounded trusted resolvers and verified infrastructure routes")
	}
	var resolvers4, resolvers6 []string
	for _, addr := range in.TrustedResolvers {
		if !addr.IsValid() || addr.Zone() != "" || addr.Is4In6() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
			return egressPlan{}, errors.New("invalid trusted unconfined resolver address")
		}
		if addr.Is4() {
			resolvers4 = append(resolvers4, addr.String())
		} else {
			resolvers6 = append(resolvers6, addr.String())
		}
	}
	// Special destinations are denied on non-loopback interfaces. These include
	// RFC1918, shared address space, local/link-local metadata, unspecified,
	// multicast and reserved/broadcast ranges. Root-discovered routes add the
	// backend's actual infrastructure, including any publicly numbered peers.
	var denied []netip.Prefix
	for _, prefix := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
		"::/96", "::ffff:0:0/96", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		denied = append(denied, netip.MustParsePrefix(prefix))
	}
	for _, prefix := range in.LocalPrefixes {
		if !prefix.IsValid() || prefix.Bits() == 0 || prefix.Addr().Zone() != "" || prefix != prefix.Masked() {
			return egressPlan{}, errors.New("invalid infrastructure prefix (default routes cannot be used as destination denies)")
		}
		denied = append(denied, prefix)
	}
	denied = compactPrefixes(denied)
	var denied4, denied6 []string
	for _, prefix := range denied {
		if prefix.Addr().Is4() {
			denied4 = append(denied4, prefix.String())
		} else {
			denied6 = append(denied6, prefix.String())
		}
	}
	slices.Sort(resolvers4)
	slices.Sort(resolvers6)
	slices.Sort(denied4)
	slices.Sort(denied6)
	return egressPlan{in.OwnerSHA256, slices.Compact(resolvers4), slices.Compact(resolvers6), denied4, denied6}, nil
}

// GenerateEgressPolicy returns one atomic nft transaction for the container's
// own network namespace. It creates only its own table and never flushes or
// replaces global rules. A pre-existing table is an installation error.
// Privileged boxes do not use this integration.
func GenerateEgressPolicy(in EgressInputs) (string, error) {
	p, err := prepareEgress(in)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	// nftables 1.0.9 expands child objects only for add-table declarations,
	// while its create command supplies exclusivity. Put the owner on that
	// first create, then expand the complete children in the SAME transaction.
	// A bare create loses the owner; a create with children omits those children.
	fmt.Fprintf(&out, "create table inet %s { comment \"yoloctl:%s\"; }\n", EgressTable, p.owner)
	fmt.Fprintf(&out, "table inet %s {\n", EgressTable)
	writeSet := func(name, kind string, entries []string, interval bool) {
		slices.Sort(entries)
		entries = slices.Compact(entries)
		fmt.Fprintf(&out, "  set %s { type %s;", name, kind)
		if interval {
			out.WriteString(" flags interval;")
		}
		fmt.Fprintf(&out, " elements = { %s }; }\n", strings.Join(entries, ", "))
	}
	writeSet("denied_v4", "ipv4_addr", p.denied4, true)
	writeSet("denied_v6", "ipv6_addr", p.denied6, true)
	if len(p.resolvers4) > 0 {
		writeSet("resolver_v4", "ipv4_addr", p.resolvers4, false)
	}
	if len(p.resolvers6) > 0 {
		writeSet("resolver_v6", "ipv6_addr", p.resolvers6, false)
	}
	out.WriteString("  counter denied_ipv4 { }\n  counter denied_ipv6 { }\n")
	out.WriteString("  chain output {\n    type filter hook output priority filter; policy drop;\n")
	// Loopback must be both addressed to and routed over this namespace's lo.
	// CT reply-direction permits responses to inbound connections admitted by
	// the outer infrastructure. It never permits a newly outbound connection,
	// even after that connection becomes established.
	out.WriteString("    oifname \"lo\" ip daddr 127.0.0.0/8 accept\n")
	out.WriteString("    oifname \"lo\" ip6 daddr ::1 accept\n")
	out.WriteString("    ct direction reply ct state established,related accept\n")
	for _, proto := range []string{"udp", "tcp"} {
		if len(p.resolvers4) > 0 {
			fmt.Fprintf(&out, "    ip daddr @resolver_v4 %s dport 53 accept\n", proto)
		}
		if len(p.resolvers6) > 0 {
			fmt.Fprintf(&out, "    ip6 daddr @resolver_v6 %s dport 53 accept\n", proto)
		}
	}
	out.WriteString("    ip daddr @denied_v4 counter name denied_ipv4 drop\n")
	out.WriteString("    ip6 daddr @denied_v6 counter name denied_ipv6 drop\n")
	out.WriteString("    meta nfproto ipv4 accept\n    meta nfproto ipv6 accept\n  }\n}\n")
	return out.String(), nil
}

func compactPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		if a.Addr().BitLen() != b.Addr().BitLen() {
			return a.Addr().BitLen() - b.Addr().BitLen()
		}
		if a.Bits() != b.Bits() {
			return a.Bits() - b.Bits()
		}
		return a.Addr().Compare(b.Addr())
	})
	var compact []netip.Prefix
	for _, prefix := range prefixes {
		covered := false
		for _, parent := range compact {
			if parent.Addr().BitLen() == prefix.Addr().BitLen() && parent.Bits() <= prefix.Bits() && parent.Contains(prefix.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			compact = append(compact, prefix)
		}
	}
	return compact
}
