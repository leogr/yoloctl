package openshell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func nativeEgressPlanFixture(t *testing.T) (nativeEgressPlan, namespaceIdentity, EgressInputs) {
	t.Helper()
	identity := namespaceIdentity{Device: 7, Inode: 91}
	seed := EgressInputs{strings.Repeat("d", 64), []netip.Addr{netip.MustParseAddr("192.168.127.1")}, []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24")}}
	observed := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/64")}
	plan, err := newNativeEgressPlan(identity, seed, observed)
	if err != nil {
		t.Fatal(err)
	}
	return plan, identity, seed
}

func TestNativeEgressPlanBindsCompleteDiscoveredPolicyBeforeEffects(t *testing.T) {
	plan, identity, seed := nativeEgressPlanFixture(t)
	if err := plan.validate(identity, seed); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range plan.Observed {
		if !slices.Contains(plan.Inputs.LocalPrefixes, prefix) {
			t.Fatal("final plan omitted a discovered namespace prefix", prefix)
		}
	}
	policy, err := GenerateEgressPolicy(plan.Inputs)
	if err != nil || !strings.Contains(policy, "203.0.113.0/24") || !strings.Contains(policy, "2001:db8::/64") {
		t.Fatal("planned policy does not deny actual local public peers", err)
	}
	early, _ := prepareEgress(seed)
	earlyObjects, _ := canonicalNFTObjects(early.expectedObjects())
	if plan.RulesSHA256 == sha256Hex(earlyObjects) {
		t.Fatal("early VM-only inputs qualified the later discovered rules")
	}
	full, _ := prepareEgress(plan.Inputs)
	objects, _ := canonicalNFTObjects(full.expectedObjects())
	if plan.RulesSHA256 != sha256Hex(objects) {
		t.Fatal("complete table hash differs from the final persisted plan")
	}
}

func TestNativeEgressPlanRejectsChangedSelectionOrIncompleteRules(t *testing.T) {
	for _, change := range []string{"schema", "namespace", "seed-owner", "final-owner", "seed-resolver", "final-resolver", "seed-prefix", "missing-observed-prefix", "changed-observation", "early-hash", "reordered-observation"} {
		t.Run(change, func(t *testing.T) {
			plan, identity, seed := nativeEgressPlanFixture(t)
			switch change {
			case "schema":
				plan.Schema++
			case "namespace":
				plan.Namespace.Inode++
			case "seed-owner":
				plan.Seed.OwnerSHA256 = strings.Repeat("e", 64)
			case "final-owner":
				plan.Inputs.OwnerSHA256 = strings.Repeat("e", 64)
			case "seed-resolver":
				plan.Seed.TrustedResolvers[0] = netip.MustParseAddr("8.8.8.8")
			case "final-resolver":
				plan.Inputs.TrustedResolvers[0] = netip.MustParseAddr("8.8.8.8")
			case "seed-prefix":
				plan.Seed.LocalPrefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
			case "missing-observed-prefix":
				plan.Inputs.LocalPrefixes = plan.Inputs.LocalPrefixes[:1]
			case "changed-observation":
				plan.Observed[0] = netip.MustParsePrefix("198.51.100.0/24")
			case "early-hash":
				early, _ := prepareEgress(seed)
				objects, _ := canonicalNFTObjects(early.expectedObjects())
				plan.RulesSHA256 = sha256Hex(objects)
			case "reordered-observation":
				slices.Reverse(plan.Observed)
			}
			if err := plan.validate(identity, seed); err == nil {
				t.Fatal("changed or incomplete plan passed", change)
			}
		})
	}
}

func TestNativeEgressPlanStrictResponseAndIndependentCopies(t *testing.T) {
	plan, identity, seed := nativeEgressPlanFixture(t)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeEgressPlan(raw, identity, seed); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range [][]byte{
		append(append([]byte{}, raw...), raw...),
		append([]byte(`{"unknown":true,`), raw[1:]...),
		append([]byte(fmt.Sprintf(`{"schema":%d,`, BootstrapSchema)), raw[1:]...),
		[]byte(`null`),
	} {
		if _, err := decodeNativeEgressPlan(malformed, identity, seed); err == nil {
			t.Fatal("ambiguous/unknown/trailing response accepted")
		}
	}
	seed.LocalPrefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
	seed.TrustedResolvers[0] = netip.MustParseAddr("8.8.8.8")
	after, _ := json.Marshal(plan)
	if !bytes.Equal(raw, after) {
		t.Fatal("caller mutation changed the retained complete plan")
	}
	originalSeed, _ := json.Marshal(plan.Seed)
	plan.Inputs.TrustedResolvers[0] = netip.MustParseAddr("9.9.9.9")
	plan.Inputs.LocalPrefixes[0] = netip.MustParsePrefix("192.0.2.0/24")
	afterSeed, _ := json.Marshal(plan.Seed)
	if !bytes.Equal(originalSeed, afterSeed) {
		t.Fatal("final inputs alias the trusted seed")
	}
}

func TestNativeEgressPlanObservedInventoryCannotChangeBeforeFixedApply(t *testing.T) {
	plan, identity, seed := nativeEgressPlanFixture(t)
	reordered := append(slices.Clone(plan.Observed), plan.Observed[0])
	slices.Reverse(reordered)
	if err := plan.validateObserved(reordered); err != nil {
		t.Fatal("equivalent observation refused", err)
	}
	for _, changed := range [][]netip.Prefix{
		nil, plan.Observed[:1],
		append(slices.Clone(plan.Observed), netip.MustParsePrefix("198.51.100.0/24")),
		{netip.MustParsePrefix("0.0.0.0/0")},
		{netip.MustParsePrefix("203.0.113.1/24")},
		{netip.MustParsePrefix("::ffff:192.0.2.0/120")},
	} {
		if err := plan.validateObserved(changed); err == nil {
			t.Fatal("changed/unavailable inventory was appended after planning")
		}
	}
	if _, err := newNativeEgressPlan(namespaceIdentity{}, seed, plan.Observed); err == nil {
		t.Fatal("unselected namespace accepted")
	}
	if _, err := newNativeEgressPlan(identity, seed, make([]netip.Prefix, 4097)); err == nil {
		t.Fatal("unbounded observed inventory accepted")
	}
}
