//go:build !linux

package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) prepareNativeContainerRemoval(context.Context, string, string, bool) (*NativeContainerRemoval, error) {
	return nil, errors.New("native OpenShell removal requires the Linux VM controller")
}
