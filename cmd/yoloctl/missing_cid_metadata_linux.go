package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
)

type missingCIDMetadataKind uint8

const (
	missingCIDNativeEvidence missingCIDMetadataKind = iota + 1
	missingCIDCurrentRuntimeEvidence
	missingCIDSelectedProjectionEvidence
	missingCIDLegacyMetadataCandidate
	missingCIDAbsentMetadataCandidate
)

type missingCIDRequiredJoin uint8

const (
	missingCIDOriginalExistingReceiptRoot missingCIDRequiredJoin = iota + 1
	missingCIDOriginalParentPhysicalRead
	missingCIDManagedProjectionIdentityJoin
	missingCIDWholeOwnerFinalization
)

// NO cleanup/Stop/remove/legacy-eligibility token can be obtained from this type.
// The absent original receipt-root/physical-parent/managed projection/finalizer
// constructors are explicit remaining requirements, not successful callbacks.
type missingCIDMetadataResult struct {
	box                          string
	kind                         missingCIDMetadataKind
	nativeOrigins                []openshell.NativeOriginIdentity
	nativeRegistrations          []operator.RegistrationIdentity
	nativeClaims                 []operator.Endpoint
	legacyRegistrations          []operator.RegistrationIdentity
	legacyClaims                 []operator.Endpoint
	runtimeCIDs                  []string
	ociMounts                    []container.NativeMountEntry //private full source metadata; never render
	registrationScope            operator.RegistrationScopeKind
	missingRegistrationComponent string
	nativePrefix                 openshell.NativeOriginPrefixState
	bareScaffolds                int
	pending                      []missingCIDRequiredJoin
}

func (missingCIDMetadataResult) String() string {
	return "missing-CID PRIMARY metadata candidate [no cleanup or historical ACK]"
}

type missingCIDMetadataSample struct {
	native        openshell.MissingCIDNativeMetadata
	registrations operator.RegistrationScopeObservation
	claims        []controller.MissingCIDClaimObservation
	runtimeCIDs   []string
	mounts        []container.NativeMountEntry
}

