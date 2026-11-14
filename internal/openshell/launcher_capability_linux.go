package openshell

import (
	"errors"
	"os"

	"github.com/leogr/yoloctl/internal/safepath"
)

// LauncherCapabilityName is the record, beside origin.json in a container's
// control root, that the build which created the box made the fixed launcher
// its supervisor's main process (stop-box route M-b, provisional name).
const LauncherCapabilityName = "launcher"

// ContainerLauncherCapable reads that record for the container whose control
// root is controlRoot: true when present (root-owned, exactly as written),
// false when definitely absent (a box created before the launcher); any other
// failure is an error, never "absent".
func ContainerLauncherCapable(controlRoot string) (bool, error) {
	raw, info, err := safepath.ReadFileInfo(controlRoot, LauncherCapabilityName, 16)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := privateFileOwner(info, 0, 0); err != nil {
		return false, err
	}
	if string(raw) != "1\n" {
		return false, errors.New("launcher capability record is malformed")
	}
	return true, nil
}
