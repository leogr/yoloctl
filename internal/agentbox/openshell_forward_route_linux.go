package agentbox

import (
	"context"

	"github.com/leogr/yoloctl/internal/port"
)

func (a LiveApplier) startNativePortForward(ctx context.Context, spec port.Spec) error {
	return StartNativeOpenShellForward(ctx, a.Client, spec, a.VerifyStorage)
}