// Settled predicate only: validated target-positive evidence dominates old
// legacy metadata and neutral scaffolds. No absence=>legacy or historical ACK.
func classifyMissingCIDMetadata(box string, sample missingCIDMetadataSample) (missingCIDMetadataResult, error) {
	if layout.ValidateName("agentbox", box) != nil || sample.native.Box != box || sample.native.Space == "" || sample.native.DiskID == "" || sample.native.DiskUUID == "" {
		return missingCIDMetadataResult{}, errors.New("missing-CID sample differs from actual original selected scope")
	}
	switch sample.native.Prefix {
	case openshell.NativeOriginMissingRuntime, openshell.NativeOriginMissingControl, openshell.NativeOriginMissingContainers, openshell.NativeOriginPrefixPresent:
	default:
		return missingCIDMetadataResult{}, errors.New("native metadata prefix unavailable")
	}
	switch sample.registrations.Kind {
	case operator.RegistrationScopeRootPresent:
		if sample.registrations.MissingComponent != "" {
			return missingCIDMetadataResult{}, errors.New("mixed registration scope")
		}
	case operator.RegistrationScopePrefixAbsent:
		if len(sample.registrations.Identities) != 0 || sample.registrations.MissingComponent != "run" && sample.registrations.MissingComponent != "yoloctl" && sample.registrations.MissingComponent != "operators" {
			return missingCIDMetadataResult{}, errors.New("unknown/partial absent registration prefix")
		}
	default:
		return missingCIDMetadataResult{}, errors.New("registration source unavailable, never empty")
	}
	result := missingCIDMetadataResult{box: box, registrationScope: sample.registrations.Kind, missingRegistrationComponent: sample.registrations.MissingComponent, nativePrefix: sample.native.Prefix, bareScaffolds: sample.native.BareScaffolds, runtimeCIDs: slices.Clone(sample.runtimeCIDs), pending: []missingCIDRequiredJoin{missingCIDOriginalExistingReceiptRoot, missingCIDOriginalParentPhysicalRead, missingCIDManagedProjectionIdentityJoin, missingCIDWholeOwnerFinalization}}
	if sample.native.BareScaffolds < 0 {
		return missingCIDMetadataResult{}, errors.New("unknown scaffold summary")
	}
	if sample.native.Prefix != openshell.NativeOriginPrefixPresent && (len(sample.native.Origins) != 0 || len(sample.native.PayloadIncarnations) != 0) {
		return missingCIDMetadataResult{}, errors.New("mixed missing-prefix/native payload state")
	}
	seen := map[string]bool{}
	for _, origin := range sample.native.Origins {
		if origin.Space != sample.native.Space || origin.DiskID != sample.native.DiskID || origin.DiskUUID != sample.native.DiskUUID || layout.ValidateName("agentbox", origin.Box) != nil || !missingCIDHex(origin.Incarnation, 32) || !missingCIDHex(origin.Qualification, 64) || !missingCIDHex(origin.HelperSHA256, 64) || seen[origin.Incarnation] {
			return missingCIDMetadataResult{}, errors.New("foreign/repeated/partial native original relation")
		}
		seen[origin.Incarnation] = true
		if origin.Box == box {
			result.nativeOrigins = append(result.nativeOrigins, origin)
		}
	}
	payloadSeen := map[string]bool{}
	for _, inc := range sample.native.PayloadIncarnations {
		if !seen[inc] || payloadSeen[inc] {
			return missingCIDMetadataResult{}, errors.New("orphan/repeated native payload summary")
		}
		payloadSeen[inc] = true
	}
	cidSeen := map[string]bool{}
	for _, cid := range result.runtimeCIDs {
		if !missingCIDHex(cid, 64) || cidSeen[cid] {
			return missingCIDMetadataResult{}, errors.New("partial/repeated runtime candidate")
		}
		cidSeen[cid] = true
	}
	for _, identity := range sample.registrations.Identities {
		if err := identity.Validate(); err != nil || identity.Box != box {
			return missingCIDMetadataResult{}, errors.Join(err, errors.New("foreign/unknown selected registration identity"))
		}
		switch identity.Mode {
		case operator.RegistrationOpenShellNative:
			result.nativeRegistrations = append(result.nativeRegistrations, identity)
		case operator.RegistrationLegacy:
			result.legacyRegistrations = append(result.legacyRegistrations, identity)
		default:
			return missingCIDMetadataResult{}, errors.New("unknown registration mode")
		}
	}
	for _, claim := range sample.claims {
		if err := claim.Endpoint.Validate(); err != nil || claim.Endpoint.Box != box {
			return missingCIDMetadataResult{}, errors.Join(err, errors.New("foreign/partial selected parent claim"))
		}
		if claim.Native {
			result.nativeClaims = append(result.nativeClaims, claim.Endpoint)
		} else {
			result.legacyClaims = append(result.legacyClaims, claim.Endpoint)
		}
	}
	result.ociMounts = cloneMissingCIDMounts(sample.mounts)
	selectedProjection := missingCIDSelectedProjection(box, result.legacyRegistrations, result.nativeRegistrations, sample.claims, sample.mounts)
	switch {
	case len(result.nativeOrigins)+len(result.nativeRegistrations)+len(result.nativeClaims) > 0:
		result.kind = missingCIDNativeEvidence
	case len(result.runtimeCIDs) > 0:
		result.kind = missingCIDCurrentRuntimeEvidence
	case selectedProjection:
		result.kind = missingCIDSelectedProjectionEvidence
	case len(result.legacyRegistrations)+len(result.legacyClaims) > 0:
		result.kind = missingCIDLegacyMetadataCandidate
	default:
		result.kind = missingCIDAbsentMetadataCandidate
	}
	return result, nil
}

