package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func pendingImageUpdate(boot *spaceconfig.Bootstrap) error {
	return fmt.Errorf("space %s has a pending VM image update; resume with space recreate %s --update-vm-image, or recover with --rollback-vm-image (compatible images only)", boot.Name, boot.Name)
}

func recreateSpaceImage(cmd *cobra.Command, boot *spaceconfig.Bootstrap) error {
	if boot.VM.Backend != "firecracker" {
		return errors.New("VM image update flags apply only to Firecracker spaces")
	}
	if boot.Initializing || boot.DiskUUID == "" || boot.Import != nil {
		return errors.New("finish space initialization/import before updating its VM image")
	}
	if _, err := controllerMounts(boot); err != nil {
		return err
	}
	if err := vmhost.New().RequireAccountDisk(cmd.Context(), boot.Name, boot.DiskID); err != nil {
		return err
	}
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	source, _ := cmd.Flags().GetString("vm-image")
	if cmd.Flags().Changed("vm-image") && source == "" {
		return errors.New("--vm-image requires a prepared image directory")
	}
	rollback, _ := cmd.Flags().GetBool("rollback-vm-image")
	err = replaceSpaceImage(cmd.Context(), boot, source, rollback, bundle, cmd.ErrOrStderr(), imageUpdateOps{
		resolve: vmhost.ResolveImage,
		remove:  deleteSpaceRuntime,
		start: func(ctx context.Context, b *spaceconfig.Bootstrap, errw io.Writer) error {
			return startControllerSpaceImpl(ctx, b, true, nil, errw)
		},
		save: (*spaceconfig.Bootstrap).Save,
	})
	if err != nil {
		return err
	}
	cliInfo(logging.Host, "recreated space %s with VM image %s; start boxes with box start --space %s", boot.Name, boot.VM.Image, boot.Name)
	return nil
}

// These operations are the existing verified image installer, termination-proof
// teardown, controller deployment and durable bootstrap publication. The host
// lifecycle lock spans this whole transaction. Tests inject boundary failures.
type imageUpdateOps struct {
	resolve func(context.Context, string, string) (string, error)
	remove  func(context.Context, string, io.Writer) error
	start   func(context.Context, *spaceconfig.Bootstrap, io.Writer) error
	save    func(*spaceconfig.Bootstrap) error
}

func replaceSpaceImage(ctx context.Context, boot *spaceconfig.Bootstrap, source string, rollback bool, bundle string, errw io.Writer, op imageUpdateOps) error {
	u := boot.ImageUpdate
	if u != nil && u.Bundle != bundle {
		return errors.New("pending VM image update belongs to a different yoloctl build; resume with that build")
	}
	if rollback && u == nil {
		return errors.New("no pending VM image update to roll back; use --vm-image for an explicit replacement")
	}
	if u != nil && source != "" {
		return pendingImageUpdate(boot)
	}
	if rollback {
		source = u.Previous
	} else if u != nil {
		source = u.Target
	}
	// Resolve verifies protocol, manifest and bytes before any VM teardown. An
	// older image can be selected for recovery only if this build supports it.
	target, err := op.resolve(ctx, boot.VM.Backend, source)
	if err != nil {
		return fmt.Errorf("verify replacement VM image (existing VM unchanged): %w", err)
	}
	if u == nil || rollback {
		u = &spaceconfig.VMImageUpdate{Previous: boot.VM.Image, Target: target, Bundle: bundle, Phase: "prepared"}
		boot.ImageUpdate = u
		if err := op.save(boot); err != nil {
			return err
		}
	} else if target != u.Target {
		return errors.New("pending VM image identity changed; refusing replacement")
	}
	if u.Phase == "prepared" {
		// Deletion proves the previous VM is gone before selection changes. If
		// interrupted after deletion, repeating it is safe; data disk survives.
		if err := op.remove(ctx, boot.Name, errw); err != nil {
			return fmt.Errorf("VM image update pending: %w", err)
		}
		boot.VM.Image = u.Target
		u.Phase = "selected"
		if err := op.save(boot); err != nil {
			return err
		}
	}
	// A selected checkpoint retries boot/deployment without deleting the new VM.
	if err := op.start(ctx, boot, errw); err != nil {
		return fmt.Errorf("replacement failed; accounts/declarations retained; %w: %w", pendingImageUpdate(boot), err)
	}
	boot.ImageUpdate = nil
	if err := op.save(boot); err != nil {
		boot.ImageUpdate = u
		return err
	}
	return nil
}
