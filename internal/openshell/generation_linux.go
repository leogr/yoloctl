package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// GenerationTarget is a current engine observation from the admitted caller's
// fixed-CID verifier. It is not proof of any earlier stop/start result. Unknown
// outcomes must remain fenced by the enclosing controller, never cleared here.
type GenerationTarget struct {
	ContainerID string
	Status      string
	Running     bool
	PID         int
	// EmptyTaskStateObserved carries the actual adapter's explicit no-process
	// shape. It is neither proof of past execution nor a Stop acknowledgement.
	// Missing/unknown state must not be normalized to an inactive status.
	EmptyTaskStateObserved bool
}

// NativeGeneration preserves the selected container's private create-time
// environment and rollback policy across fresh generation preparation. It is
// read before destructive session/runtime cleanup, under existing admission.
// No account lookup, original activation replay, launch or cleanup authority
// is supplied by this checkpoint or its metadata. Close deletes no disk state.
type NativeGeneration struct {
	root        *os.File
	cid         string
	release     QualifiedRelease
	current     RuntimeRecord
	environment map[string]string
	policy      []byte
	rego        []byte
	owners      runtimeOwners
	poisoned    bool
	uncertainty error
}

func (NativeGeneration) String() string     { return "OpenShell generation checkpoint [redacted]" }
func (g NativeGeneration) GoString() string { return g.String() }
func (g *NativeGeneration) Close() error {
	if g == nil {
		return nil
	}
	clear(g.environment)
	g.environment = nil
	clear(g.rego)
	g.rego = nil
	clear(g.policy)
	g.policy = nil
	if g.root == nil {
		return nil
	}
	err := g.root.Close()
	g.root = nil
	return err
}
func (g *NativeGeneration) OriginalPolicy() []byte {
	if g == nil {
		return nil
	}
	return bytes.Clone(g.policy)
}