// This REAL primary caller consumes actual retained native/registration/claim
// reader objects and actual successful engine APIs, not callback-empty lists.
// All owners are BORROWED: ultimate original caller MUST retain them through
// its last external callback/finalization. No effect dispatch is installed.
func sampleMissingCIDMetadata(ctx context.Context, box string, client *container.Client, native *openshell.MissingCIDNativeRead, registrations *operator.RegistrationScope, claims *controller.MissingCIDClaimRead, priorEffectErr error) (missingCIDMetadataResult, error) {
	if priorEffectErr != nil {
		return missingCIDMetadataResult{}, errors.Join(priorEffectErr, errors.New("missing-CID observations cannot settle failed/uncertain prior Stop or remove"))
	}
	if ctx == nil || client == nil || native == nil || registrations == nil || claims == nil {
		return missingCIDMetadataResult{}, errors.New("required actual classifier primary source unavailable")
	}
	before, err := client.RuntimeIdentityInventory(ctx)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	rows, err := before.Identities()
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	candidates, err := before.BoxCandidates(box)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	mounts, err := client.NativeMountInventory(ctx)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	after, err := client.RuntimeIdentityInventory(ctx)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	repeated, err := after.Identities()
	if err != nil || !slices.Equal(rows, repeated) {
		return missingCIDMetadataResult{}, errors.Join(err, errors.New("complete runtime ID/name scope moved across OCI observation"))
	}
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ContainerID] = true
	}
	if len(mounts) != len(ids) {
		return missingCIDMetadataResult{}, errors.New("runtime/OCI configured scope differs")
	}
	for _, entry := range mounts {
		if !ids[entry.ContainerID] {
			return missingCIDMetadataResult{}, errors.New("foreign/repeated OCI inventory member")
		}
		delete(ids, entry.ContainerID)
	}
	if len(ids) != 0 {
		return missingCIDMetadataResult{}, errors.New("partial OCI inventory")
	}
	// Keep private source/mount strings only in owned stack observations. Actual
	// protected source identity/alias join is STILL REQUIRED, not inferred by text.
	registrationView, err := registrations.Observation(ctx, box)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	claimView, err := claims.Observation(ctx, box)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	nativeView, err := native.Observation(ctx)
	if err != nil {
		return missingCIDMetadataResult{}, err
	}
	if client.Env() != nativeView.Space {
		return missingCIDMetadataResult{}, errors.New("runtime client differs from original native selected space")
	}
	if err := registrations.CheckOriginal(ctx); err != nil {
		return missingCIDMetadataResult{}, err
	}
	repeatedClaims, err := claims.Observation(ctx, box)
	if err != nil || !slices.Equal(claimView, repeatedClaims) {
		return missingCIDMetadataResult{}, errors.Join(err, errors.New("original parent claims moved during primary sample"))
	}
	return classifyMissingCIDMetadata(box, missingCIDMetadataSample{nativeView, registrationView, claimView, candidates, mounts})
}

func missingCIDHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func cloneMissingCIDMounts(rows []container.NativeMountEntry) []container.NativeMountEntry {
	out := make([]container.NativeMountEntry, len(rows))
	for i, row := range rows {
		out[i] = row
		out[i].Mounts = make([]container.NativeMount, len(row.Mounts))
		for j, mount := range row.Mounts {
			out[i].Mounts[j] = mount
			out[i].Mounts[j].Options = slices.Clone(mount.Options)
		}
	}
	return out
}

// Positive configured selected-path evidence only. No text match can prove
// absence of aliases/live mounts; the opaque source-identity join is pending.
func missingCIDSelectedProjection(box string, legacy, native []operator.RegistrationIdentity, claims []controller.MissingCIDClaimObservation, mounts []container.NativeMountEntry) bool {
	roots := []string{sandbox.AgentboxDirHost(box), sandbox.SocketDirHost(box), sandbox.AgentboxPolicyHostDir(box), session.StateDirFor(box), session.SocketPath(box)}
	for _, identity := range append(slices.Clone(legacy), native...) {
		roots = append(roots, identity.Directory())
	}
	for _, claim := range claims {
		roots = append(roots, claim.Endpoint.Directory())
	}
	selected := func(value string) bool {
		if !filepath.IsAbs(value) {
			return false
		}
		value = filepath.Clean(value)
		for _, root := range roots {
			if value == root || strings.HasPrefix(value, root+"/") {
				return true
			}
		}
		return false
	}
	for _, entry := range mounts {
		if selected(entry.RootPath) {
			return true
		}
		for _, mount := range entry.Mounts {
			if selected(mount.Source) {
				return true
			}
		}
	}
	return false
}
