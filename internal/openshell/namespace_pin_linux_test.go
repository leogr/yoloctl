package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
)

var namespaceCreateSequence = []string{
	"check-1", "persist:namespace-bind-attempt:0:", "check-2", "bind", "read-pin", "persist:pin-present:23:",
	"check-3", "persist:policy-apply-attempt:23:", "check-4", "apply", "verify-policy", "check-5",
	"persist:policy-readback:23:" + strings.Repeat("a", 64), "check-6",
}

func pinNamespaceScript(fail string, events *[]string, cause error) pinNamespaceHooks {
	step := func(name string) error {
		*events = append(*events, name)
		if name == fail {
			return cause
		}
		return nil
	}
	checks := 0
	return pinNamespaceHooks{
		check: func() error { checks++; return step(fmt.Sprintf("check-%d", checks)) },
		persist: func(phase namespacePinPhase, mount uint64, hash string) error {
			return step(fmt.Sprintf("persist:%s:%d:%s", phase, mount, hash))
		},
		bind: func() error { return step("bind") },
		readPin: func() (uint64, error) {
			if err := step("read-pin"); err != nil {
				return 0, err
			}
			if fail == "zero-pin" {
				return 0, nil
			}
			return 23, nil
		},
		apply: func() error { return step("apply") },
		verifyPolicy: func() (activationProof, error) {
			if err := step("verify-policy"); err != nil {
				return activationProof{}, err
			}
			rules := strings.Repeat("a", 64)
			if fail == "wrong-policy" {
				rules = strings.Repeat("b", 64)
			}
			return activationProof{Schema: BootstrapSchema, RulesSHA256: rules}, nil // synthetic protocol callback only
		},
	}
}

func TestNamespacePinProtocolOrdersActualBindingPolicyAndDurableReadback(t *testing.T) {
	var events []string
	progress, proof, err := runPinNamespaceCreate(context.Background(), strings.Repeat("a", 64), pinNamespaceScript("", &events, nil))
	if err != nil || !progress.ready || !progress.bindAcknowledged || !progress.pinRecorded || progress.mount != 23 || !progress.policyAcknowledged || !progress.readbackRecorded || proof.RulesSHA256 != strings.Repeat("a", 64) || !reflect.DeepEqual(events, namespaceCreateSequence) {
		t.Fatal("namespace protocol ordering/ACKs", progress, proof, err, events)
	}
}

func TestNamespacePinProtocolFailedACKNeverRetriesOrAuthorizes(t *testing.T) {
	for index, where := range namespaceCreateSequence {
		t.Run(where, func(t *testing.T) {
			var events []string
			cause := errors.New("lost reply at " + where)
			progress, proof, err := runPinNamespaceCreate(context.Background(), strings.Repeat("a", 64), pinNamespaceScript(where, &events, cause))
			var uncertain *NativeEffectsUncertain
			if err == nil || !errors.Is(err, cause) || errors.As(err, &uncertain) == strings.HasPrefix(where, "check-") || progress.ready || proof.Schema != 0 || !reflect.DeepEqual(events, namespaceCreateSequence[:index+1]) {
				t.Fatal("failed reply continued/reclassified/returned proof", progress, proof, err, events)
			}
			if progress.bindAttempted != (index >= 3) || progress.bindAcknowledged != (index > 3) || progress.pinRecorded != (index > 5) || progress.policyAttempted != (index >= 9) || progress.policyAcknowledged != (index > 9) || progress.readbackRecorded != (index > 12) {
				t.Fatal("phase/attempt manufactured positive ACK", progress, where)
			}
		})
	}
}

