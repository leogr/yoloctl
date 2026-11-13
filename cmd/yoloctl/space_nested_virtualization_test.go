package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

type nestedDeploymentVM struct {
	controllerDeploymentVM
	applied []vmapi.Resources
}

func (v *nestedDeploymentVM) SetResources(_ context.Context, _ string, r vmapi.Resources) error {
	v.applied = append(v.applied, r)
	return nil
}

func TestNestedBootResourcesResolveListedInstanceAndRefuseUnknownIdentity(t *testing.T) {
	oldHost, oldLookup := nestedHostFacts, lookupDeploymentVM
	t.Cleanup(func() { nestedHostFacts, lookupDeploymentVM = oldHost, oldLookup })
	nestedHostFacts = func() vmapi.NestedHostFacts {
		return vmapi.NestedHostFacts{OS: "darwin", Chip: "Apple M3 Max", Version: "26.7.1"}
	}
	lookupDeploymentVM = func(context.Context, string) (vmapi.Instance, bool, error) {
		return vmapi.Instance{VMType: "vz", Arch: "aarch64", Status: "Stopped"}, true, nil
	}
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{CPUs: 4, Memory: "4GiB", Backend: "lima"}}
	vm := &nestedDeploymentVM{}
	if err := applySpaceBootResources(t.Context(), boot, vm); err != nil {
		t.Fatal(err)
	}
	if len(vm.applied) != 1 || vm.applied[0].NestedVirtualization == nil || !*vm.applied[0].NestedVirtualization {
		t.Fatal(vm.applied)
	}
	if boot.VM.NestedVirtualization != "" {
		t.Fatal("old absent preference was rewritten")
	}
	lookupDeploymentVM = func(context.Context, string) (vmapi.Instance, bool, error) {
		return vmapi.Instance{}, false, errors.New("identity unavailable")
	}
	if err := applySpaceBootResources(t.Context(), boot, vm); err == nil {
		t.Fatal("accepted unreadable instance")
	}
	if len(vm.applied) != 1 {
		t.Fatal("applied after identity failure")
	}
}

func TestNestedBootNoticeReasonsAndSuppression(t *testing.T) {
	for _, tt := range []struct{ os, backend, driver, arch, chip, version, reason string }{
		{"linux", "lima", "qemu", "aarch64", "", "", "yoloctl does not manage it for Lima on this system"},
		{"linux", "firecracker", "", "", "", "", "yoloctl does not manage it for the Firecracker backend"},
		{"darwin", "lima", "vz", "aarch64", "Apple M2", "15", "this Mac needs Apple M3 or later and macOS 15 or later (found Apple M2, macOS 15)"},
		{"darwin", "lima", "vz", "aarch64", "", "", "yoloctl could not tell whether this Mac supports it; --nested-virtualization on makes Lima decide"},
		{"darwin", "lima", "vz", "", "Apple M3", "15", "yoloctl manages this setting only for Lima vz instances with a native aarch64 guest (found driver vz, guest unknown); the VM type is fixed at creation"},
	} {
		result, err := vmapi.ResolveNestedVirtualization("auto", tt.backend, vmapi.Instance{VMType: tt.driver, Arch: tt.arch}, vmapi.NestedHostFacts{OS: tt.os, Chip: tt.chip, Version: tt.version})
		if err != nil || result.Reason != tt.reason {
			t.Fatalf("%+v: %+v %v", tt, result, err)
		}
		want := "nested virtualization is not enabled by yoloctl for space fixture: " + tt.reason + ". To silence this: yoloctl space set fixture --nested-virtualization off"
		if got := nestedBootNotice("fixture", "", result); got != want {
			t.Fatalf("notice: %s", got)
		}
		for _, pref := range []string{"on", "off"} {
			if got := nestedBootNotice("fixture", pref, result); got != "" {
				t.Fatal(got)
			}
		}
	}
	if got := nestedBootNotice("fixture", "auto", vmapi.NestedResolution{Enabled: true}); got != "" {
		t.Fatal(got)
	}
}

func TestSpaceSetNestedPreferenceOnlyPersistsChosenValue(t *testing.T) {
	_, _ = resourceSpaceFixture(t)
	for _, value := range []string{"on", "off", "auto"} {
		if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", value); err != nil {
			t.Fatal(err)
		}
		boot, err := spaceconfig.LoadBootstrap("dev")
		if err != nil || boot.VM.NestedVirtualization != value {
			t.Fatalf("%+v %v", boot, err)
		}
	}
	if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", "sometimes"); err == nil || !strings.Contains(err.Error(), "auto, on or off") {
		t.Fatal(err)
	}
}

func TestNestedShowReadsConfiguredFileWithoutClaimingEffectiveState(t *testing.T) {
	boot, _ := resourceSpaceFixture(t)
	boot.VM.Backend = "lima"
	boot.VM.NestedVirtualization = "off"
	old := nestedHostFacts
	t.Cleanup(func() { nestedHostFacts = old })
	nestedHostFacts = func() vmapi.NestedHostFacts { return vmapi.NestedHostFacts{OS: "linux"} }
	dir, err := lima.InstanceDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lima.yaml")
	if err := os.WriteFile(path, []byte("nestedVirtualization: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := spaceShowData{Instance: vmapi.Instance{Name: "dev"}}
	fillNestedSpaceShow(&view, boot)
	if view.NestedPreference != "off" || view.NestedConfigured != "true" || view.NestedHostEligibility != "not managed on this system" {
		t.Fatalf("%+v", view)
	}
	var output bytes.Buffer
	if err := writeSpaceShowJSON(&output, &view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"nested_virtualization_configured": "true"`) {
		t.Fatal(output.String())
	}
	if err := os.Rename(path, path+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".retained", path); err != nil {
		t.Fatal(err)
	}
	fillNestedSpaceShow(&view, boot)
	if view.NestedConfigured != "unavailable" {
		t.Fatalf("followed unsafe file: %+v", view)
	}
}

func TestSpaceSetNestedPreferenceRefusesRunningVM(t *testing.T) {
	before, applied := resourceSpaceFixture(t)
	lookupResourceSpace = func(context.Context, string) (vmapi.Instance, bool, error) {
		return vmapi.Instance{Status: "Running"}, true, nil
	}
	if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", "on"); err == nil {
		t.Fatal("running space accepted")
	}
	after, err := spaceconfig.LoadBootstrap("dev")
	if err != nil {
		t.Fatal(err)
	}
	if after.VM.NestedVirtualization != before.VM.NestedVirtualization || len(*applied) != 0 {
		t.Fatal("changed running space")
	}
}
