package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
)

// ReadNativePolicy observes one selected, versioned policy. The admitted root
// caller independently verifies current storage and actual container/binds.
// Old controller/helper source qualification is valid observation; this never
// installs artifacts, reads bootstrap credentials or grants launch/receipt
// authority. Mutation qualification remains the separate SDK entry points.
func ReadNativePolicy(ctx context.Context, selected RuntimeRecord, release QualifiedRelease) ([]byte, error) {
	if err := ctx.Err(); err != nil {
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
	return readNativePolicyAt(ctx, root, selected, release, owners)
}

// Ordinary owned-file fixture seam; production supplies the actual admitted
// native root and fixed reserved role owners, never a caller-selected path.
func readNativePolicyAt(ctx context.Context, root *os.File, selected RuntimeRecord, release QualifiedRelease, owners runtimeOwners) ([]byte, error) {
	if root == nil {
		return nil, errors.New("policy read lacks its protected native root")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Production already bound this helper hash to the recorded release in
	// nativeGenerationIdentity. Match the selected launch here, as the
	// existing private readRuntimeMaterialAt fixture seam does.
	if err := selected.Validate(release, selected.Launch.HelperSHA256); err != nil {
		return nil, err
	}
	if err := verifyGenerationSelection(root, selected, owners); err != nil {
		return nil, err
	}
	generations, err := runtimeDirectoryAt(root, "generations", owners.rootUID, owners.rootGID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = generations.Close() }()
	generation, err := runtimeDirectoryAt(generations, selected.Generation, owners.rootUID, owners.rootGID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = generation.Close() }()
	immutable, err := runtimeReadAt(generation, "record.json", owners.rootUID, owners.rootGID, 1<<20)
	if err != nil {
		return nil, err
	}
	expected, err := json.Marshal(selected)
	if err != nil || !bytes.Equal(immutable, expected) {
		return nil, errors.Join(err, errors.New("policy immutable generation differs from selected runtime"))
	}
	supervisor, err := runtimeDirectoryAt(generation, "supervisor", owners.supervisorUID, owners.supervisorGID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = supervisor.Close() }()
	policy, err := runtimeReadAt(supervisor, "policy.yaml", owners.supervisorUID, owners.supervisorGID, 1<<20)
	if err != nil {
		return nil, err
	}
	hash := sha256Hex(policy)
	if hash != selected.Files["supervisor/policy.yaml"] || hash != selected.Launch.Binding.PolicySHA256 {
		return nil, errors.New("policy bytes differ from selected record/launch hash")
	}
	if err := selected.Launch.ValidateWorkspacePolicy(policy); err != nil {
		return nil, err
	}
	if err := verifyGenerationSelection(root, selected, owners); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return policy, nil
}