func TestNamespacePinProtocolZeroOrWrongReadbackRefusesAndCancellationHasNoEffects(t *testing.T) {
	for _, where := range []string{"zero-pin", "wrong-policy"} {
		var events []string
		progress, proof, err := runPinNamespaceCreate(context.Background(), strings.Repeat("a", 64), pinNamespaceScript(where, &events, nil))
		var uncertain *NativeEffectsUncertain
		end := 5
		if where == "wrong-policy" {
			end = 11
		}
		if !errors.As(err, &uncertain) || progress.ready || proof.Schema != 0 || !reflect.DeepEqual(events, namespaceCreateSequence[:end]) {
			t.Fatal("invalid readback accepted/continued", progress, proof, err, events)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []string
	if progress, proof, err := runPinNamespaceCreate(ctx, strings.Repeat("a", 64), pinNamespaceScript("", &events, nil)); !errors.Is(err, context.Canceled) || progress.ready || proof.Schema != 0 || len(events) != 0 {
		t.Fatal("canceled namespace protocol reached callbacks", progress, err, events)
	}
	if _, _, err := runPinNamespaceCreate(context.Background(), strings.Repeat("a", 64), pinNamespaceHooks{}); err == nil {
		t.Fatal("missing callbacks accepted")
	}
}

func namespacePolicyResultFixture(t *testing.T) (namespacePinRecord, RuntimeRecord, []byte) {
	t.Helper()
	pin, record := namespacePinRecordFixture(t)
	prepared, err := prepareEgress(pin.Plan.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := json.Marshal(map[string]any{"nftables": append([]nftObject{{"metainfo": nftObject{"version": "1.0.9", "json_schema_version": 1}}}, prepared.expectedObjects()...)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(networkResult{BootstrapSchema, pin.Scope.Namespace, copyNativeEgressInputs(pin.Plan.Inputs), readback})
	if err != nil {
		t.Fatal(err)
	}
	return pin, record, raw
}

func TestNativeEgressParentRequiresExactChildPolicyProof(t *testing.T) {
	pin, record, raw := namespacePolicyResultFixture(t)
	proof, err := decodePlannedNetworkResult(raw, pin.Plan, record)
	if err != nil || proof.validate(record, pin.Scope.Namespace) != nil || proof.RulesSHA256 != pin.Plan.RulesSHA256 {
		t.Fatal("complete child result refused", proof, err)
	}
	proof.Inputs.LocalPrefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
	if sameNativeEgressInputs(*proof.Inputs, pin.Plan.Inputs) {
		t.Fatal("proof aliases original plan")
	}
	for _, change := range []string{"schema", "namespace", "owner", "resolvers", "prefixes", "readback", "plan", "confined", "unknown", "duplicate", "trailing", "empty"} {
		t.Run(change, func(t *testing.T) {
			pin, record, raw := namespacePolicyResultFixture(t)
			var result networkResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "schema":
				result.Schema++
			case "namespace":
				result.Namespace.Inode++
			case "owner":
				result.Inputs.OwnerSHA256 = strings.Repeat("b", 64)
			case "resolvers":
				result.Inputs.TrustedResolvers = []netip.Addr{netip.MustParseAddr("8.8.8.8")}
			case "prefixes":
				result.Inputs.LocalPrefixes = nil
			case "readback":
				result.Readback = []byte(`{"nftables":[]}`)
			case "plan":
				pin.Plan.RulesSHA256 = strings.Repeat("b", 64)
			case "confined":
				record.Launch.UnconfinedNetwork = false
			}
			raw, _ = json.Marshal(result)
			switch change {
			case "unknown":
				raw = append([]byte(`{"unknown":1,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(fmt.Sprintf(`{"schema":%d,`, BootstrapSchema)), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "empty":
				raw = nil
			}
			if got, err := decodePlannedNetworkResult(raw, pin.Plan, record); err == nil || got.Schema != 0 || got.Inputs != nil {
				t.Fatal("changed child result returned proof", got, err)
			}
		})
	}
}

func TestNativeEgressParentRefusesVMSeedChangesWithoutRewritingPlan(t *testing.T) {
	pin, record := namespacePinRecordFixture(t)
	before, _ := json.Marshal(pin.Plan)
	current := infrastructureInputs{resolvers: append([]netip.Addr(nil), pin.Plan.Seed.TrustedResolvers...), prefixes: append([]netip.Prefix(nil), pin.Plan.Seed.LocalPrefixes...)}
	if err := validateNativeEgressSeed(pin.Plan, record, current); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"resolver", "prefix", "record"} {
		selected := current
		r := record
		switch change {
		case "resolver":
			selected.resolvers = []netip.Addr{netip.MustParseAddr("8.8.8.8")}
		case "prefix":
			selected.prefixes = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
		case "record":
			r.Launch.Workdir = "/another"
		}
		if err := validateNativeEgressSeed(pin.Plan, r, selected); err == nil {
			t.Fatal("changed VM/record seed accepted", change)
		}
	}
	after, _ := json.Marshal(pin.Plan)
	if string(before) != string(after) {
		t.Fatal("VM seed validation rewrote durable plan")
	}
}

func TestNamespacePinEntryRefusesMissingOrConsumedDirectoryAndClosesFD(t *testing.T) {
	if attempt, err := createNativeNamespacePin(context.Background(), nil, nil, nil, nil); err == nil || attempt != nil {
		t.Fatal("missing directory proof accepted")
	}
	dir := &nativePinDirectoryAttempt{namespaceConsumed: true}
	if attempt, err := createNativeNamespacePin(context.Background(), nil, dir, nil, nil); err == nil || attempt != nil {
		t.Fatal("consumed directory reused")
	}
	file, err := os.CreateTemp(t.TempDir(), "close-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	attempt := &nativePinAttempt{namespace: file, directory: dir}
	if err := attempt.Close(); err != nil || !attempt.closed || attempt.namespace != nil {
		t.Fatal("pin Close did not release descriptor", err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("pin metadata Close leaked ordinary FD", err)
	}
	if err := attempt.Close(); err != nil {
		t.Fatal("pin Close not idempotent", err)
	}
}
