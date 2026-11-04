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

	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
)

func socketPathCLIHome(t *testing.T) string {
	t.Helper()
	stubOperatorClient(t, nil)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", 100))
	t.Setenv("YOLOCTL_HOME", root)
	// Any VM/disk subprocess is an effect, including an orphan-VM probe before
	// import. Record those calls without using or changing a live VM.
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("SOCKET_TEST_CALLS", calls)
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SOCKET_TEST_CALLS\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func requireSocketPathRefusal(t *testing.T, err error, calls string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "Lima socket path") {
		t.Fatalf("expected socket-path preflight, got %v", err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight reached VM/disk subprocess", err)
	}
}

func TestSpaceSocketPathRefusalPrecedesCreateAndImportEffects(t *testing.T) {
	for _, action := range []string{"create", "import"} {
		t.Run(action, func(t *testing.T) {
			calls := socketPathCLIHome(t)
			workdir := filepath.Join(t.TempDir(), "new-project")
			args := []string{"space", action, "target"}
			if action == "create" {
				args = append(args, "--account", "personal", "--workdir", workdir)
			} else {
				args = append(args, filepath.Join(t.TempDir(), "missing.tar.gz"))
			}
			args = append(args, "--backend", "lima")
			err := resourceCommand(t, args...)
			requireSocketPathRefusal(t, err, calls)
			spaces, err := layout.SpacesDir()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(spaces); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("published destination/seed before validation", err)
			}
			if _, err := os.Stat(workdir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("created project before validation", err)
			}
			home, err := layout.LimaHome()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(home, "_disks")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("allocated account disk before validation", err)
			}
		})
	}
}

func TestSpaceSocketPathRefusalPrecedesCompatibilityAndRecreate(t *testing.T) {
	for _, action := range []string{"start", "recreate"} {
		for _, guarded := range []bool{false, true} {
			label := action
			if guarded {
				label += "-installer"
			}
			t.Run(label, func(t *testing.T) {
				calls := socketPathCLIHome(t)
				boot := imageUpdateFixture(t)
				boot.VM.Backend, boot.VM.Image = "", "" // Legacy selection must still validate Lima.
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(mustBootstrapPath(t, boot.Name))
				if err != nil {
					t.Fatal(err)
				}
				oldCheck := checkCommandVMCompatibility
				t.Cleanup(func() { checkCommandVMCompatibility = oldCheck })
				checkCommandVMCompatibility = func(context.Context, *spaceconfig.Bootstrap) error {
					t.Error("compatibility offer reached before path validation")
					return &spaceVMCompatibilityError{Space: boot.Name, Backend: "lima"}
				}
				args := []string{"space", action, boot.Name}
				if action == "recreate" {
					args = append(args, "--force")
				}
				observed := false
				if guarded {
					unlock, err := spacelock.Acquire(t.Context(), boot.Name, true)
					if err != nil {
						t.Fatal(err)
					}
					unlock()
					expected, err := consentSnapshot(boot, "Stopped")
					if err != nil {
						t.Fatal(err)
					}
					oldObserve, oldPipe := observeInstallConsent, validateInstallResultPipe
					t.Cleanup(func() { observeInstallConsent, validateInstallResultPipe = oldObserve, oldPipe })
					observeInstallConsent = func(context.Context, string) (installmigration.Snapshot, error) {
						observed = true
						return expected, nil
					}
					validateInstallResultPipe = func(int) error { return nil }
					args = append(args, "--"+installPreconditionFlag, expected.Encode(), "--"+installReceiptFlag, "3")
				}
				requireSocketPathRefusal(t, resourceCommand(t, args...), calls)
				if observed != guarded {
					t.Fatal("path validation bypassed locked installer consent", observed, guarded)
				}
				after, err := os.ReadFile(mustBootstrapPath(t, boot.Name))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("refusal changed bootstrap", err)
				}
			})
		}
	}
}

func mustBootstrapPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := layout.SpaceBundleDir(name)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "space.yaml")
}

func TestSpaceSocketPathRefusalCoversDirectDeployment(t *testing.T) {
	calls := socketPathCLIHome(t)
	boot := imageUpdateFixture(t)
	boot.VM.Backend, boot.VM.Image = "lima", ""
	boot.Initializing, boot.DiskUUID = true, ""
	oldNew := newDeploymentVM
	t.Cleanup(func() { newDeploymentVM = oldNew })
	newDeploymentVM = func() controllerDeploymentVM { t.Fatal("deployment reached disk/VM access"); return nil }
	requireSocketPathRefusal(t, startControllerSpaceImpl(t.Context(), boot, true, nil, io.Discard), calls)
}

func TestSpaceSocketPathFittingStartReachesExistingCompatibilityCheck(t *testing.T) {
	stubOperatorClient(t, nil)
	shortYoloctlHome(t)
	boot := imageUpdateFixture(t)
	boot.VM.Backend, boot.VM.Image = "lima", ""
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("reached normal compatibility check")
	oldCheck := checkCommandVMCompatibility
	t.Cleanup(func() { checkCommandVMCompatibility = oldCheck })
	checkCommandVMCompatibility = func(context.Context, *spaceconfig.Bootstrap) error { return stop }
	if err := resourceCommand(t, "space", "start", boot.Name); !errors.Is(err, stop) {
		t.Fatal("fitting path changed start route", err)
	}
}

// Avoid Go test names lengthening the socket fixture past macOS's bound.
func shortYoloctlHome(t *testing.T) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "yc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("YOLOCTL_HOME", root)
}