// SnapshotNativeGeneration checks current native identity/source qualification
// and the exact selected root/private files. The caller independently verifies
// storage, actual CID/launch/binds and admission before and after this read.
func SnapshotNativeGeneration(ctx context.Context, selected RuntimeRecord, release QualifiedRelease, containerID string) (*NativeGeneration, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("generation checkpoint requires admitted native VM-root infrastructure")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := release.checkExistingBoxHelper(selected.Launch.Binding.Box); err != nil {
		return nil, err
	}
	if err := nativeGenerationIdentity(selected, release); err != nil {
		return nil, err
	}
	control, err := layout.OpenShellControlDir()
	if err != nil {
		return nil, err
	}
	root, err := openNativeGenerationDirectory(filepath.Join(control, "containers", selected.Launch.Binding.Incarnation))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	owners := runtimeOwners{0, 0, int(selected.Launch.UID), int(selected.Launch.GID), int(SupervisorUID), int(SupervisorGID)}
	return snapshotGenerationAt(root, selected, release, containerID, owners)
}
func nativeGenerationIdentity(record RuntimeRecord, release QualifiedRelease) error {
	current := spaceenv.Current()
	if current == nil || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("generation mutation requires current native root identity")
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := record.Validate(release, helper.BinarySHA256); err != nil {
		return err
	}
	binding := record.Launch.Binding
	if binding.Space != current.Space || binding.DiskID != current.DiskID || binding.DiskUUID != current.DiskUUID || record.Launch.UID != uint32(current.UID) || record.Launch.GID != uint32(current.GID) {
		return errors.New("generation belongs to another native disk/space/identity")
	}
	// An old controller generation is valid observation of a retained container.
	// PrepareNext supplies the CURRENT controller generation for fresh authority.
	return nil
}
func openNativeGenerationDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("invalid native generation directory")
	}
	anchor, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(anchor) }()
	fd, err := unix.Openat2(anchor, strings.TrimPrefix(path, "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func cloneRuntimeRecord(record RuntimeRecord) RuntimeRecord {
	record.Files = maps.Clone(record.Files)
	return record
}
func generationOrigin(record RuntimeRecord) containerOrigin {
	b := record.Launch.Binding
	return containerOrigin{b.Space, b.DiskID, b.DiskUUID, b.Box, b.Incarnation, b.Qualification, record.Launch.HelperSHA256}
}
func verifyGenerationSelection(root *os.File, selected RuntimeRecord, owners runtimeOwners) error {
	if err := runtimeDirectoryOwner(root, owners.rootUID, owners.rootGID); err != nil {
		return err
	}
	origin, err := runtimeReadAt(root, "origin.json", owners.rootUID, owners.rootGID, 4096)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(generationOrigin(selected))
	if !bytes.Equal(origin, expected) {
		return errors.New("native generation origin differs from its selected container")
	}
	raw, err := runtimeReadAt(root, "current.json", owners.rootUID, owners.rootGID, 1<<20)
	if err != nil {
		return err
	}
	expected, _ = json.Marshal(selected)
	if !bytes.Equal(raw, expected) {
		return errors.New("native generation selection changed")
	}
	return nil
}

// Owner parameters model the three protected roles in ordinary project tests.
// Production always supplies root, the actual workload and reserved supervisor.
func snapshotGenerationAt(root *os.File, selected RuntimeRecord, release QualifiedRelease, cid string, owners runtimeOwners) (*NativeGeneration, error) {
	if !lowerHex(cid, 32) {
		return nil, errors.New("generation checkpoint requires the full inspected container ID")
	}
	if err := verifyGenerationSelection(root, selected, owners); err != nil {
		return nil, err
	}
	material, err := readRuntimeMaterialAt(root, release, selected.Launch.HelperLaunch(), owners)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, body := range material.files {
			clear(body)
		}
	}()
	if recordIdentity(material.record) != recordIdentity(selected) {
		return nil, errors.New("private generation differs from selected runtime")
	}

	environment, err := generationEnvironment(selected, material.files["boundary/bootstrap.json"])
	if err != nil {
		return nil, err
	}

	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &NativeGeneration{root: os.NewFile(uintptr(fd), root.Name()), cid: cid, release: release, current: cloneRuntimeRecord(selected), environment: environment, policy: bytes.Clone(material.files["supervisor/policy.yaml"]), rego: bytes.Clone(material.files["supervisor/sandbox-policy.rego"]), owners: owners}, nil
}
func generationEnvironment(selected RuntimeRecord, raw []byte) (map[string]string, error) {

	if err := checkJSONKeys(raw); err != nil {
		return nil, err
	}
	var boundary boundaryConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&boundary); err != nil {
		return nil, errors.New("selected private bootstrap has an invalid schema")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("selected private bootstrap has trailing data")
	}
	digest, err := selected.Launch.Binding.digest()
	if err != nil {
		return nil, err
	}
	identity := boundary.WorkloadIdentity
	if boundary.BoundaryID != selected.Launch.Binding.Box || boundary.Generation != selected.Generation || boundary.SessionID != selected.SessionID || boundary.SessionRotation != 1 || boundary.AuthEpoch != 1 || boundary.GatewayID != issuerID || identity.UID != selected.Launch.UID || identity.GID != selected.Launch.GID || len(identity.SupplementaryGIDs) != 0 || identity.Source != "yoloctl" || identity.ResourceDigest != "sha256:"+digest || boundary.ProvisioningEvidence.Generation != selected.Generation || boundary.ProvisioningEvidence.Digest != digest {
		return nil, errors.New("selected bootstrap belongs to another runtime generation/identity")
	}
	input := BootstrapInput{Binding: selected.Launch.Binding, UID: selected.Launch.UID, GID: selected.Launch.GID, Workdir: selected.Launch.Workdir, ReadOnlyWorkdir: selected.Launch.WorkspaceReadOnly, Environment: boundary.ChildEnv}
	if err := input.validate(); err != nil {
		return nil, err
	}
	for key := range boundary.ChildEnv {
		if strings.HasPrefix(key, "OPENSHELL_") {
			return nil, errors.New("selected child environment contains a reserved bootstrap control")
		}
	}
	if _, ok := boundary.ChildEnv["HOME"]; !ok {
		return nil, errors.New("selected bootstrap lacks its explicit workload home")
	}
	if _, ok := boundary.ChildEnv["USER"]; !ok {
		return nil, errors.New("selected bootstrap lacks its explicit workload user")
	}
	return maps.Clone(boundary.ChildEnv), nil
}

