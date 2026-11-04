package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// nativeNFTCommand is bound to the verified infrastructure executable by the
// private worker, after entry into the independently selected namespace. It
// is a project-test seam, never a workload/environment-selected command.
type nativeNFTCommand func(context.Context, []string, io.Reader) ([]byte, error)

// applyPlannedNativeEgress requires the caller's already durable exact plan
// and fresh namespace/peer authority. These values alone grant neither. A
// newly discovered prefix is refused, never merged after ledger publication.
// Existing tables are never adopted, replayed or replaced, even if identical.
func applyPlannedNativeEgress(ctx context.Context, plan nativeEgressPlan, namespace namespaceIdentity, observed []netip.Prefix, nft nativeNFTCommand) (networkResult, error) {
	if err := plan.validate(namespace, plan.Seed); err != nil {
		return networkResult{}, err
	}
	if err := plan.validateObserved(observed); err != nil {
		return networkResult{}, err
	}
	policy, err := GenerateEgressPolicy(plan.Inputs)
	if err != nil {
		return networkResult{}, err
	}
	present, err := nativeEgressTablePresent(ctx, nft)
	if err != nil {
		return networkResult{}, err
	}
	if present {
		return networkResult{}, errors.New("native egress refuses an existing table before fresh policy application")
	}
	if err := ctx.Err(); err != nil {
		return networkResult{}, err
	}
	if _, err := nft(ctx, []string{"-f", "-"}, strings.NewReader(policy)); err != nil {
		return networkResult{}, fmt.Errorf("native egress policy: %w", err)
	}
	readback, err := nft(ctx, []string{"-j", "list", "table", "inet", EgressTable}, nil)
	if err == nil {
		err = VerifyEgressPolicyJSON(readback, plan.Inputs)
	}
	if err != nil {
		return networkResult{}, fmt.Errorf("native egress policy: %w", err)
	}
	return networkResult{BootstrapSchema, namespace, copyNativeEgressInputs(plan.Inputs), bytes.Clone(readback)}, nil
}

type nativeEgressRetirement struct {
	Schema      int               `json:"schema"`
	Namespace   namespaceIdentity `json:"namespace"`
	RulesSHA256 string            `json:"rules_sha256"`
	TableAbsent bool              `json:"table_absent"`
	Deleted     bool              `json:"deleted"`
}

// retirePlannedNativeEgress requires the admitted parent's exact owned pin,
// protected origin and same-generation inactive-container proof after an
// actual Stop ACK. It deliberately needs no live old PID. Its result concerns
// only current table absence, never activation history or receipt completion.
// The parent still must acknowledge exact unmount and durable pin retirement.
func retirePlannedNativeEgress(ctx context.Context, plan nativeEgressPlan, namespace namespaceIdentity, nft nativeNFTCommand) (nativeEgressRetirement, error) {
	if err := plan.validate(namespace, plan.Seed); err != nil {
		return nativeEgressRetirement{}, err
	}
	present, err := nativeEgressTablePresent(ctx, nft)
	if err != nil {
		return nativeEgressRetirement{}, err
	}
	result := nativeEgressRetirement{Schema: BootstrapSchema, Namespace: namespace, RulesSHA256: plan.RulesSHA256, TableAbsent: true}
	if !present {
		return result, nil
	}
	readback, err := nft(ctx, []string{"-j", "list", "table", "inet", EgressTable}, nil)
	if err != nil {
		return nativeEgressRetirement{}, err
	}
	if err := VerifyEgressPolicyJSON(readback, plan.Inputs); err != nil {
		return nativeEgressRetirement{}, err
	}
	handle, err := nativeEgressReadbackHandle(readback)
	if err != nil {
		return nativeEgressRetirement{}, err
	}
	if err := ctx.Err(); err != nil {
		return nativeEgressRetirement{}, err
	}
	// Delete the verified kernel object, not a later table with the same name.
	// A failed/lost reply is never followed by a lookup or name-based fallback.
	if _, err := nft(ctx, []string{"delete", "table", "inet", "handle", handle}, nil); err != nil {
		return nativeEgressRetirement{}, fmt.Errorf("native egress retirement: %w", err)
	}
	present, err = nativeEgressTablePresent(ctx, nft)
	if err == nil && present {
		err = errors.New("native egress table remains after acknowledged deletion")
	}
	if err != nil {
		return nativeEgressRetirement{}, fmt.Errorf("native egress retirement: %w", err)
	}
	result.Deleted = true
	return result, nil
}

