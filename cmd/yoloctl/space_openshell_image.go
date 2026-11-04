package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/lima"
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
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
	if err != nil {
		return err
	}
	cmd := spaceOpenShellInitCommand(read, boot.Name, "sudo", "/sbin/yoloctl-init", "unit")
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = io.Discard
	var stderr spaceOpenShellInitError
	cmd.Stderr = &stderr
	cmd.WaitDelay = 250 * time.Millisecond
	err = cmd.Run()
	if read.Err() != nil {
		return fmt.Errorf("check VM image OpenShell support: %w", read.Err())
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
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const probe = "if ! test -x /usr/sbin/nft || ! /usr/sbin/nft --version >/dev/null 2>&1; then exit 78; fi"
	cmd := spaceOpenShellInitCommand(read, boot.Name, "sudo", "sh", "-c", probe)
	cmd.Stdout = io.Discard
	var stderr spaceOpenShellInitError
	cmd.Stderr = &stderr
	cmd.WaitDelay = 250 * time.Millisecond
	err := cmd.Run()
	if read.Err() != nil {
		return fmt.Errorf("check Lima OpenShell nft support: %w", read.Err())
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

// Named storage avoids promoted ReaderFrom bypasses during exec's io.Copy.
type spaceOpenShellInitError struct {
	data     []byte
	overflow bool
}

func (w *spaceOpenShellInitError) Write(p []byte) (int, error) {
	n := len(p)
	keep := 512 - len(w.data)
	if keep > len(p) {
		keep = len(p)
	}
	w.data = append(w.data, p[:keep]...)
	if keep < n {
		w.overflow = true
	}
	return n, nil
}
