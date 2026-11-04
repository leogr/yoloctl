package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"
)

const namespacePinRecordSchema = 1

type namespacePinPhase string

const (
	pinPlanned                 namespacePinPhase = "pin-planned"
	pinDirectoryBindAttempt    namespacePinPhase = "directory-bind-attempt"
	pinDirectoryBound          namespacePinPhase = "directory-bound"
	pinDirectoryPrivateAttempt namespacePinPhase = "directory-private-attempt"
	pinDirectoryPrivate        namespacePinPhase = "directory-private"
	pinNamespaceBindAttempt    namespacePinPhase = "namespace-bind-attempt"
	pinPresent                 namespacePinPhase = "pin-present"
	pinPolicyApplyAttempt      namespacePinPhase = "policy-apply-attempt"
	pinPolicyReadback          namespacePinPhase = "policy-readback"
	pinRetiring                namespacePinPhase = "retiring"
	pinTableAbsent             namespacePinPhase = "table-absent"
	pinUnmountAttempt          namespacePinPhase = "pin-unmount-attempt"
	pinUnmounted               namespacePinPhase = "unmounted"
	pinDirectoryUnmountAttempt namespacePinPhase = "directory-unmount-attempt"
	pinDirectoryUnmounted      namespacePinPhase = "directory-unmounted"
	pinPartialPrivateAttempt   namespacePinPhase = "partial-private-attempt"
	pinPartialPrivate          namespacePinPhase = "partial-private"
	pinPartialUnmountAttempt   namespacePinPhase = "partial-unmount-attempt"
	pinPartialUnmounted        namespacePinPhase = "partial-unmounted"
)

// namespacePinScope records observations selected by the admitted creator.
// Its PID/start are historical binding, not a demand that the process remain
// alive during cold cleanup. The real pin creator must independently verify
// peer/process/record/namespace and the persistent VM mount namespace first.
type namespacePinScope struct {
	Binding             Binding           `json:"binding"`
	RecordSHA256        string            `json:"record_sha256"`
	Generation          string            `json:"generation"`
	ContainerID         string            `json:"container_id"`
	KernelBootID        string            `json:"kernel_boot_id"`
	PeerPID             int               `json:"peer_pid"`
	PeerStartTicks      string            `json:"peer_start_ticks"`
	EngineStartedAt     time.Time         `json:"engine_started_at"`
	Namespace           namespaceIdentity `json:"namespace"`
	InfrastructureMount namespaceIdentity `json:"infrastructure_mount_namespace"`
}

type namespacePinPlaceholder struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// namespacePinRecord is durable bookkeeping, never an in-call mount/stop/
// delete/unmount ACK or authority to activate. These private data helpers do
// no I/O, no discovery and no effects. Filesystem ownership, admission, exact
// selected release, live creation checks and actual mount identity remain the
// effectful caller's responsibility; decoding an existing record grants none.
type namespacePinRecord struct {
	Schema           int                     `json:"schema"`
	OriginSHA256     string                  `json:"origin_sha256"`
	Scope            namespacePinScope       `json:"scope"`
	Placeholder      namespacePinPlaceholder `json:"placeholder"`
	Plan             nativeEgressPlan        `json:"plan"`
	Phase            namespacePinPhase       `json:"phase"`
	DirectoryMountID uint64                  `json:"directory_mount_id"`
	PinMountID       uint64                  `json:"pin_mount_id"`
	ReadbackSHA256   string                  `json:"readback_sha256"`
}

func newNamespacePinScope(record RuntimeRecord, target ActivationTarget, start string, network, infrastructureMount namespaceIdentity, bootID string) (namespacePinScope, error) {
	scope := namespacePinScope{
		Binding: record.Launch.Binding, RecordSHA256: recordIdentity(record), Generation: record.Generation,
		ContainerID: target.ContainerID, KernelBootID: bootID, PeerPID: target.PID,
		PeerStartTicks: start, EngineStartedAt: target.StartedAt.Round(0).UTC(),
		Namespace: network, InfrastructureMount: infrastructureMount,
	}
	if err := scope.validate(record, target.ContainerID); err != nil {
		return namespacePinScope{}, err
	}
	return scope, nil
}

