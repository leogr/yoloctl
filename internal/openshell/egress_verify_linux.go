package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The JSON forms follow the pinned nftables 1.0.9 libnftables-json schema and
// src/json.c. Source: https://netfilter.org/projects/nftables/files/nftables-1.0.9.tar.xz
// Actual kernel readback still belongs to G3; synthetic unit fixtures do not
// establish that a particular VM's nft emits the qualified representation.
type nftObject map[string]any

func nftMatch(left, right any, op string) nftObject {
	return nftObject{"match": nftObject{"left": left, "right": right, "op": op}}
}
func nftMeta(key string) nftObject { return nftObject{"meta": nftObject{"key": key}} }
func nftPayload(protocol, field string) nftObject {
	return nftObject{"payload": nftObject{"protocol": protocol, "field": field}}
}
func nftPrefix(prefix string) nftObject {
	p := netip.MustParsePrefix(prefix)
	return nftObject{"prefix": nftObject{"addr": p.Addr().String(), "len": p.Bits()}}
}

func (p egressPlan) expectedObjects() []nftObject {
	objects := []nftObject{
		{"table": nftObject{"family": "inet", "name": EgressTable, "comment": "yoloctl:" + p.owner}},
		{"chain": nftObject{"family": "inet", "table": EgressTable, "name": "output", "type": "filter", "hook": "output", "prio": 0, "policy": "drop"}},
	}
	set := func(name, kind string, entries []string, interval bool) {
		values := make([]any, 0, len(entries))
		for _, entry := range entries {
			if interval {
				values = append(values, nftPrefix(entry))
			} else {
				values = append(values, entry)
			}
		}
		body := nftObject{"family": "inet", "table": EgressTable, "name": name, "type": kind, "elem": values}
		if interval {
			body["flags"] = []string{"interval"}
		}
		objects = append(objects, nftObject{"set": body})
	}
	set("denied_v4", "ipv4_addr", p.denied4, true)
	set("denied_v6", "ipv6_addr", p.denied6, true)
	if len(p.resolvers4) > 0 {
		set("resolver_v4", "ipv4_addr", p.resolvers4, false)
	}
	if len(p.resolvers6) > 0 {
		set("resolver_v6", "ipv6_addr", p.resolvers6, false)
	}
	for _, name := range []string{"denied_ipv4", "denied_ipv6"} {
		objects = append(objects, nftObject{"counter": nftObject{"family": "inet", "table": EgressTable, "name": name}})
	}
	rule := func(expressions ...any) {
		objects = append(objects, nftObject{"rule": nftObject{"family": "inet", "table": EgressTable, "chain": "output", "expr": expressions}})
	}
	accept := nftObject{"accept": nil}
	rule(nftMatch(nftMeta("oifname"), "lo", "=="), nftMatch(nftPayload("ip", "daddr"), nftPrefix("127.0.0.0/8"), "=="), accept)
	rule(nftMatch(nftMeta("oifname"), "lo", "=="), nftMatch(nftPayload("ip6", "daddr"), "::1", "=="), accept)
	rule(nftMatch(nftObject{"ct": nftObject{"key": "direction"}}, "reply", "=="), nftMatch(nftObject{"ct": nftObject{"key": "state"}}, []string{"established", "related"}, "in"), accept)
	for _, protocol := range []string{"udp", "tcp"} {
		for _, family := range []struct {
			proto, set string
			resolvers  []string
		}{{"ip", "resolver_v4", p.resolvers4}, {"ip6", "resolver_v6", p.resolvers6}} {
			if len(family.resolvers) > 0 {
				rule(nftMatch(nftPayload(family.proto, "daddr"), "@"+family.set, "=="), nftMatch(nftPayload(protocol, "dport"), 53, "=="), accept)
			}
		}
	}
	rule(nftMatch(nftPayload("ip", "daddr"), "@denied_v4", "=="), nftObject{"counter": "denied_ipv4"}, nftObject{"drop": nil})
	rule(nftMatch(nftPayload("ip6", "daddr"), "@denied_v6", "=="), nftObject{"counter": "denied_ipv6"}, nftObject{"drop": nil})
	rule(nftMatch(nftMeta("nfproto"), "ipv4", "=="), accept)
	rule(nftMatch(nftMeta("nfproto"), "ipv6", "=="), accept)
	return objects
}

