package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
)

// Pure metadata fixtures only; NO native context, receipt/source/ACK factory.
func missingCIDMetadataFixture() missingCIDMetadataSample {
	return missingCIDMetadataSample{native: openshell.MissingCIDNativeMetadata{Box: "box", Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "native-uuid", Prefix: openshell.NativeOriginPrefixPresent}, registrations: operator.RegistrationScopeObservation{Kind: operator.RegistrationScopeRootPresent}}
}
func TestMissingCIDMetadataDistinctAbsenceLegacyNativeAndRuntimeCandidates(t *testing.T) {
	endpoint := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)}
	for _, kind := range []string{"absent", "legacy", "native-tag", "native-origin", "native-claim", "runtime", "projection", "neutral", "mixed-keep"} {
		t.Run(kind, func(t *testing.T) {
			sample := missingCIDMetadataFixture()
			want := missingCIDAbsentMetadataCandidate
			switch kind {
			case "legacy":
				sample.registrations.Identities = []operator.RegistrationIdentity{operator.LegacyRegistrationIdentity(endpoint)}
				want = missingCIDLegacyMetadataCandidate
			case "native-tag":
				sample.registrations.Identities = []operator.RegistrationIdentity{{Endpoint: endpoint, Mode: operator.RegistrationOpenShellNative, HelperProtocol: 2, Qualification: strings.Repeat("d", 64)}}
				want = missingCIDNativeEvidence
			case "native-origin":
				sample.native.Origins = []openshell.NativeOriginIdentity{{Space: sample.native.Space, DiskID: sample.native.DiskID, DiskUUID: sample.native.DiskUUID, Box: "box", Incarnation: endpoint.Incarnation, Qualification: strings.Repeat("c", 64), HelperSHA256: strings.Repeat("d", 64)}}
				want = missingCIDNativeEvidence
			case "native-claim":
				sample.claims = []controller.MissingCIDClaimObservation{{Endpoint: endpoint, Native: true}}
				want = missingCIDNativeEvidence
			case "runtime":
				sample.runtimeCIDs = []string{strings.Repeat("e", 64)}
				want = missingCIDCurrentRuntimeEvidence
			case "projection":
				sample.registrations.Identities = []operator.RegistrationIdentity{operator.LegacyRegistrationIdentity(endpoint)}
				sample.mounts = []container.NativeMountEntry{{ContainerID: strings.Repeat("e", 64), Mounts: []container.NativeMount{{Source: endpoint.Directory(), Options: []string{"ro"}}}}}
				want = missingCIDSelectedProjectionEvidence
			case "neutral":
				sample.native.BareScaffolds = 1
				sample.native.Prefix = openshell.NativeOriginMissingContainers
				sample.registrations = operator.RegistrationScopeObservation{Kind: operator.RegistrationScopePrefixAbsent, MissingComponent: "operators"}
			case "mixed-keep":
				sample.registrations.Identities = []operator.RegistrationIdentity{operator.LegacyRegistrationIdentity(endpoint), {Endpoint: operator.Endpoint{Box: "box", Incarnation: strings.Repeat("f", 32)}, Mode: operator.RegistrationOpenShellNative, HelperProtocol: 2, Qualification: strings.Repeat("d", 64)}}
				want = missingCIDNativeEvidence
			}
			result, err := classifyMissingCIDMetadata("box", sample)
			if err != nil || result.kind != want {
				t.Fatal("metadata evidence mapped incorrectly", result.kind, err)
			}
			if len(result.pending) != 4 {
				t.Fatal("metadata erased pending original receipt/projection/finalization joins")
			}
		})
	}
}
func TestMissingCIDMetadataUnknownUnavailableForeignAndPartialAreErrors(t *testing.T) {
	for _, kind := range []string{"unavailable-root", "partial-absence", "partial-native", "foreign-selection", "bad-runtime", "bad-tag"} {
		t.Run(kind, func(t *testing.T) {
			sample := missingCIDMetadataFixture()
			switch kind {
			case "unavailable-root":
				sample.registrations = operator.RegistrationScopeObservation{}
			case "partial-absence":
				sample.registrations = operator.RegistrationScopeObservation{Kind: operator.RegistrationScopePrefixAbsent}
			case "partial-native":
				sample.native.Origins = []openshell.NativeOriginIdentity{{Space: sample.native.Space, DiskID: sample.native.DiskID, DiskUUID: sample.native.DiskUUID, Box: "box", Incarnation: strings.Repeat("b", 32)}}
			case "foreign-selection":
				sample.native.Origins = []openshell.NativeOriginIdentity{{Space: "other"}}
			case "bad-runtime":
				sample.runtimeCIDs = []string{"short-name"}
			case "bad-tag":
				sample.registrations.Identities = []operator.RegistrationIdentity{{Endpoint: operator.Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)}}}
			}
			if _, err := classifyMissingCIDMetadata("box", sample); err == nil {
				t.Fatal("unknown/partial source became legacy/absence candidate")
			}
		})
	}
}
func TestMissingCIDMetadataPriorFailedEffectDominatesCurrentAbsence(t *testing.T) {
	failed := errors.New("original Stop/remove reply uncertain")
	if result, err := sampleMissingCIDMetadata(context.Background(), "box", nil, nil, nil, nil, failed); !errors.Is(err, failed) || result.kind != 0 {
		t.Fatal("current empty input settled original failed effect", err)
	}
	if _, err := sampleMissingCIDMetadata(context.Background(), "box", nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing actual readers became empty census")
	}
}
