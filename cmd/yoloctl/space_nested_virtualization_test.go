package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

type nestedDeploymentVM struct {
	controllerDeploymentVM
	applied  []vmapi.Resources
	starts   int
	startErr error
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
		{"darwin", "lima", "vz", "aarch64", "Apple M2", "15", "this Mac needs Apple M3 or later and macOS 15 or later (found Apple M2, macOS 15)"},
		{"darwin", "lima", "vz", "aarch64", "", "", "yoloctl could not tell whether this Mac supports it; --nested-virtualization on makes Lima decide"},
		{"darwin", "lima", "vz", "", "Apple M3", "15", "yoloctl manages this setting only for Lima vz instances with a native aarch64 guest (found driver vz, guest unknown); the VM type is fixed at creation"},
	} {
		result, err := vmapi.ResolveNestedVirtualization("auto", tt.backend, vmapi.Instance{VMType: tt.driver, Arch: tt.arch}, vmapi.NestedHostFacts{OS: tt.os, Chip: tt.chip, Version: tt.version})
		if err != nil || result.Reason != tt.reason {
			t.Fatalf("%+v: %+v %v", tt, result, err)
		}
		want := "nested virtualization is not enabled by yoloctl for space fixture: " + tt.reason + ". To silence this: yoloctl space set fixture --nested-virtualization off"
		if got := nestedBootNotice("fixture", tt.backend, "", result); got != want {
			t.Fatalf("notice: %s", got)
		}
		for _, pref := range []string{"on", "off"} {
			if got := nestedBootNotice("fixture", tt.backend, pref, result); got != "" {
				t.Fatal(got)
			}
		}
	}
	if got := nestedBootNotice("fixture", "lima", "auto", vmapi.NestedResolution{Enabled: true}); got != "" {
		t.Fatal(got)
	}
}

