package layout

import (
	"path/filepath"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// AgentObserveParent holds one capped tmpfs mount point per box. It lives in
// the reserved controller namespace, outside the sandbox socket directory
// that is already bound into the box and that box teardown removes.
func AgentObserveParent() string {
	return filepath.Join(spaceenv.RunRoot, "agent-observe")
}

// AgentObserveDir is the VM mount point of a box's agent observation tmpfs.
// It is keyed by the validated box name: declaration IDs are minted only for
// chat enrollment, and destroy removes the directory before a name is reused.
func AgentObserveDir(box string) (string, error) {
	if err := ValidateName("agentbox", box); err != nil {
		return "", err
	}
	return filepath.Join(AgentObserveParent(), box), nil
}
