package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func controllerMounts(boot *spaceconfig.Bootstrap) ([]vmhost.Mount, error) {
	var mounts []vmhost.Mount
	identity := &spaceconfig.Config{AccountDiskID: boot.DiskID}
	for _, m := range boot.Mounts {
		if err := rejectSpaceHostPath(m.Source, identity); err != nil {
			return nil, err
		}
		mounts = append(mounts, vmhost.Mount{Location: m.Source, Writable: m.Writable})
	}
	root, err := layout.ConfigRoot()
	if err != nil {
		return nil, err
	}
	for _, kind := range []string{"agents", "features", "capabilities"} {
		path := filepath.Join(root, kind)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
		if st, err := os.Lstat(path); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("catalog %s must be a directory, not a symlink", path)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, vmhost.Mount{Location: canonical, MountPoint: filepath.Join(spaceenv.CatalogRoot, kind), Uncached: true})
	}
	return mounts, nil
}

func nativeInfrastructure(ctx context.Context, space, action string, input any, out, errw io.Writer) error {
	cmd := vmhost.ShellCommand(ctx, space, "sudo", session.VMBinaryPath, "controller-internal", action)
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewReader(raw)
	}
	cmd.Stdout, cmd.Stderr = out, errw
	return cmd.Run()
}

// callHostController does not boot or install anything. Host lifecycle is the
// only place that can select a deployment or bring a stopped space online.
func callHostController(ctx context.Context, boot *spaceconfig.Bootstrap, req controller.Request, streams controller.ClientIO) error {
	bridge := vmhost.ShellCommand(ctx, boot.Name, "sudo", session.VMBinaryPath, "controller-internal", "connect")
	flush := vmhost.AttachFilteredStderr(bridge, streams.Err)
	defer flush()
	conn, err := controller.StartTransport(bridge)
	if err != nil {
		return err
	}
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: boot.Bundle, Space: boot.Name, Generation: boot.Generation}
	return controller.Call(ctx, conn, hello, req, streams)
}

func checkControllerSpace(ctx context.Context, space string) (*spaceconfig.Bootstrap, error) {
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return nil, err
	}
	if boot.ImageUpdate != nil {
		return nil, pendingImageUpdate(boot)
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil {
		return nil, err
	}
	if boot.Bundle != identity {
		return nil, asUserError(fmt.Errorf("space %s selects controller bundle %s; this CLI is %s; use the selected build, or explicitly run yoloctl space start %s --update-controller", space, boot.Bundle, identity, space))
	}
	probe, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	inst, found, err := findSpaceInstance(probe, space)
	if err != nil {
		return nil, err
	}
	if inst.Status == "Paused" && inst.StatusDetail != "" {
		return nil, asUserError(errors.New(inst.StatusDetail))
	}
	if !found || inst.Status != "Running" {
		return nil, asUserError(fmt.Errorf("space %s is not running; start it with: yoloctl space start %s", space, space))
	}
	return boot, nil
}

func controllerControl(ctx context.Context, boot *spaceconfig.Bootstrap, method string, errw io.Writer) error {
	return callHostController(ctx, boot, controller.Request{
		ID: rand.Text(), Method: method, Space: boot.Name, Dash: -1,
	}, controller.ClientIO{Out: io.Discard, Err: errw})
}

// A replacement VM may be running before its controller was installed. Only
// an explicit inactive/failed service result permits deployment after a failed
// drain; an unavailable or active controller must retain its operation boundary.
func drainControllerForDeployment(drain func(io.Writer) error, state func() ([]byte, error), errw io.Writer) error {
	var log bytes.Buffer
	if err := drain(&log); err != nil {
		raw, probeErr := state()
		if probeErr != nil {
			return fmt.Errorf("cannot establish controller state: %w", probeErr)
		}
		switch string(bytes.TrimSpace(raw)) {
		case "inactive", "failed":
			return nil
		default:
			_, _ = io.Copy(errw, &log)
			return fmt.Errorf("running controller is unavailable; refusing to interrupt its operations: %w", err)
		}
	}
	return nil
}

// startControllerSpace runs only under the host lifecycle lock. initialize is
// a one-time seed from creation or an explicit import, never host desired
// state for an already activated controller.
func startControllerSpace(ctx context.Context, boot *spaceconfig.Bootstrap, update bool, initialize *controller.StoreBundle, errw io.Writer) error {
	if boot.ImageUpdate != nil {
		return pendingImageUpdate(boot)
	}
	return startControllerSpaceImpl(ctx, boot, update, initialize, errw)
}

