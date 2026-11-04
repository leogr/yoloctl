package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// A fixed, root-only lifecycle transaction, never an operator RPC. Grouping
// deployment avoids repeated SSH/CLI startup and native-disk preparation without
// skipping any identity, store, or admission checks.
type controllerDeployment struct {
	Context    spaceenv.Context        `json:"context"`
	Initialize *controller.StoreBundle `json:"initialize,omitempty"`
}

func deployController(ctx context.Context, d controllerDeployment) error {
	c := d.Context
	if err := c.Validate(); err != nil {
		return err
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil || identity != c.Bundle {
		return errors.New("bootstrap and installed controller builds differ")
	}
	var stop *exec.Cmd
	if c.ServiceManager == "microvm" {
		stop = exec.CommandContext(ctx, "/sbin/yoloctl-init", "controller", "stop")
	} else {
		stop = exec.CommandContext(ctx, "sh", "-c", `set -eu
state=$(systemctl show -p LoadState --value yoloctl-controller.service)
if [ "$state" != "not-found" ]; then systemctl stop yoloctl-controller.service; fi`)
	}
	stop.Stderr = os.Stderr
	if err := stop.Run(); err != nil {
		return err
	}
	if err := writeControllerContext(c); err != nil {
		return err
	}
	if err := controller.PrepareRuntimeForLifecycle(ctx); err != nil {
		return err
	}
	boot := &spaceconfig.Bootstrap{Name: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID}
	for _, m := range c.Mounts {
		boot.Mounts = append(boot.Mounts, spaceconfig.Mount{Source: m.Source, Writable: m.Writable})
	}
	if d.Initialize != nil {
		state, err := controller.InspectStore(c.DiskMount)
		if err != nil {
			return err
		}
		if state.Activated {
			digest, err := d.Initialize.Digest(boot)
			if err != nil || digest != state.Digest {
				return errors.New("initialization checkpoint differs from the active store; recovery required")
			}
		} else if _, err := controller.PublishStore(c.DiskMount, boot, *d.Initialize); err != nil {
			return err
		}
	}
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	if _, err := spaceconfig.Load(c.Space); err != nil {
		return err
	}
	if err := controller.ActivateStore(c.DiskMount); err != nil {
		return err
	}
	if err := safepath.RemoveFile(filepath.Join(c.DiskMount, ".controller"), "maintenance"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var start *exec.Cmd
	if c.ServiceManager == "microvm" {
		start = exec.CommandContext(ctx, "/sbin/yoloctl-init", "controller", "start")
	} else {
		if err := runControllerInternal(ctx, []string{"install-service"}); err != nil {
			return err
		}
		start = exec.CommandContext(ctx, "systemctl", "start", controller.ServiceName)
	}
	start.Stderr = os.Stderr
	return start.Run()
}
