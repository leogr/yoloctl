package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

type spaceVMCompatibilityError struct{ Space, Backend string }

func (e *spaceVMCompatibilityError) command() string {
	cmd := "yoloctl space recreate " + e.Space
	if e.Backend == "firecracker" {
		cmd += " --update-vm-image"
	}
	return cmd
}
func (e *spaceVMCompatibilityError) Error() string {
	return fmt.Sprintf("this space's VM was made by an older yoloctl; its OpenShell setup cannot run this build's boxes. Run `%s`, then retry this command", e.command())
}

var checkCommandVMCompatibility = checkRunningSpaceVMCompatibility
var recreateCompatibleSpace func(*cobra.Command, *spaceconfig.Bootstrap) error

// Root construction registers completion callbacks which route back here.
// Bind this execution seam after variable initialization, never as an init
// expression that closes that callback dependency cycle.
func init() { recreateCompatibleSpace = executeCompatibleSpaceRecreate }

func executeCompatibleSpaceRecreate(cmd *cobra.Command, boot *spaceconfig.Bootstrap) error {
	root := newClientRootCmd()
	root.SetIn(cmd.InOrStdin())
	root.SetOut(cmd.OutOrStdout())
	root.SetErr(cmd.ErrOrStderr())
	args := []string{"space", "recreate", boot.Name, "--force"}
	if boot.VM.Backend == "firecracker" {
		args = append(args, "--update-vm-image")
	}
	root.SetArgs(args)
	return root.ExecuteContext(cmd.Context())
}

func checkRunningSpaceVMCompatibility(ctx context.Context, boot *spaceconfig.Bootstrap) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if boot.ImageUpdate != nil {
		return pendingImageUpdate(boot)
	}
	if boot.VM.Backend != "firecracker" {
		return checkSpaceOpenShellImage(ctx, boot)
	}
	inst, found, err := findSpaceInstance(ctx, boot.Name)
	if err != nil {
		return err
	}
	if !found || inst.Status != "Running" {
		return nil
	} // box commands never boot a stopped VM
	return checkSpaceOpenShellImage(ctx, boot)
}

// The original box request has not been sent. Only a typed, read-only VM
// mismatch can offer recreation; lost replies and arbitrary errors never retry.
func ensureCommandVMCompatibility(cmd *cobra.Command, space string) error {
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return err
	}
	err = checkCommandVMCompatibility(cmd.Context(), boot)
	if err == nil {
		return nil
	}
	if err = offerSpaceVMRecreate(cmd, boot, err); err != nil {
		return err
	}
	current, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return err
	}
	return checkCommandVMCompatibility(cmd.Context(), current) // one attempt, never a prompt loop
}

func offerSpaceVMRecreate(cmd *cobra.Command, boot *spaceconfig.Bootstrap, cause error) error {
	var mismatch *spaceVMCompatibilityError
	if !errors.As(cause, &mismatch) {
		return cause
	}
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	// Installer consent covers its printed command and locked snapshot. A VM
	// replacement needs a separately selected lifecycle command, not this offer.
	if hasInstallPrecondition(cmd) {
		return cause
	}
	if !stdinIsTerminalFn(cmd.InOrStdin()) || !writerIsTerminal(cmd.ErrOrStderr()) {
		return cause
	}
	prompt := fmt.Sprintf("This space's VM was made by an older yoloctl: its OpenShell setup cannot run this build's boxes. Recreate VM %s now? Accounts, box settings and host workdirs are kept; running boxes and sessions stop, and ephemeral workdirs are lost. The requested operation then continues. [y/N]: ", boot.Name)
	yes, err := promptYesNo(prompt, cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if !yes {
		return cause
	}
	return recreateCompatibleSpace(cmd, boot)
}
func commandNeedsVMCompatibility(cmd *cobra.Command) bool {
	if flag := cmd.Flag("manifest-only"); flag != nil && flag.Value.String() == "true" {
		return false
	}
	switch commandMethod(cmd) {
	case "box.create", "box.copy", "box.start", "box.restart", "box.recreate", "box.reconcile", "box.destroy":
		return true
	}
	return false
}
