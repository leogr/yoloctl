package openshell

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func namespacePinRecordFixture(t *testing.T) (namespacePinRecord, RuntimeRecord) {
	t.Helper()
	// This is synthetic metadata over the ordinary material fixture. No live
	// PID, namespace, mount, policy readback or operation ACK is supplied.
	f := newMaterialFixture(t)
	record := f.record
	record.Launch.UnconfinedNetwork = true
	identity := namespaceIdentity{Device: 7, Inode: 91}
	mount := namespaceIdentity{Device: 7, Inode: 92}
	target := ActivationTarget{ContainerID: strings.Repeat("a", 64), PID: 1_000_000_000, StartedAt: time.Unix(1_800_000_000, 0)}
	scope, err := newNamespacePinScope(record, target, "12345", identity, mount, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	seed := EgressInputs{egressOwner(record), []netip.Addr{netip.MustParseAddr("192.168.127.1")}, []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24")}}
	plan, err := newNativeEgressPlan(identity, seed, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := newNamespacePinRecord(scope, record, target.ContainerID, namespacePinPlaceholder{Device: 8, Inode: 101}, plan)
	if err != nil {
		t.Fatal(err)
	}
	return pin, record
}

func TestNamespacePinRecordBindsImmutableOriginAndColdGeneration(t *testing.T) {
	pin, record := namespacePinRecordFixture(t)
	raw, _ := json.Marshal(pin)
	if _, err := decodeNamespacePinRecord(raw, record, pin.Scope.ContainerID, pin.OriginSHA256); err != nil {
		t.Fatal("cold structural record must not require a live original PID", err)
	}
	for _, change := range []string{"cid", "record", "namespace", "mount-namespace", "boot", "start", "placeholder", "plan", "origin"} {
		t.Run(change, func(t *testing.T) {
			p, record := namespacePinRecordFixture(t)
			origin := p.OriginSHA256
			switch change {
			case "cid":
				p.Scope.ContainerID = strings.Repeat("b", 64)
			case "record":
				p.Scope.RecordSHA256 = strings.Repeat("b", 64)
			case "namespace":
				p.Scope.Namespace.Inode++
			case "mount-namespace":
				p.Scope.InfrastructureMount.Inode++
			case "boot":
				p.Scope.KernelBootID = "22222222-2222-4333-8444-555555555555"
			case "start":
				p.Scope.PeerStartTicks = "12346"
			case "placeholder":
				p.Placeholder.Inode++
			case "plan":
				p.Plan.Inputs.TrustedResolvers[0] = netip.MustParseAddr("8.8.8.8")
			case "origin":
				p.OriginSHA256 = strings.Repeat("b", 64)
			}
			// Rehashing the mutable record cannot replace the separately
			// obtained immutable origin anchor.
			p.OriginSHA256, _ = p.originIdentity()
			if change == "origin" {
				origin = strings.Repeat("b", 64)
			}
			raw, _ := json.Marshal(p)
			got, err := decodeNamespacePinRecord(raw, record, strings.Repeat("a", 64), origin)
			if err == nil || got.Schema != 0 || got.Scope.ContainerID != "" {
				t.Fatal("changed origin returned a usable record", err)
			}
		})
	}
}

func TestNamespacePinRecordPhaseDistinguishesPlanMountAndReadback(t *testing.T) {
	for _, test := range []struct {
		name      string
		phase     namespacePinPhase
		directory uint64
		mount     uint64
		hash      bool
		valid     bool
	}{
		{"planned", pinPlanned, 0, 0, false, true},
		{"planned-claims-directory", pinPlanned, 6, 0, false, false},
		{"planned-claims-mount", pinPlanned, 0, 7, false, false},
		{"planned-claims-rules", pinPlanned, 0, 0, true, false},
		{"directory-bind-attempt", pinDirectoryBindAttempt, 0, 0, false, true},
		{"directory-bind-attempt-claims-ack", pinDirectoryBindAttempt, 6, 0, false, false},
		{"directory-bound", pinDirectoryBound, 6, 0, false, true},
		{"directory-bound-without-id", pinDirectoryBound, 0, 0, false, false},
		{"private-attempt", pinDirectoryPrivateAttempt, 6, 0, false, true},
		{"directory-private", pinDirectoryPrivate, 6, 0, false, true},
		{"namespace-attempt", pinNamespaceBindAttempt, 6, 0, false, true},
		{"namespace-attempt-claims-ack", pinNamespaceBindAttempt, 6, 7, false, false},
		{"partial-private-attempt", pinPartialPrivateAttempt, 6, 0, false, true},
		{"partial-private", pinPartialPrivate, 6, 0, false, true},
		{"partial-unmount-attempt", pinPartialUnmountAttempt, 6, 0, false, true},
		{"partial-unmounted", pinPartialUnmounted, 6, 0, false, true},
		{"partial-claims-namespace", pinPartialUnmounted, 6, 7, false, false},
		{"partial-claims-policy", pinPartialUnmounted, 6, 0, true, false},
		{"directory-claims-pin", pinDirectoryPrivate, 6, 7, false, false},
		{"pinned", pinPresent, 6, 7, false, true},
		{"pinned-without-directory", pinPresent, 0, 7, false, false},
		{"pinned-same-mount", pinPresent, 7, 7, false, false},
		{"pinned-without-mount", pinPresent, 6, 0, false, false},
		{"pinned-claims-readback", pinPresent, 6, 7, true, false},
		{"policy-attempt", pinPolicyApplyAttempt, 6, 7, false, true},
		{"policy-attempt-claims-readback", pinPolicyApplyAttempt, 6, 7, true, false},
		{"readback", pinPolicyReadback, 6, 7, true, true},
		{"readback-missing-rules", pinPolicyReadback, 6, 7, false, false},
		{"retire-before-policy", pinRetiring, 6, 7, false, true},
		{"retire-after-policy", pinRetiring, 6, 7, true, true},
		{"retire-missing-mount", pinRetiring, 6, 0, false, false},
		{"absent-partial-policy", pinTableAbsent, 6, 7, false, true},
		{"pin-unmount-attempt", pinUnmountAttempt, 6, 7, true, true},
		{"unmounted", pinUnmounted, 6, 7, true, true},
		{"directory-unmount-attempt", pinDirectoryUnmountAttempt, 6, 7, true, true},
		{"directory-unmounted", pinDirectoryUnmounted, 6, 7, true, true},
		{"directory-unmounted-without-pin", pinDirectoryUnmounted, 6, 0, false, false},
		{"unknown", "activated", 6, 7, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin, record := namespacePinRecordFixture(t)
			pin.Phase, pin.DirectoryMountID, pin.PinMountID = test.phase, test.directory, test.mount
			if test.hash {
				pin.ReadbackSHA256 = pin.Plan.RulesSHA256
			}
			if err := pin.validate(record, pin.Scope.ContainerID, pin.OriginSHA256); (err == nil) != test.valid {
				t.Fatal("wrong structural phase result", err)
			}
		})
	}
}

func TestNamespacePinRecordRejectsAmbiguousJSONAndClonesPlan(t *testing.T) {
	pin, record := namespacePinRecordFixture(t)
	plan := pin.Plan
	copy, err := newNamespacePinRecord(pin.Scope, record, pin.Scope.ContainerID, pin.Placeholder, plan)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(copy)
	plan.Inputs.LocalPrefixes[0] = netip.MustParsePrefix("192.0.2.0/24")
	plan.Seed.TrustedResolvers[0] = netip.MustParseAddr("8.8.8.8")
	plan.Observed[0] = netip.MustParsePrefix("198.51.100.0/24")
	after, _ := json.Marshal(copy)
	if !bytes.Equal(before, after) {
		t.Fatal("external plan slices changed the retained immutable origin")
	}
	for _, raw := range [][]byte{
		append(append([]byte{}, before...), before...),
		append([]byte(`{"schema":1,`), before[1:]...),
		append([]byte(`{"unknown":true,`), before[1:]...),
		[]byte(`null`),
	} {
		if got, err := decodeNamespacePinRecord(raw, record, copy.Scope.ContainerID, copy.OriginSHA256); err == nil || got.Schema != 0 {
			t.Fatal("ambiguous/unknown/trailing data returned a record", err)
		}
	}
}

func TestNamespacePinBootClassificationIsMetadataOnlyAndRejectsUnknown(t *testing.T) {
	pin, _ := namespacePinRecordFixture(t)
	raw, _ := json.Marshal(pin)
	if relation, err := pin.Scope.classifyBoot(pin.Scope.KernelBootID); err != nil || relation != pinSameBoot {
		t.Fatal(relation, err)
	}
	if relation, err := pin.Scope.classifyBoot("22222222-2222-4333-8444-555555555555"); err != nil || relation != pinDifferentBoot {
		t.Fatal(relation, err)
	}
	for _, invalid := range []string{"", "not-a-boot-id", "00000000-0000-0000-0000-000000000000"} {
		if relation, err := pin.Scope.classifyBoot(invalid); err == nil || relation != pinBootUnknown {
			t.Fatal("invalid boot identity became an actionable classification", relation, err)
		}
	}
	after, _ := json.Marshal(pin)
	if !bytes.Equal(raw, after) || pin.Phase != pinPlanned || pin.PinMountID != 0 {
		t.Fatal("boot comparison manufactured phase progress or a mount")
	}
}