func (g *NativeGeneration) verifyRoot() error {
	if err := g.usable(); err != nil {
		return err
	}
	selected, err := openNativeGenerationDirectory(g.root.Name())
	if err != nil {
		return err
	}
	defer func() { _ = selected.Close() }()
	oldInfo, oldErr := g.root.Stat()
	newInfo, newErr := selected.Stat()
	if oldErr != nil || newErr != nil || !os.SameFile(oldInfo, newInfo) {
		return errors.New("native checkpoint directory was replaced")
	}
	return runtimeDirectoryOwner(g.root, g.owners.rootUID, g.owners.rootGID)
}

func (g *NativeGeneration) usable() error {
	if g == nil {
		return errors.New("generation checkpoint is missing")
	}
	if g.poisoned {
		return &NativeEffectsUncertain{Cause: g.uncertainty}
	}
	if g.root == nil {
		return errors.New("generation checkpoint is closed")
	}
	return nil
}

func (g *NativeGeneration) nextInput(controller string, policy []byte) (BootstrapInput, error) {
	if err := g.usable(); err != nil {
		return BootstrapInput{}, err
	}

	launch := g.current.Launch
	binding := launch.Binding
	binding.ControllerGeneration = controller
	binding.PolicySHA256 = sha256Hex(policy)
	input := BootstrapInput{Binding: binding, UID: launch.UID, GID: launch.GID, Workdir: launch.Workdir, ReadOnlyWorkdir: launch.WorkspaceReadOnly, Environment: maps.Clone(g.environment)}
	if err := input.validate(); err != nil {
		return BootstrapInput{}, err
	}
	next, err := NewLaunchConfig(input, g.release, launch.HelperSHA256, launch.UnconfinedNetwork)
	if err != nil {
		return BootstrapInput{}, err
	}
	if err := next.ValidateWorkspacePolicy(policy); err != nil {
		return BootstrapInput{}, err
	}
	return input, nil
}

// ValidatePolicy is a read-only prerequisite for the enclosing lifecycle's
// preflight, before it ends a holder/session or stops an existing container.
func (g *NativeGeneration) ValidatePolicy(policy []byte) error {
	if err := g.usable(); err != nil {
		return err
	}
	if err := g.verifyRoot(); err != nil {
		return err
	}
	if err := verifyGenerationSelection(g.root, g.current, g.owners); err != nil {
		return err
	}
	_, err := g.nextInput(g.current.Launch.Binding.ControllerGeneration, policy)
	return err
}

// PrepareNext reuses only the original private environment/identity, minting
// entirely new per-start credentials via PrepareNative. The admitted verifier
// must bind the actual same CID, launch, live mounts and current native storage
// and prove it is inactive. It cannot reinterpret any prior unknown outcome.
// Successful preparation updates this same-call checkpoint for an authorized
// later rollback using OriginalPolicy; no old activation credentials are used.
func (g *NativeGeneration) PrepareNext(ctx context.Context, policy []byte, verifyInactive func(context.Context) (GenerationTarget, error)) (PreparedRuntime, error) {
	if err := g.usable(); err != nil {
		return PreparedRuntime{}, err
	}
	if verifyInactive == nil {
		return PreparedRuntime{}, errors.New("generation checkpoint lacks its inactive verifier")
	}
	if err := ctx.Err(); err != nil {
		return PreparedRuntime{}, err
	}
	if err := nativeGenerationIdentity(g.current, g.release); err != nil {
		return PreparedRuntime{}, err
	}
	if err := g.release.checkExistingBoxHelper(g.current.Launch.Binding.Box); err != nil {
		return PreparedRuntime{}, err
	}
	return g.prepareNext(ctx, spaceenv.Current().Generation, policy, verifyInactive, func(prepare context.Context, input BootstrapInput, rego []byte) (PreparedRuntime, error) {
		// The existing box keeps its installed, hash-pinned helper.
		return prepareNative(prepare, input, g.release, g.current.Launch.HelperSHA256, g.current.Launch.UnconfinedNetwork, policy, rego, false)
	})
}

