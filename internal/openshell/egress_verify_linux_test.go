package openshell

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

func egressReadbackFixture(t *testing.T) (EgressInputs, map[string]any) {
	t.Helper()
	input := EgressInputs{strings.Repeat("d", 64), []netip.Addr{netip.MustParseAddr("192.168.127.1"), netip.MustParseAddr("2001:db8::53")}, []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24"), netip.MustParsePrefix("2001:db8::1/128")}}
	plan, err := prepareEgress(input)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture is synthetic, not an attributed VM readback. The AST forms
	// are checked against nftables 1.0.9's schema/json.c/CT JSON unit fixtures.
	objects := append([]nftObject{{"metainfo": nftObject{"version": "1.0.9", "json_schema_version": 1}}}, plan.expectedObjects()...)
	raw, err := json.Marshal(map[string]any{"nftables": objects})
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return input, fixture
}

func TestEgressReadbackVerifiesSemanticRulesAndOwnership(t *testing.T) {
	input, fixture := egressReadbackFixture(t)
	for _, entry := range fixture["nftables"].([]any) {
		for kind, value := range entry.(map[string]any) {
			body := value.(map[string]any)
			if kind == "metainfo" {
				continue
			}
			body["handle"] = float64(123)
			if kind == "counter" {
				body["packets"] = float64(1234)
				body["bytes"] = float64(98765)
			}
			if kind == "set" {
				body["size"] = float64(65535)
				elements := body["elem"].([]any)
				for i, j := 0, len(elements)-1; i < j; i, j = i+1, j-1 {
					elements[i], elements[j] = elements[j], elements[i]
				}
			}
		}
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEgressPolicyJSON(raw, input); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareEgress(input)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(plan.expectedObjects())
	if !bytes.Contains(expected, []byte(`"right":["established","related"]`)) {
		t.Fatal("comma-separated CT flags must match pinned upstream's JSON list representation")
	}
}

func TestEgressReadbackRejectsFilteringAndAuthorityMutations(t *testing.T) {
	type edit func(map[string]any)
	for name, mutate := range map[string]edit{
		"wrong-owner":            func(f map[string]any) { nftBody(f, "table", "")["comment"] = "yoloctl:" + strings.Repeat("e", 64) },
		"dormant-table":          func(f map[string]any) { nftBody(f, "table", "")["flags"] = []any{"dormant"} },
		"wrong-hook":             func(f map[string]any) { nftBody(f, "chain", "output")["hook"] = "input" },
		"accept-default":         func(f map[string]any) { nftBody(f, "chain", "output")["policy"] = "accept" },
		"missing-private-prefix": func(f map[string]any) { b := nftBody(f, "set", "denied_v4"); b["elem"] = b["elem"].([]any)[1:] },
		"wider-resolver":         func(f map[string]any) { nftBody(f, "set", "resolver_v4")["elem"] = []any{"192.168.127.0/24"} },
		"timed-denies":           func(f map[string]any) { nftBody(f, "set", "denied_v4")["timeout"] = float64(30) },
		"extra-chain": func(f map[string]any) {
			f["nftables"] = append(f["nftables"].([]any), map[string]any{"chain": map[string]any{"family": "inet", "table": EgressTable, "name": "other"}})
		},
		"broad-established":  func(f map[string]any) { b := nftRules(f)[2]; b["expr"] = b["expr"].([]any)[1:] },
		"original-direction": func(f map[string]any) { ruleExprMatch(nftRules(f)[2], 0)["right"] = "original" },
		"different-DNS-port": func(f map[string]any) { ruleExprMatch(nftRules(f)[3], 1)["right"] = float64(853) },
		"public-before-deny": func(f map[string]any) {
			objects := f["nftables"].([]any)
			objects[len(objects)-1], objects[len(objects)-3] = objects[len(objects)-3], objects[len(objects)-1]
		},
		"negative-counter": func(f map[string]any) { nftBody(f, "counter", "denied_ipv4")["packets"] = float64(-1) },
		"unknown-object": func(f map[string]any) {
			f["nftables"] = append(f["nftables"].([]any), map[string]any{"flowtable": map[string]any{}})
		},
		"new-schema": func(f map[string]any) { nftBody(f, "metainfo", "")["json_schema_version"] = float64(2) },
	} {
		t.Run(name, func(t *testing.T) {
			input, fixture := egressReadbackFixture(t)
			mutate(fixture)
			raw, _ := json.Marshal(fixture)
			if err := VerifyEgressPolicyJSON(raw, input); err == nil {
				t.Fatal("unsafe installed rule mutation passed verification")
			}
		})
	}
	input, fixture := egressReadbackFixture(t)
	raw, _ := json.Marshal(fixture)
	duplicate := bytes.Replace(raw, []byte(`"policy":"drop"`), []byte(`"policy":"accept","policy":"drop"`), 1)
	if err := VerifyEgressPolicyJSON(duplicate, input); err == nil {
		t.Fatal("duplicate authority field accepted")
	}
}

func nftBody(f map[string]any, kind, name string) map[string]any {
	for _, value := range f["nftables"].([]any) {
		if raw, ok := value.(map[string]any)[kind]; ok {
			body := raw.(map[string]any)
			if name == "" || body["name"] == name {
				return body
			}
		}
	}
	panic("missing fixture object")
}
func nftRules(f map[string]any) []map[string]any {
	var rules []map[string]any
	for _, value := range f["nftables"].([]any) {
		if raw, ok := value.(map[string]any)["rule"]; ok {
			rules = append(rules, raw.(map[string]any))
		}
	}
	return rules
}
func ruleExprMatch(rule map[string]any, n int) map[string]any {
	return rule["expr"].([]any)[n].(map[string]any)["match"].(map[string]any)
}
