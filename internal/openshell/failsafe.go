package openshell

import (
	"context"
	"errors"
	"time"
)

// runtimeFailSafe ends one single wait that nothing else ends: code in the
// box, in the VM, on the server side, in detached cleanups, and steps reached
// from box attach's setup, which can outlive its client. A caller's earlier
// deadline or cancellation still ends the wait first. Tests shorten it.
var runtimeFailSafe = 2 * time.Minute

func runRuntimeWait(ctx context.Context, wait func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, runtimeFailSafe)
	defer cancel()
	err := wait(ctx)
	if err != nil && ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	return err
}