// The same bounded/file-verified pipeline runs in ordinary current-owner
// project fixtures with a terminal preparation callback. Only PrepareNext
// supplies native identity/source checks and the real root preparation effect.
func (g *NativeGeneration) prepareNext(ctx context.Context, controller string, policy []byte, verifyInactive func(context.Context) (GenerationTarget, error), prepare func(context.Context, BootstrapInput, []byte) (PreparedRuntime, error)) (PreparedRuntime, error) {
	if err := g.usable(); err != nil {
		return PreparedRuntime{}, err
	}
	if verifyInactive == nil || prepare == nil {
		return PreparedRuntime{}, errors.New("generation preparation requires complete wiring")
	}
	if err := ctx.Err(); err != nil {
		return PreparedRuntime{}, err
	}
	verify := func() error {
		target, err := verifyInactive(ctx)
		if err != nil {
			return err
		}
		if target.ContainerID != g.cid || target.Running || target.PID != 0 || (target.Status != "created" && target.Status != "exited" && target.Status != "stopped" && (target.Status != "" || !target.EmptyTaskStateObserved)) {
			return errors.New("fresh generation requires the same verified inactive container")
		}
		return nil
	}
	if err := verify(); err != nil {
		return PreparedRuntime{}, err
	}

	if err := g.verifyRoot(); err != nil {
		return PreparedRuntime{}, err
	}

	if err := verifyGenerationSelection(g.root, g.current, g.owners); err != nil {
		return PreparedRuntime{}, err
	}
	input, err := g.nextInput(controller, policy)
	if err != nil {
		return PreparedRuntime{}, err
	}
	// No account resolution or image ENV reread: those creation-time values
	// change only on explicit container recreation, not policy reload/start.
	if sha256Hex(g.rego) != g.release.document.Pins.RegoSHA256 {
		return PreparedRuntime{}, errors.New("generation Rego differs from its qualified pair")
	}
	if err := verify(); err != nil {
		return PreparedRuntime{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreparedRuntime{}, err
	}
	prepared, err := prepare(ctx, input, bytes.Clone(g.rego))

	if err != nil {
		g.poisoned = true
		g.uncertainty = fmt.Errorf("native generation preparation may have selected private state; do not retry automatically: %w", err)
		return PreparedRuntime{}, &NativeEffectsUncertain{Cause: g.uncertainty}
	}
	expected, validationErr := NewLaunchConfig(input, g.release, g.current.Launch.HelperSHA256, g.current.Launch.UnconfinedNetwork)
	if validationErr == nil {
		validationErr = prepared.Record.Validate(g.release, g.current.Launch.HelperSHA256)
	}
	if validationErr == nil && (prepared.ContainerRoot != g.root.Name() || prepared.GenerationRoot != filepath.Join(g.root.Name(), "generations", prepared.Record.Generation) || prepared.Record.Launch != expected || prepared.Record.Generation == g.current.Generation || prepared.Record.SessionID == g.current.SessionID || prepared.Record.SSHHostPublicKey == g.current.SSHHostPublicKey || prepared.Record.Files["status/activation.key"] == g.current.Files["status/activation.key"]) {
		validationErr = errors.New("generation preparation did not acknowledge fresh bound runtime material")
	}
	if validationErr == nil {
		validationErr = verifyGenerationSelection(g.root, prepared.Record, g.owners)
	}
	if validationErr == nil {
		validationErr = g.verifyRoot()
	}
	if validationErr == nil {
		material, readErr := readRuntimeMaterialAt(g.root, g.release, prepared.Record.Launch.HelperLaunch(), g.owners)
		validationErr = readErr
		if validationErr == nil {
			environment, envErr := generationEnvironment(material.record, material.files["boundary/bootstrap.json"])
			validationErr = envErr
			if validationErr == nil && (recordIdentity(material.record) != recordIdentity(prepared.Record) || !maps.Equal(environment, g.environment)) {
				validationErr = errors.New("prepared generation changed the original private environment")
			}
		}
		for _, body := range material.files {
			clear(body)
		}
	}

	if validationErr != nil {
		g.poisoned = true
		g.uncertainty = validationErr
		return PreparedRuntime{}, &NativeEffectsUncertain{Cause: g.uncertainty}
	}

	g.current = cloneRuntimeRecord(prepared.Record)
	return prepared, nil
}
