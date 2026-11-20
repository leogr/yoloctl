//go:build !linux

package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
)

// OpenShellHelperNeedsRecreate is a Linux VM controller read.
func OpenShellHelperNeedsRecreate(context.Context, *container.Client, *container.Details) (bool, error) {
	return false, errors.New("the OpenShell helper check requires the Linux VM controller")
}

func readNativeRuntimePolicy(context.Context, *container.Client, string, *container.Details) ([]byte, error) {
	return nil, errors.New("native policy reads require the Linux VM controller")
}
