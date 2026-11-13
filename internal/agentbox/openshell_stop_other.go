//go:build !linux

package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) stopNativeRuntime(context.Context, string, string) error {
	return errors.New("native OpenShell stop requires the Linux VM controller")
}

func (a LiveApplier) stopNativeRuntimeForResult(context.Context, string, string) error {
	return errors.New("native OpenShell stop requires the Linux VM controller")
}

// RemoveStoppedEndpoint is a Linux VM controller operation.
func (a LiveApplier) RemoveStoppedEndpoint(context.Context, string) error {
	return errors.New("native endpoint cleanup requires the Linux VM controller")
}
