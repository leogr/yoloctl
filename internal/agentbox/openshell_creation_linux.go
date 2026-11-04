package agentbox

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func (a LiveApplier) nativeCreationPrerequisite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.VerifyStorage == nil || a.Client == nil || a.nativeCreation == nil {
		return errors.New("native creation qualification requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	if err := a.VerifyStorage(ctx); err != nil {
		return err
	}
	if a.nativeCreation.release.Identity() == "" {
		return errors.New("native creation release is not qualified")
	}
	return a.nativeCreation.release.ValidateHelperSource()
}

func (a LiveApplier) preflightQualifiedNativeCreation(ctx context.Context, c *Config, image string, policy []byte) error {
	if err := a.nativeCreationPrerequisite(ctx); err != nil {
		return err
	}
	if err := nativeCreationImage(image); err != nil {
		return err
	}
	if err := a.PreflightNativeImage(ctx, image); err != nil {
		return err
	}
	if err := a.Client.RequireWorkloadPool(ctx); err != nil {
		return err
	}
	selection := a.nativeCreation
	// This is pure validation of the same options/policy assembler used by
	// actual creation. The all-zero identity is discarded, never allocated,
	// published or used as a reservation/activation/cleanup capability.
	if _, _, err := planNativeContainer(c, image, *spaceenv.Current(), selection.release, strings.Repeat("0", 32), policy); err != nil {
		return err
	}
	if _, err := openshell.InstallNativeRelease(ctx, selection.release, selection.rego); err != nil {
		return err
	}
	_, err := openshell.InstallNativeHelper(ctx, selection.release)
	return err
}

func (a LiveApplier) createQualifiedNativeContainer(ctx context.Context, c *Config, image string, policy []byte) error {
	if err := a.preflightQualifiedNativeCreation(ctx, c, image, policy); err != nil {
		return err
	}
	return a.createNativeContainer(ctx, c, image, policy, a.nativeCreation.release, a.nativeCreation.rego)
}
