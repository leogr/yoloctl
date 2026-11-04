package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func imageUpdateFixture(t *testing.T) *spaceconfig.Bootstrap {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := &spaceconfig.Bootstrap{SchemaVersion: 3, Name: "fixture", DiskID: strings.Repeat("1", 32), DiskUUID: "retained-uuid", UID: 1000, GID: 1000, Bundle: "old-controller", Generation: "generation", VM: spaceconfig.VMSettings{Backend: "firecracker", Image: filepath.Join(t.TempDir(), "old"), CPUs: 2, Memory: "2GiB", Disk: "30GiB"}}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVMImageUpdateFailureRecovery(t *testing.T) {
	for _, failure := range []string{"prepare", "prepared-save", "delete", "selected-save", "start", "completed-save"} {
		t.Run(failure, func(t *testing.T) {
			boot := imageUpdateFixture(t)
			old := boot.VM.Image
			target := filepath.Join(t.TempDir(), "new")
			disk := filepath.Join(t.TempDir(), "native-data")
			if err := os.WriteFile(disk, []byte("accounts and declarations"), 0600); err != nil {
				t.Fatal(err)
			}
			removed, started := 0, 0
			injected := errors.New("interrupted")
			op := imageUpdateOps{
				resolve: func(context.Context, string, string) (string, error) {
					if failure == "prepare" {
						return "", injected
					}
					return target, nil
				},
				remove: func(context.Context, string, io.Writer) error {
					persisted, err := spaceconfig.LoadBootstrap(boot.Name)
					if err != nil || persisted.ImageUpdate == nil || persisted.ImageUpdate.Target != target || persisted.VM.Image != old {
						t.Fatalf("teardown preceded checkpoint: %+v %v", persisted, err)
					}
					removed++
					if failure == "delete" {
						return injected
					}
					return nil
				},
				start: func(_ context.Context, b *spaceconfig.Bootstrap, _ io.Writer) error {
					persisted, err := spaceconfig.LoadBootstrap(b.Name)
					if err != nil || persisted.ImageUpdate.Phase != "selected" || persisted.VM.Image != target {
						t.Fatalf("boot preceded selection: %+v %v", persisted, err)
					}
					started++
					if failure == "start" {
						return injected
					}
					b.Bundle = "new-controller"
					return b.Save()
				},
				save: func(b *spaceconfig.Bootstrap) error {
					phase := "completed"
					if b.ImageUpdate != nil {
						phase = b.ImageUpdate.Phase
					}
					if failure == phase+"-save" {
						return injected
					}
					return b.Save()
				},
			}
			if err := replaceSpaceImage(t.Context(), boot, "", false, "new-controller", io.Discard, op); !errors.Is(err, injected) {
				t.Fatalf("lost failure: %v", err)
			}
			persisted, err := spaceconfig.LoadBootstrap(boot.Name)
			if err != nil {
				t.Fatal(err)
			}
			if (failure == "prepare" || failure == "prepared-save") && (removed != 0 || started != 0 || persisted.ImageUpdate != nil || persisted.VM.Image != old) {
				t.Fatal("preparation failure affected old VM")
			}
			if failure == "delete" && (started != 0 || persisted.VM.Image != old) {
				t.Fatal("failed termination selected new image")
			}
			deletionsBeforeRetry := removed
			expectDeletion := persisted.ImageUpdate == nil || persisted.ImageUpdate.Phase == "prepared"
			failure = "" // retry with a freshly loaded durable checkpoint
			if err := replaceSpaceImage(t.Context(), persisted, "", false, "new-controller", io.Discard, op); err != nil {
				t.Fatal(err)
			}
			if (removed > deletionsBeforeRetry) != expectDeletion {
				t.Fatal("selected recovery deleted its new VM")
			}
			final, err := spaceconfig.LoadBootstrap(boot.Name)
			if err != nil || final.VM.Image != target || final.ImageUpdate != nil || final.Bundle != "new-controller" || final.DiskID != boot.DiskID || final.DiskUUID != boot.DiskUUID {
				t.Fatalf("recovery lost identity: %+v %v", final, err)
			}
			if data, err := os.ReadFile(disk); err != nil || string(data) != "accounts and declarations" {
				t.Fatal("native data changed", err)
			}
		})
	}
}

func TestVMImageUpdateRollbackVerifiesBeforeStopping(t *testing.T) {
	boot := imageUpdateFixture(t)
	old := boot.VM.Image
	target := filepath.Join(t.TempDir(), "new")
	boot.VM.Image = target
	boot.ImageUpdate = &spaceconfig.VMImageUpdate{Previous: old, Target: target, Bundle: "new", Phase: "selected"}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	removed := false
	compatible := false
	op := imageUpdateOps{
		resolve: func(_ context.Context, _, source string) (string, error) {
			if source != old {
				t.Fatalf("rollback chose %q", source)
			}
			if !compatible {
				return "", errors.New("unsupported protocol")
			}
			return old, nil
		},
		remove: func(context.Context, string, io.Writer) error { removed = true; return nil },
		start: func(_ context.Context, b *spaceconfig.Bootstrap, _ io.Writer) error {
			if b.VM.Image != old {
				t.Fatal("wrong rollback image")
			}
			return nil
		},
		save: (*spaceconfig.Bootstrap).Save,
	}
	if err := replaceSpaceImage(t.Context(), boot, "", true, "new", io.Discard, op); err == nil || removed {
		t.Fatal("incompatible rollback stopped VM")
	}
	compatible = true
	if err := replaceSpaceImage(t.Context(), boot, "", true, "new", io.Discard, op); err != nil {
		t.Fatal(err)
	}
	if !removed || boot.VM.Image != old || boot.ImageUpdate != nil {
		t.Fatal("rollback failed")
	}
}

func TestVMImageUpdateRequiresExplicitMatchingRecovery(t *testing.T) {
	boot := imageUpdateFixture(t)
	boot.ImageUpdate = &spaceconfig.VMImageUpdate{Previous: boot.VM.Image, Target: filepath.Join(t.TempDir(), "new"), Bundle: "owner", Phase: "prepared"}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := checkControllerSpace(t.Context(), boot.Name); err == nil || !strings.Contains(err.Error(), "pending VM image update") {
		t.Fatalf("ordinary command bypassed recovery: %v", err)
	}
	if err := startControllerSpace(t.Context(), boot, true, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "pending VM image update") {
		t.Fatalf("ordinary start bypassed recovery: %v", err)
	}
	for _, tc := range []struct{ bundle, source string }{{"other", ""}, {"owner", "/override"}} {
		// No dependency may be invoked before rejecting these requests.
		if err := replaceSpaceImage(t.Context(), boot, tc.source, false, tc.bundle, io.Discard, imageUpdateOps{}); err == nil {
			t.Fatal("accepted conflicting recovery")
		}
	}
}

func TestControllerDeploymentRequiresDrainOrProvenInactiveService(t *testing.T) {
	for _, tc := range []struct {
		name, state  string
		drain, probe error
		allow        bool
	}{
		{name: "drained", allow: true},
		{name: "not installed", state: "inactive\n", drain: io.EOF, allow: true},
		{name: "failed", state: "failed\n", drain: io.EOF, allow: true},
		{name: "active", state: "active", drain: io.EOF},
		{name: "restart backoff", state: "activating", drain: io.EOF},
		{name: "stopping", state: "deactivating", drain: io.EOF},
		{name: "unavailable", drain: io.EOF, probe: io.ErrUnexpectedEOF},
		{name: "ambiguous", state: "unknown", drain: io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostic bytes.Buffer
			probed := false
			err := drainControllerForDeployment(func(w io.Writer) error {
				_, _ = io.WriteString(w, "drain diagnostic")
				return tc.drain
			}, func() ([]byte, error) {
				probed = true
				return []byte(tc.state), tc.probe
			}, &diagnostic)
			if (err == nil) != tc.allow || probed != (tc.drain != nil) {
				t.Fatalf("deployment admission: %v, probed %v", err, probed)
			}
			if tc.allow && diagnostic.Len() != 0 {
				t.Fatal("successful recovery printed a drain error")
			}
		})
	}
}

func TestVMImageRecreateFlagsAreExclusive(t *testing.T) {
	cmd, _, err := newClientRootCmd().Find([]string{"space", "recreate"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"update-vm-image", "vm-image", "rollback-vm-image"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("missing %s", flag)
		}
	}
	if err := cmd.ParseFlags([]string{"--update-vm-image", "--vm-image", "/explicit"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.ValidateFlagGroups(); err == nil {
		t.Fatal("conflicting update flags accepted")
	}
}
