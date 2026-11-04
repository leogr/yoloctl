//go:build !linux

package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) stopNativeRuntime(context.Context, string, string) error {
	return errors.New("native OpenShell stop requires the Linux VM controller")
}
