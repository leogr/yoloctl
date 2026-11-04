package layout

import (
	"fmt"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// OpenShellControlDir is VM-root-only state on the verified native data disk.
// Its issuer subtree must never be bound into any container. Only individual
// per-container runtime subtrees may be projected by the native provisioner.
func OpenShellControlDir() (string, error) {
	c := spaceenv.Current()
	if c == nil {
		return "", fmt.Errorf("OpenShell control state requires the native controller")
	}
	return filepath.Join(c.DiskMount, "runtime", "openshell"), nil
}
