package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/processio"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmwire"
)

var spaceOpenShellInitCommand = vmhost.ShellCommand
var spaceOpenShellInstance = findSpaceInstance

// Check the running init before draining/replacing a controller. Starting a
// stopped VM may be necessary to read its capability, but this never updates
// media, drains the old controller or touches a box. Image replacement stays
// an explicit space recreate operation.
func checkSpaceOpenShellImage(ctx context.Context, boot *spaceconfig.Bootstrap) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if boot.VM.Backend != "firecracker" {
		err := lima.CheckOpenShellVM(boot.Name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} // a fresh VM uses this build's template
		if errors.Is(err, lima.ErrOpenShellVMTooOld) {
			return asUserError(&spaceVMCompatibilityError{Space: boot.Name, Backend: "lima"})
		}
		if err != nil {
			return err
		}
		inst, found, err := spaceOpenShellInstance(ctx, boot.Name)
		if err != nil || !found || inst.Status != "Running" {
			return err
		}
		return checkRunningLimaNFT(ctx, boot)
	}
	raw, err := json.Marshal(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
	if err != nil {
		return err
	}
	args := []string{"sudo", "/sbin/yoloctl-init", "unit"}
	cmd := spaceOpenShellInitCommand(ctx, boot.Name, args...)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr spaceOpenShellInitError
	err = processio.Capture(ctx, cmd, &stderr, false, 2*time.Minute)
	if cause := ctx.Err(); cause != nil {
		return spaceOpenShellCheckContextError("check VM image OpenShell support", cause, args, stderr)
	}
	if err == nil && !stderr.overflow {
		return nil
	}
	if !stderr.overflow && strings.Contains(string(stderr.data), "invalid managed service identity") {
		return asUserError(&spaceVMCompatibilityError{Space: boot.Name, Backend: "firecracker"})
	}
	if stderr.overflow {
		err = errors.Join(err, errors.New("guest init diagnostic exceeded capture bound"))
	}
	return fmt.Errorf("check VM image OpenShell support: %w", err)
}

// Lima may report Running even when its system provision failed. Check the
// fixed guest executable before controller/box effects, without installing it.
func checkRunningLimaNFT(ctx context.Context, boot *spaceconfig.Bootstrap) error {
	const probe = "if ! test -x /usr/sbin/nft || ! /usr/sbin/nft --version >/dev/null 2>&1; then exit 78; fi"
	args := []string{"sudo", "sh", "-c", probe}
	cmd := spaceOpenShellInitCommand(ctx, boot.Name, args...)
	var stderr spaceOpenShellInitError
	err := processio.Capture(ctx, cmd, &stderr, false, 2*time.Minute)
	if cause := ctx.Err(); cause != nil {
		return spaceOpenShellCheckContextError("check Lima OpenShell nft support", cause, args, stderr)
	}
	if stderr.overflow {
		return errors.Join(errors.New("guest nft diagnostic exceeded capture bound"), err)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 78 {
		return asUserError(&spaceVMCompatibilityError{Space: boot.Name, Backend: "lima"})
	}
	if err != nil {
		return fmt.Errorf("check Lima OpenShell nft support: %w", err)
	}
	return nil
}

func spaceOpenShellCheckContextError(check string, cause error, args []string, stderr spaceOpenShellInitError) error {
	if !errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", check, cause)
	}
	output := "no stderr output"
	switch {
	case stderr.overflow:
		output = fmt.Sprintf("stderr (first %d bytes) %q", spaceOpenShellDiagnosticLimit, stderr.data)
	case len(stderr.data) != 0:
		output = fmt.Sprintf("stderr %q", stderr.data)
	}
	return fmt.Errorf("%s: %w (guest command %s, %s)", check, cause, remoteCommandString(args), output)
}

const spaceOpenShellDiagnosticLimit = 512

// Named storage avoids promoted ReaderFrom bypasses during exec's io.Copy.
type spaceOpenShellInitError struct {
	data     []byte
	overflow bool
}

func (w *spaceOpenShellInitError) Write(p []byte) (int, error) {
	n := len(p)
	keep := spaceOpenShellDiagnosticLimit - len(w.data)
	if keep > len(p) {
		keep = len(p)
	}
	w.data = append(w.data, p[:keep]...)
	if keep < n {
		w.overflow = true
	}
	return n, nil
}
