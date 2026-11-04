package agentbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// PreflightNativeImage reads the final selected image before any native setup,
// reservation, container create or replacement teardown. Callers retain main's
// immutable prepared-image pin throughout preflight and apply. This check grants
// no helper/process/namespace authority and never overrides image configuration.
// Replacement wiring must call it before ending an existing session.
func (a LiveApplier) PreflightNativeImage(ctx context.Context, image string) error {
	if a.Client == nil || image == "" {
		return errors.New("native image preflight requires the selected runtime image")
	}
	entrypoint, err := a.Client.ImageEntrypoint(ctx, image, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("inspect selected native runtime image: %w", err)
	}
	if len(entrypoint) != 0 {
		return fmt.Errorf("OpenShell native runtime does not support image %q with inherited ENTRYPOINT %q; rebuild the base without ENTRYPOINT or move its setup into a feature/RUN step", image, entrypoint)
	}
	return nil
}