func (s namespacePinScope) validate(record RuntimeRecord, cid string) error {
	if err := record.Launch.Validate(); err != nil {
		return err
	}
	if !record.Launch.UnconfinedNetwork || s.Binding != record.Launch.Binding || s.RecordSHA256 != recordIdentity(record) || s.Generation != record.Generation || s.ContainerID != cid || !lowerHex(cid, 32) {
		return errors.New("namespace pin scope differs from the selected unconfined container/record")
	}
	digest, err := s.Binding.digest()
	if err != nil {
		return err
	}
	generation, err := generationForSession(digest, record.SessionID)
	if err != nil || generation != s.Generation {
		return errors.New("namespace pin scope has an invalid generation/session binding")
	}
	start, err := strconv.ParseUint(s.PeerStartTicks, 10, 64)
	if err != nil || start == 0 || strconv.FormatUint(start, 10) != s.PeerStartTicks || s.PeerPID <= 1 || s.EngineStartedAt.IsZero() || s.EngineStartedAt.Year() < 1 || s.EngineStartedAt.Year() > 9999 || !canonicalUUID(s.KernelBootID) {
		return errors.New("namespace pin scope lacks its original process/start/kernel-boot binding")
	}
	if s.Namespace.Device == 0 || s.Namespace.Inode == 0 || s.InfrastructureMount.Device == 0 || s.InfrastructureMount.Inode == 0 || s.Namespace == s.InfrastructureMount {
		return errors.New("namespace pin scope lacks distinct network and VM mount namespace identities")
	}
	return nil
}

func newNamespacePinRecord(scope namespacePinScope, record RuntimeRecord, cid string, placeholder namespacePinPlaceholder, plan nativeEgressPlan) (namespacePinRecord, error) {
	if err := scope.validate(record, cid); err != nil {
		return namespacePinRecord{}, err
	}
	if err := plan.validate(scope.Namespace, plan.Seed); err != nil {
		return namespacePinRecord{}, err
	}
	plan.Seed = copyNativeEgressInputs(plan.Seed)
	plan.Inputs = copyNativeEgressInputs(plan.Inputs)
	plan.Observed = slices.Clone(plan.Observed)
	result := namespacePinRecord{Schema: namespacePinRecordSchema, Scope: scope, Placeholder: placeholder, Plan: plan, Phase: pinPlanned}
	origin, err := result.originIdentity()
	if err != nil {
		return namespacePinRecord{}, err
	}
	result.OriginSHA256 = origin
	if err := result.validate(record, cid, origin); err != nil {
		return namespacePinRecord{}, err
	}
	return result, nil
}

