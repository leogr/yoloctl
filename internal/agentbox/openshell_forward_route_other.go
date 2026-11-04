//go:build !linux

package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/port"
)

func (a LiveApplier) startNativePortForward(context.Context, port.Spec) error {
	return errors.New("native OpenShell forwards require the Linux VM controller")
}