func TestFirecrackerDefaultBootResourcesAreSilent(t *testing.T) {
	wire, _ := captureCLIRecords(t)
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{CPUs: 4, Memory: "4GiB", Backend: "firecracker"}}
	vm := &nestedDeploymentVM{}
	if err := applySpaceBootResources(t.Context(), boot, vm); err != nil {
		t.Fatal(err)
	}
	if want := []vmapi.Resources{{CPUs: 4, Memory: "4GiB"}}; !reflect.DeepEqual(vm.applied, want) {
		t.Fatalf("applied resources = %+v, want %+v", vm.applied, want)
	}
	if records := readCLIRecords(t, wire); len(records) != 0 {
		t.Fatalf("unexpected Firecracker boot log: %+v", records)
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
	if err := writeSpaceShow(&output, &view); err != nil {
		t.Fatal(err)
	}
	if want := "Nested virtualization: off (saved); true (configured in instance file); not managed on this system (host eligibility)\n"; !strings.Contains(output.String(), want) {
		t.Fatalf("missing Lima status %q in:\n%s", want, &output)
	}
	output.Reset()
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

func TestFirecrackerNestedShowReportsKVMDependencies(t *testing.T) {
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: "firecracker"}}
	view := spaceShowData{Instance: vmapi.Instance{Name: "fixture"}}
	fillNestedSpaceShow(&view, boot)
	if view.NestedPreference != "auto" || view.NestedConfigured != "not managed" || view.NestedHostEligibility != "not assessed" {
		t.Fatalf("unexpected Firecracker nested virtualization status: %+v", view)
	}
	const reason = "Firecracker nested KVM depends on host KVM support and the guest kernel"
	var output bytes.Buffer
	if err := writeSpaceShow(&output, &view); err != nil {
		t.Fatal(err)
	}
	want := "Nested virtualization: auto (saved); not managed; not assessed (host eligibility)\n" +
		"Nested virtualization reason: " + reason + "\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("missing Firecracker status %q in:\n%s", want, &output)
	}
	output.Reset()
	if err := writeSpaceShowJSON(&output, &view); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"nested_virtualization_preference": "auto"`,
		`"nested_virtualization_configured": "not managed"`,
		`"nested_virtualization_host_eligibility": "not assessed"`,
		`"nested_virtualization_reason": "` + reason + `"`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing Firecracker JSON field %q in:\n%s", want, &output)
		}
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

func (v *nestedDeploymentVM) Start(context.Context, string) error { v.starts++; return v.startErr }

func TestRejectedVZFirstBootOffRemedyPreservesPendingState(t *testing.T) {
	for _, pendingImport := range []bool{false, true} {
		t.Run(fmt.Sprintf("import=%v", pendingImport), func(t *testing.T) {
			boot, applied := resourceSpaceFixture(t)
			boot.Initializing = true
			boot.DiskUUID = ""
			boot.VM.Backend = "lima"
			boot.VM.NestedVirtualization = "on"
			if pendingImport {
				boot.Import = &spaceconfig.ImportState{SHA256: strings.Repeat("ab", 32), Size: 100}
			}
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			oldHost, oldLookup := nestedHostFacts, lookupDeploymentVM
			t.Cleanup(func() { nestedHostFacts, lookupDeploymentVM = oldHost, oldLookup })
			nestedHostFacts = func() vmapi.NestedHostFacts {
				return vmapi.NestedHostFacts{OS: "darwin", Chip: "Apple M3 Max", Version: "26.7.1"}
			}
			lookupDeploymentVM = func(context.Context, string) (vmapi.Instance, bool, error) {
				return vmapi.Instance{VMType: "vz", Arch: "aarch64", Status: "Stopped"}, true, nil
			}
			failure := errors.New("Lima VZ rejected nested virtualization")
			vm := &nestedDeploymentVM{startErr: failure}
			if err := applySpaceBootResources(t.Context(), boot, vm); err != nil {
				t.Fatal(err)
			}
			err := startSpaceVM(t.Context(), boot, vm)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), "space set dev --nested-virtualization off") {
				t.Fatalf("missing runtime rejection remedy: %v", err)
			}
			if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", "off"); err != nil {
				t.Fatal(err)
			}
			after, err := spaceconfig.LoadBootstrap("dev")
			if err != nil {
				t.Fatal(err)
			}
			want := *boot
			want.VM.NestedVirtualization = "off"
			if !reflect.DeepEqual(&want, after) {
				t.Fatalf("changed pending state: want %+v got %+v", want, after)
			}
			if len(*applied) != 0 || vm.starts != 1 {
				t.Fatal("off remedy applied resources or restarted")
			}
			if err := applySpaceBootResources(t.Context(), after, vm); err != nil {
				t.Fatal(err)
			}
			if len(vm.applied) != 2 || vm.applied[1].NestedVirtualization == nil || *vm.applied[1].NestedVirtualization {
				t.Fatal("next explicit start did not resolve false")
			}
			vm.startErr = nil
			if err := startSpaceVM(t.Context(), after, vm); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPendingNestedOffRemedyDoesNotAdmitOtherMutations(t *testing.T) {
	for _, extra := range [][]string{{"--cpus", "6"}, {"--memory", "8GiB"}, {"--disk", "50GiB"}, {"--data-disk", "20GiB"}, {"--image-layer-compression", "gzip"}, {"--no-backup"}, {"--nested-virtualization", "auto"}} {
		t.Run(strings.Join(extra, "_"), func(t *testing.T) {
			boot, applied := resourceSpaceFixture(t)
			boot.Initializing = true
			boot.DiskUUID = ""
			boot.VM.NestedVirtualization = "on"
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"space", "set", "dev", "--nested-virtualization", "off"}, extra...)
			if err := resourceCommand(t, args...); err == nil {
				t.Fatal("admitted another mutation")
			}
			after, err := spaceconfig.LoadBootstrap("dev")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(boot, after) || len(*applied) != 0 {
				t.Fatal("changed pending state")
			}
		})
	}
	for _, status := range []string{"Running", "Unknown"} {
		t.Run(status, func(t *testing.T) {
			boot, _ := resourceSpaceFixture(t)
			boot.Initializing = true
			boot.DiskUUID = ""
			boot.VM.NestedVirtualization = "on"
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			lookupResourceSpace = func(context.Context, string) (vmapi.Instance, bool, error) {
				return vmapi.Instance{Status: status}, true, nil
			}
			if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", "off"); err == nil {
				t.Fatal("accepted unsafe VM state")
			}
		})
	}
}

func TestNestedCreateRejectsUnsupportedOnBeforeAllocation(t *testing.T) {
	for _, route := range []struct{ backend, os string }{{"lima", "linux"}, {"firecracker", "darwin"}, {"firecracker", "linux"}} {
		err := validateNestedCreateRequest("on", route.backend, vmapi.NestedHostFacts{OS: route.os})
		if err == nil || !strings.Contains(err.Error(), "create with --nested-virtualization off or auto") {
			t.Fatalf("%+v: %v", route, err)
		}
		for _, preference := range []string{"auto", "off", ""} {
			if err := validateNestedCreateRequest(preference, route.backend, vmapi.NestedHostFacts{OS: route.os}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := validateNestedCreateRequest("on", "lima", vmapi.NestedHostFacts{OS: "darwin"}); err != nil {
		t.Fatal("must defer actual-instance and VZ runtime checks", err)
	}
}

func TestNestedOffDoesNotBypassImageRecoveryGuard(t *testing.T) {
	boot, applied := resourceSpaceFixture(t)
	boot.VM.Backend = "firecracker"
	boot.VM.Image = filepath.Join(t.TempDir(), "old-image")
	boot.VM.NestedVirtualization = "on"
	boot.ImageUpdate = &spaceconfig.VMImageUpdate{Previous: boot.VM.Image, Target: filepath.Join(t.TempDir(), "new-image"), Bundle: boot.Bundle, Phase: "prepared"}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if err := resourceCommand(t, "space", "set", "dev", "--nested-virtualization", "off"); err == nil {
		t.Fatal("image recovery guard bypassed")
	}
	after, err := spaceconfig.LoadBootstrap("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(boot, after) || len(*applied) != 0 {
		t.Fatal("image checkpoint changed")
	}
}
