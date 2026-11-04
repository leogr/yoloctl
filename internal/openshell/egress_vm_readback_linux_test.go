package openshell

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// These exact bytes were captured read-only from DevOps run 9's retained
// namespace after the apply-attempt failure (nftables 1.0.9). This is an
// attributed readback fixture, not a new kernel execution.
func TestEgressVMReadbackRequiresInstalledOwner(t *testing.T) {
	raw, err := os.ReadFile("testdata/nft-1.0.9-ngo-readback.json")
	if err != nil {
		t.Fatal(err)
	}
	inputRaw, err := os.ReadFile("testdata/nft-1.0.9-ngo-inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var input EgressInputs
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEgressPolicyJSON(raw, input); err == nil {
		t.Fatal("unmarked actual table accepted")
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	nftBody(fixture, "table", "")["comment"] = "yoloctl:" + input.OwnerSHA256
	marked, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEgressPolicyJSON(marked, input); err != nil {
		t.Fatalf("all remaining actual readback must match after adding ONLY the missing owner: %v", err)
	}
}

// Run 10 established that create-with-children installs only the marked table
// on nft 1.0.9. Exact ownership must never turn that empty policy into success.
func TestEgressVMReadbackRejectsMarkedEmptyTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/nft-1.0.9-ngo-empty-readback.json")
	if err != nil {
		t.Fatal(err)
	}
	inputRaw, err := os.ReadFile("testdata/nft-1.0.9-ngo-empty-inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var input EgressInputs
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEgressPolicyJSON(raw, input); err == nil {
		t.Fatal("marked empty actual table accepted")
	}
	policy, err := GenerateEgressPolicy(input)
	if err != nil {
		t.Fatal(err)
	}
	first, rest, ok := strings.Cut(policy, "\n")
	if !ok || first != fmt.Sprintf("create table inet %s { comment \"yoloctl:%s\"; }", EgressTable, input.OwnerSHA256) ||
		!strings.HasPrefix(rest, "table inet "+EgressTable+" {\n") {
		t.Fatal("exclusive marked create and child-expanding declaration are not separate commands", policy)
	}
	for _, child := range []string{"set denied_v4", "set denied_v6", "counter denied_ipv4", "counter denied_ipv6", "chain output", "policy drop", "meta nfproto ipv6 accept"} {
		if !strings.Contains(rest, child) || strings.Contains(first, child) {
			t.Fatal("planned child missing from declarative add-table body", child)
		}
	}
}