// These seams keep boot-authority race tests independent of a live hypervisor.
type controllerDeploymentVM interface {
	AccountDisk(context.Context, string) (*vmhost.DiskInfo, error)
	CreateAccountDisk(context.Context, string) error
	RequireAccountDisk(context.Context, string, string) error
	AccountDiskBlank(context.Context, string) (bool, error)
	CreateWithSpec(context.Context, string, vmhost.Spec) error
	SetResources(context.Context, string, vmapi.Resources) error
	SetInstanceMounts(string, []vmhost.Mount) error
	SetAccountDisk(string, string, bool) error
	Start(context.Context, string) error
}

var newDeploymentVM = func() controllerDeploymentVM { return vmhost.New() }
var lookupDeploymentVM = findSpaceInstance

// Only the explicit image-update transaction may deploy a selected checkpoint.
func startControllerSpaceImpl(ctx context.Context, boot *spaceconfig.Bootstrap, update bool, initialize *controller.StoreBundle, errw io.Writer) error {
	if err := vmhost.ValidateInstancePaths(boot.VM.Backend, boot.Name); err != nil {
		return asUserError(err)
	}
	if boot.VM.Backend != "firecracker" {
		if err := checkSpaceOpenShellImage(ctx, boot); err != nil {
			return err
		}
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	if boot.Bundle != "" && boot.Bundle != identity && !update {
		return asUserError(fmt.Errorf("space %s selects a different controller build; use its selected yoloctl or space start %s --update-controller", boot.Name, boot.Name))
	}
	vm := newDeploymentVM()
	if boot.Initializing && boot.DiskUUID == "" && initialize != nil {
		disk, err := vm.AccountDisk(ctx, boot.DiskID)
		if err != nil {
			return err
		}
		if disk == nil {
			dir, err := vmhost.AccountDiskDir(boot.DiskID)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				return errors.New("pending disk metadata exists but is unreadable; inspect it before retrying")
			}
			if err := vm.CreateAccountDisk(ctx, boot.DiskID); err != nil {
				return err
			}
		}
	}
	if err := vm.RequireAccountDisk(ctx, boot.Name, boot.DiskID); err != nil {
		return err
	}
	inst, found, err := lookupDeploymentVM(ctx, boot.Name)
	if err != nil {
		return err
	}
	if inst.Status == "Paused" && inst.StatusDetail != "" {
		return asUserError(errors.New(inst.StatusDetail))
	}
	running := found && inst.Status == "Running"
	if installBootForbidden(ctx) && !running {
		return skipped(errors.New("the consented running VM is no longer running; boot is forbidden"))
	}
	if running {
		if err := checkSpaceOpenShellImage(ctx, boot); err != nil {
			return err
		}
	}
	if running && boot.Bundle != "" {
		if boot.Bundle == identity && !update {
			// A healthy matching controller needs no rewrites or restart.
			if err := controllerControl(ctx, boot, "control.ping", io.Discard); err == nil {
				if initialize != nil {
					var raw bytes.Buffer
					if err := nativeInfrastructure(ctx, boot.Name, "store-state", nil, &raw, errw); err != nil {
						return err
					}
					var state controller.StoreState
					if err := json.Unmarshal(raw.Bytes(), &state); err != nil {
						return err
					}
					digest, err := initialize.Digest(boot)
					if err != nil || !state.Activated || digest != state.Digest {
						return errors.New("active initialization checkpoint differs; recovery required")
					}
				}
				return nil
			}
		}
		if err := drainControllerForDeployment(func(log io.Writer) error {
			return controllerControl(ctx, boot, "control.drain", log)
		}, func() ([]byte, error) {
			return vmhost.ControllerService(ctx, boot.Name, "state").Output()
		}, errw); err != nil {
			return err
		}
	}
	mounts, err := controllerMounts(boot)
	if err != nil {
		return err
	}
	if !running {
		format := false
		if boot.Initializing && boot.DiskUUID == "" {
			format, err = vm.AccountDiskBlank(ctx, boot.DiskID)
			if err != nil {
				return fmt.Errorf("check pending data disk initialization: %w", err)
			}
		}
		if !found {
			if boot.VM.Backend == "firecracker" {
				_, _ = fmt.Fprintf(errw, "formatting the %s system disk…\n", diskLabel(boot.VM.Disk))
			}
			if err := vm.CreateWithSpec(ctx, boot.Name, vmhost.Spec{Mounts: mounts, DiskID: boot.DiskID, Format: format, CPUs: boot.VM.CPUs, Memory: boot.VM.Memory, Disk: boot.VM.Disk, Image: boot.VM.Image}); err != nil {
				return err
			}
		} else {
			if err := vm.SetResources(ctx, boot.Name, vmapi.Resources{CPUs: boot.VM.CPUs, Memory: boot.VM.Memory}); err != nil {
				return fmt.Errorf("apply saved VM resources before starting: %w", err)
			}
			if err := vm.SetInstanceMounts(boot.Name, mounts); err != nil {
				return err
			}
			if err := vm.SetAccountDisk(boot.Name, boot.DiskID, format); err != nil {
				return err
			}
		}
		if err := vm.Start(ctx, boot.Name); err != nil {
			return err
		}
	}
	if !running {
		if err := checkSpaceOpenShellImage(ctx, boot); err != nil {
			return err
		}
	}
	if boot.DiskUUID == "" {
		if !boot.Initializing {
			return errors.New("data disk has no initialization authority")
		}
		disk, err := accountstate.ExecuteForDisk(ctx, boot.Name, boot.DiskID, boot.DiskUUID, boot.UID, boot.GID, accountstate.Request{Action: "initialize"})
		if err != nil {
			return err
		}
		if disk.UUID == "" {
			return errors.New("data disk did not report its UUID")
		}
		if err := vm.SetAccountDisk(boot.Name, boot.DiskID, false); err != nil {
			return err
		}
		boot.DiskUUID, boot.Initializing = disk.UUID, false
		if err := boot.Save(); err != nil {
			return err
		}
	}
	if err := session.EnsureVMBinary(ctx, container.New(boot.Name)); err != nil {
		return err
	}
	if boot.Bundle != identity {
		boot.Bundle, boot.Generation = identity, rand.Text()
	}
	root, err := layout.ConfigRoot()
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if boot.Import != nil {
		if err := restoreControllerSpace(ctx, boot, boot.Context(root), errw); err != nil {
			return err
		}
		boot.Import = nil
	}
	// Publish boot authority before opening guest admission. A failed deployment
	// retains the bootstrap and initialization seed for explicit lifecycle retry.
	if err := boot.Save(); err != nil {
		return err
	}
	deployment := controllerDeployment{Context: boot.Context(root), Initialize: initialize}
	if err := nativeInfrastructure(ctx, boot.Name, "deploy", deployment, io.Discard, errw); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		probe, stop := context.WithTimeout(readyCtx, vmhost.GuestProbeTimeout)
		err = controllerControl(probe, boot, "control.ping", io.Discard)
		stop()
		if err == nil {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("controller did not become ready: %w", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func stopControllerSpace(ctx context.Context, boot *spaceconfig.Bootstrap, errw io.Writer) error {
	inst, found, err := findSpaceInstance(ctx, boot.Name)
	if err != nil || !found || inst.Status == "Stopped" {
		return err
	}
	if inst.Status == "Paused" && inst.StatusDetail != "" {
		return asUserError(errors.New(inst.StatusDetail))
	}
	graceful, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Leave at least 30 seconds of the total budget for the VM's graceful
	// poweroff (and guest filesystem flush), even if workload cleanup stalls.
	cleanup, finishCleanup := context.WithTimeout(graceful, 90*time.Second)
	defer finishCleanup()
	if err := controllerControl(cleanup, boot, "control.drain", errw); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: controller drain unavailable: %v\n", err)
	} else {
		// Root-only cleanup is callable after admission closes.
		if err := nativeInfrastructure(cleanup, boot.Name, "stop-workloads", nil, io.Discard, errw); err != nil {
			_, _ = fmt.Fprintf(errw, "warning: workload cleanup failed: %v\n", err)
		}
	}
	finishCleanup()
	if err := vmhost.New().Stop(graceful, boot.Name); err == nil || errors.Is(err, vmapi.ErrHostStoragePaused) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, _ = fmt.Fprintln(errw, "warning: graceful shutdown failed; forcing VM stop; unflushed writes may be lost")
	forced, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	return vmhost.New().ForceStop(forced, boot.Name, true)
}