// originIdentity covers the future immutable origin file, separately from
// mutable phase observations. A cold reader must obtain its expected hash from
// that independently protected file, not from the mutable record itself.
func (p namespacePinRecord) originIdentity() (string, error) {
	raw, err := json.Marshal(struct {
		Scope       namespacePinScope       `json:"scope"`
		Placeholder namespacePinPlaceholder `json:"placeholder"`
		Plan        nativeEgressPlan        `json:"plan"`
	}{p.Scope, p.Placeholder, p.Plan})
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

func (p namespacePinRecord) validate(record RuntimeRecord, cid, expectedOrigin string) error {
	if p.Schema != namespacePinRecordSchema || p.Placeholder.Device == 0 || p.Placeholder.Inode == 0 {
		return errors.New("namespace pin record has an invalid schema or placeholder identity")
	}
	if err := p.Scope.validate(record, cid); err != nil {
		return err
	}
	if err := p.Plan.validate(p.Scope.Namespace, p.Plan.Seed); err != nil {
		return err
	}
	if p.Plan.Inputs.OwnerSHA256 != egressOwner(record) {
		return errors.New("namespace pin policy owner differs from the selected runtime record")
	}
	origin, err := p.originIdentity()
	if err != nil || !lowerHex(expectedOrigin, 32) || p.OriginSHA256 != expectedOrigin || origin != expectedOrigin {
		return errors.New("namespace pin record differs from its immutable origin")
	}
	if p.ReadbackSHA256 != "" && p.ReadbackSHA256 != p.Plan.RulesSHA256 {
		return errors.New("namespace pin readback differs from its complete planned rules")
	}
	switch p.Phase {
	case pinPlanned, pinDirectoryBindAttempt:
		if p.DirectoryMountID != 0 || p.PinMountID != 0 || p.ReadbackSHA256 != "" {
			return errors.New("planned namespace pin claims a later mount/readback phase")
		}
	case pinDirectoryBound, pinDirectoryPrivateAttempt, pinDirectoryPrivate, pinNamespaceBindAttempt, pinPartialPrivateAttempt, pinPartialPrivate, pinPartialUnmountAttempt, pinPartialUnmounted:
		if p.DirectoryMountID == 0 || p.PinMountID != 0 || p.ReadbackSHA256 != "" {
			return errors.New("namespace directory phase lacks its own mount or claims later pin/readback")
		}
	case pinPresent, pinPolicyApplyAttempt:
		if p.DirectoryMountID == 0 || p.PinMountID == 0 || p.DirectoryMountID == p.PinMountID || p.ReadbackSHA256 != "" {
			return errors.New("present namespace pin has an invalid mount/readback phase")
		}
	case pinPolicyReadback:
		if p.DirectoryMountID == 0 || p.PinMountID == 0 || p.DirectoryMountID == p.PinMountID || p.ReadbackSHA256 != p.Plan.RulesSHA256 {
			return errors.New("namespace pin lacks its recorded exact policy readback")
		}
	case pinRetiring, pinTableAbsent, pinUnmountAttempt, pinUnmounted, pinDirectoryUnmountAttempt, pinDirectoryUnmounted:
		if p.DirectoryMountID == 0 || p.PinMountID == 0 || p.DirectoryMountID == p.PinMountID {
			return errors.New("namespace pin retirement lacks its historical mount identity")
		}
	default:
		return errors.New("unknown namespace pin phase")
	}
	return nil
}

func decodeNamespacePinRecord(raw []byte, record RuntimeRecord, cid, expectedOrigin string) (namespacePinRecord, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return namespacePinRecord{}, errors.New("unbounded namespace pin record")
	}
	if err := checkJSONKeys(raw); err != nil {
		return namespacePinRecord{}, err
	}
	var pin namespacePinRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pin); err != nil {
		return namespacePinRecord{}, err
	}
	if err := pin.validate(record, cid, expectedOrigin); err != nil {
		return namespacePinRecord{}, err
	}
	return pin, nil
}

type namespacePinBootRelation int

const (
	pinBootUnknown namespacePinBootRelation = iota
	pinSameBoot
	pinDifferentBoot
)

// classifyBoot only compares canonical metadata with a caller-supplied boot
// identity. The caller must independently read/verify the actual VM kernel.
// Neither result authorizes unmount/deletion, proves absence or settles a
// receipt. Different-boot handling must not use old inodes to select a live NS.
func (s namespacePinScope) classifyBoot(currentKernelBootID string) (namespacePinBootRelation, error) {
	if !canonicalUUID(s.KernelBootID) || !canonicalUUID(currentKernelBootID) {
		return pinBootUnknown, errors.New("namespace pin boot comparison requires canonical kernel identities")
	}
	if s.KernelBootID == currentKernelBootID {
		return pinSameBoot, nil
	}
	return pinDifferentBoot, nil
}