// VerifyEgressPolicyJSON compares the complete installed table with the
// selected plan. It ignores only kernel object handles, named counter values,
// hash-set capacity and ordering of unordered objects/set elements. Rule and
// expression order remain exact. Unknown objects/fields are never omitted.
func VerifyEgressPolicyJSON(raw []byte, input EgressInputs) error {
	p, err := prepareEgress(input)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 2<<20 {
		return errors.New("unbounded or missing nft rule readback")
	}
	if err := checkJSONKeys(raw); err != nil {
		return err
	}
	var document struct {
		Objects []map[string]json.RawMessage `json:"nftables"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	var actual []nftObject
	metaSeen := false
	for _, entry := range document.Objects {
		if len(entry) != 1 {
			return errors.New("invalid nft readback object")
		}
		for kind, rawBody := range entry {
			if kind == "metainfo" {
				if metaSeen {
					return errors.New("duplicate nft metainfo")
				}
				metaSeen = true
				var meta struct {
					Version string `json:"version"`
					Release string `json:"release_name"`
					Schema  int    `json:"json_schema_version"`
				}
				if json.Unmarshal(rawBody, &meta) != nil || meta.Schema != 1 {
					return errors.New("unqualified nft JSON schema")
				}
				continue
			}
			switch kind {
			case "table", "chain", "set", "rule", "counter":
			default:
				return fmt.Errorf("unexpected nft object %q", kind)
			}
			var body map[string]any
			d := json.NewDecoder(bytes.NewReader(rawBody))
			d.UseNumber()
			if err := d.Decode(&body); err != nil || body == nil {
				return errors.New("invalid nft object body")
			}
			if body["family"] != "inet" || kind != "table" && body["table"] != EgressTable {
				return errors.New("nft readback belongs to another table")
			}
			if handle, ok := body["handle"]; ok {
				if !nftUnsigned(handle) {
					return errors.New("invalid nft handle")
				}
				delete(body, "handle")
			}
			if kind == "counter" {
				for _, name := range []string{"packets", "bytes"} {
					if value, ok := body[name]; ok {
						if !nftUnsigned(value) {
							return errors.New("invalid named counter value")
						}
						delete(body, name)
					}
				}
			}
			if kind == "set" {
				if value, ok := body["size"]; ok {
					if !nftUnsigned(value) {
						return errors.New("invalid nft set capacity")
					}
					delete(body, "size")
				}
			}
			actual = append(actual, nftObject{kind: body})
		}
	}
	want, err := canonicalNFTObjects(p.expectedObjects())
	if err != nil {
		return err
	}
	got, err := canonicalNFTObjects(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("installed nft table differs from the selected runtime egress plan")
	}
	return nil
}

func nftUnsigned(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	_, err := strconv.ParseUint(string(number), 10, 64)
	return err == nil
}

func canonicalNFTObjects(objects []nftObject) ([]byte, error) {
	// Marshal/decode once so expected and readback maps have identical JSON
	// value types; no expression or address is converted by ad-hoc strings.
	raw, err := json.Marshal(objects)
	if err != nil {
		return nil, err
	}
	var normalized []map[string]map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&normalized); err != nil {
		return nil, err
	}
	var unordered []string
	var rules []any
	for _, entry := range normalized {
		for kind, body := range entry {
			if kind == "rule" {
				rules = append(rules, body)
				continue
			}
			if kind == "set" {
				values, ok := body["elem"].([]any)
				if !ok {
					return nil, errors.New("missing nft set elements")
				}
				var encoded []string
				for _, value := range values {
					raw, err := json.Marshal(value)
					if err != nil {
						return nil, err
					}
					encoded = append(encoded, string(raw))
				}
				slices.Sort(encoded)
				body["elem"] = encoded
			}
			raw, err := json.Marshal(map[string]any{kind: body})
			if err != nil {
				return nil, err
			}
			unordered = append(unordered, string(raw))
		}
	}
	slices.Sort(unordered)
	return json.Marshal(struct {
		Objects string
		Rules   []any
	}{strings.Join(unordered, "\n"), rules})
}