// Called only after complete semantic/owner verification above. Handles are
// incidental to policy hashing, but required for replacement-safe deletion.
func nativeEgressReadbackHandle(raw []byte) (string, error) {
	var document struct {
		Objects []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	result := ""
	for _, object := range document.Objects {
		if body, ok := object["table"]; ok {
			var table map[string]any
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if err := d.Decode(&table); err != nil || table["family"] != "inet" || table["name"] != EgressTable || !nftUnsigned(table["handle"]) || result != "" {
				return "", errors.New("owned nft readback lacks a unique kernel table handle")
			}
			result = table["handle"].(json.Number).String()
		}
	}
	value, err := strconv.ParseUint(result, 10, 64)
	if err != nil || value == 0 {
		return "", errors.New("owned nft readback lacks a positive kernel table handle")
	}
	return result, nil
}

func nativeEgressTablePresent(ctx context.Context, nft nativeNFTCommand) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if nft == nil {
		return false, errors.New("native egress lacks its fixed infrastructure executor")
	}
	raw, err := nft(ctx, []string{"-j", "list", "tables"}, nil)
	if err != nil {
		return false, err
	}
	return parseNativeEgressTableInventory(raw)
}

// Only a successful complete `nft -j list tables` response is an absence
// observation. A failed lookup's text/exit code, empty/null JSON, truncated or
// unknown inventory never stands in for it. This is not a historical ACK.
func parseNativeEgressTableInventory(raw []byte) (bool, error) {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return false, errors.New("missing or unbounded native nft inventory")
	}
	if err := checkJSONKeys(raw); err != nil {
		return false, err
	}
	var document struct {
		Objects []map[string]json.RawMessage `json:"nftables"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return false, err
	}
	if document.Objects == nil || len(document.Objects) > 16385 {
		return false, errors.New("invalid native nft inventory length")
	}
	meta, present := false, false
	seen := map[string]bool{}
	for _, object := range document.Objects {
		if len(object) != 1 {
			return false, errors.New("ambiguous native nft inventory object")
		}
		for kind, rawBody := range object {
			if kind == "metainfo" {
				var info struct {
					Version string `json:"version"`
					Release string `json:"release_name"`
					Schema  int    `json:"json_schema_version"`
				}
				d := json.NewDecoder(bytes.NewReader(rawBody))
				d.DisallowUnknownFields()
				if meta || d.Decode(&info) != nil || info.Schema != 1 || info.Version == "" {
					return false, errors.New("invalid native nft inventory metainfo")
				}
				meta = true
				continue
			}
			if kind != "table" {
				return false, errors.New("unexpected native nft inventory object")
			}
			var body map[string]any
			d := json.NewDecoder(bytes.NewReader(rawBody))
			d.UseNumber()
			if err := d.Decode(&body); err != nil || body == nil {
				return false, errors.New("invalid native nft table body")
			}
			family, familyOK := body["family"].(string)
			name, nameOK := body["name"].(string)
			if !familyOK || !nameOK || name == "" || len(name) > 255 || strings.ContainsRune(name, 0) || !nftUnsigned(body["handle"]) {
				return false, errors.New("invalid native nft table identity")
			}
			switch family {
			case "ip", "ip6", "inet", "arp", "bridge", "netdev":
			default:
				return false, errors.New("unknown native nft table family")
			}
			for key, value := range body {
				switch key {
				case "family", "name", "handle":
				case "comment":
					comment, ok := value.(string)
					if !ok || len(comment) > 4096 || strings.ContainsRune(comment, 0) {
						return false, errors.New("invalid native nft table comment")
					}
				case "flags":
					if !nativeNFTInventoryFlags(value) {
						return false, errors.New("invalid native nft table flags")
					}
				default:
					return false, errors.New("unknown native nft inventory table field")
				}
			}
			key := family + "\x00" + name
			if seen[key] {
				return false, errors.New("duplicate native nft inventory table")
			}
			seen[key] = true
			if name == EgressTable {
				if family != "inet" {
					return false, errors.New("native egress table name occurs in a foreign family")
				}
				present = true
			}
		}
	}
	if !meta {
		return false, errors.New("native nft inventory lacks its schema metainfo")
	}
	return present, nil
}

func nativeNFTInventoryFlags(value any) bool {
	values, ok := value.([]any)
	if !ok {
		values = []any{value}
	}
	if len(values) == 0 || len(values) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range values {
		flag, ok := item.(string)
		if !ok || flag == "" || len(flag) > 64 || strings.ContainsRune(flag, 0) || seen[flag] {
			return false
		}
		seen[flag] = true
	}
	return true
}
