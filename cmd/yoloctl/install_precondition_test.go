package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func TestInstallPreconditionMismatchesAreLockedAndHaveNoEffects(t *testing.T) {
	for _, action := range []string{"start", "recreate"} {
		for _, change := range []string{"bundle", "state", "identity", "generation", "bootstrap", "unavailable"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				stubOperatorClient(t, nil)
				t.Setenv("YOLOCTL_HOME", t.TempDir())
				unlock, err := spacelock.Acquire(t.Context(), "fixture", true)
				if err != nil {
					t.Fatal(err)
				}
				unlock()
				expected := installmigration.Snapshot{Space: "fixture", Bundle: "old", State: "Running", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("b", 64)}
				current := expected
				switch change {
				case "bundle":
					current.Bundle = "other"
				case "state":
					current.State = "Stopped"
				case "identity":
					current.DiskID = strings.Repeat("c", 32)
				case "generation":
					current.Generation = "other"
				case "bootstrap":
					current.Bootstrap = strings.Repeat("c", 64)
				}
				oldObserve, oldPipe := observeInstallConsent, validateInstallResultPipe
				t.Cleanup(func() { observeInstallConsent, validateInstallResultPipe = oldObserve, oldPipe })
				held := false
				observed := false
				observeInstallConsent = func(context.Context, string) (installmigration.Snapshot, error) {
					if !held {
						t.Fatal("precondition observation outside lifecycle lock")
					}
					observed = true
					if change == "unavailable" {
						return installmigration.Snapshot{}, errors.New("unavailable")
					}
					return current, nil
				}
				validateInstallResultPipe = func(int) error { return nil }
				root := newClientRootCmd()
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs([]string{"space", action, "fixture", "--" + installPreconditionFlag, expected.Encode(), "--" + installReceiptFlag, "3"})
				ctx := spacelock.WithObserver(t.Context(), func(_ context.Context, e spacelock.Event) { held = e.Held })
				err = root.ExecuteContext(ctx)
				if classifyError(err) != installmigration.SkipStatus || !observed || held {
					t.Fatal(err, observed, held)
				}
				spaces, err := layout.SpacesDir()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(spaces); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("mismatch created bootstrap storage")
				}
			})
		}
	}
}

type installDeploymentFake struct {
	controllerDeploymentVM
	creates, starts int
	stop            error
}

func (*installDeploymentFake) RequireAccountDisk(context.Context, string, string) error { return nil }
func (f *installDeploymentFake) CreateWithSpec(context.Context, string, vmhost.Spec) error {
	f.creates++
	return nil
}
func (*installDeploymentFake) SetResources(context.Context, string, vmapi.Resources) error {
	return nil
}
func (*installDeploymentFake) SetInstanceMounts(string, []vmhost.Mount) error { return nil }
func (*installDeploymentFake) SetAccountDisk(string, string, bool) error      { return nil }
func (f *installDeploymentFake) Start(context.Context, string) error          { f.starts++; return f.stop }

func TestInstallBootForbiddenReachesDeploymentObservation(t *testing.T) {
	for _, scenario := range []string{"disappeared", "stopped", "approved-stopped-start", "approved-recreate", "ordinary-start"} {
		t.Run(scenario, func(t *testing.T) {
			shortYoloctlHome(t)
			boot := imageUpdateFixture(t)
			// This deployment fake tests consent, independent of which
			// platform adapters are linked into the test binary.
			boot.VM.Backend, boot.VM.Image = "lima", ""
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			boot.ImageUpdate = nil
			fake := &installDeploymentFake{stop: errors.New("stop after approved boot")}
			oldNew, oldLookup := newDeploymentVM, lookupDeploymentVM
			t.Cleanup(func() { newDeploymentVM, lookupDeploymentVM = oldNew, oldLookup })
			newDeploymentVM = func() controllerDeploymentVM { return fake }
			lookupDeploymentVM = func(context.Context, string) (vmhost.Instance, bool, error) {
				return vmhost.Instance{Status: "Stopped"}, scenario == "stopped" || scenario == "approved-stopped-start", nil
			}
			ctx := t.Context()
			forbidden := scenario == "disappeared" || scenario == "stopped"
			if forbidden {
				ctx = context.WithValue(ctx, installConsentKey{}, true)
			}
			err := startControllerSpaceImpl(ctx, boot, true, nil, io.Discard)
			if forbidden {
				if classifyError(err) != installmigration.SkipStatus || fake.creates != 0 || fake.starts != 0 {
					t.Fatal(err, fake)
				}
			} else if !errors.Is(err, fake.stop) || fake.starts != 1 {
				t.Fatal(err, fake)
			}
			saved, loadErr := spaceconfig.LoadBootstrap(boot.Name)
			if loadErr != nil || saved.Bundle != boot.Bundle {
				t.Fatal("deployment changed bootstrap before verified completion")
			}
		})
	}
}

// archyFable F1: an interrupted guarded run returns the cancellation, never
// the "state changed, skipped" status (76) that would let the installer go on.
func TestInstallPreconditionInterruptionIsNotASkip(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	unlock, err := spacelock.Acquire(t.Context(), "fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	expected := installmigration.Snapshot{Space: "fixture", Bundle: "old", State: "Running", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("b", 64)}
	oldObserve, oldPipe := observeInstallConsent, validateInstallResultPipe
	t.Cleanup(func() { observeInstallConsent, validateInstallResultPipe = oldObserve, oldPipe })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observeInstallConsent = func(context.Context, string) (installmigration.Snapshot, error) {
		cancel() // Ctrl-C arrives while the consent is being observed
		return installmigration.Snapshot{}, context.Canceled
	}
	validateInstallResultPipe = func(int) error { return nil }
	root := newClientRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"space", "start", "fixture", "--" + installPreconditionFlag, expected.Encode(), "--" + installReceiptFlag, "3"})
	err = root.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) || classifyError(err) == installmigration.SkipStatus {
		t.Fatalf("interruption reported as %v (status %d)", err, classifyError(err))
	}
}
