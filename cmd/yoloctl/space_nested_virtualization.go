package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/leogr/yoloctl/internal/lima"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

var nestedHostFacts = vmapi.ReadNestedHostFacts

// applySpaceBootResources runs after creation too: Lima has now selected the
// actual driver and guest architecture, and no boot has happened yet.
func applySpaceBootResources(ctx context.Context, boot *spaceconfig.Bootstrap, vm controllerDeploymentVM) error {
	host := nestedHostFacts()
	var instance vmapi.Instance
	if host.OS == "darwin" && boot.VM.Backend != "firecracker" {
		row, found, err := lookupDeploymentVM(ctx, boot.Name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("VM for space %q is not listed", boot.Name)
		}
		instance = row
	}
	result, err := vmapi.ResolveNestedVirtualization(boot.VM.NestedVirtualization, boot.VM.Backend, instance, host)
	if err != nil {
		return fmt.Errorf("nested virtualization: %w; use yoloctl space set %s --nested-virtualization off", err, boot.Name)
	}
	resources := vmapi.Resources{CPUs: boot.VM.CPUs, Memory: boot.VM.Memory}
	if result.Managed {
		resources.NestedVirtualization = &result.Enabled
	}
	if err := vm.SetResources(ctx, boot.Name, resources); err != nil {
		return fmt.Errorf("apply saved VM resources before starting: %w", err)
	}
	if notice := nestedBootNotice(boot.Name, boot.VM.NestedVirtualization, result); notice != "" {
		cliNotice(logging.Host, "%s", notice)
	}
	return nil
}

// fillNestedSpaceShow labels configured state separately from host eligibility.
func fillNestedSpaceShow(view *spaceShowData, boot *spaceconfig.Bootstrap) {
	view.NestedPreference = vmapi.NestedPreference(boot.VM.NestedVirtualization)
	host := nestedHostFacts()
	view.NestedHostEligibility = "not eligible"
	if host.Eligible() {
		view.NestedHostEligibility = "eligible"
	}
	if host.OS != "darwin" {
		view.NestedHostEligibility = "not managed on this system"
	} else if host.Chip == "" || host.Version == "" {
		view.NestedHostEligibility = "unavailable"
	}
	result, _ := vmapi.ResolveNestedVirtualization("auto", boot.VM.Backend, view.Instance, host)
	view.NestedReason = result.Reason
	view.NestedConfigured = "not managed"
	if boot.VM.Backend != "firecracker" {
		value, err := lima.InstanceNestedVirtualization(boot.Name)
		switch {
		case err != nil:
			view.NestedConfigured = "unavailable"
		case value == nil:
			view.NestedConfigured = "not set"
		default:
			view.NestedConfigured = strconv.FormatBool(*value)
		}
	}
}

func nestedBootNotice(name, preference string, result vmapi.NestedResolution) string {
	if vmapi.NestedPreference(preference) != "auto" || result.Enabled {
		return ""
	}
	return fmt.Sprintf("nested virtualization is not enabled by yoloctl for space %s: %s. To silence this: yoloctl space set %s --nested-virtualization off", name, result.Reason, name)
}
