package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestInstallerConsentTravelsFromLockedGuardToDeployment(t *testing.T) {
	shortYoloctlHome(t)
	boot, _ := resourceSpaceFixture(t)
	expected, err := consentSnapshot(boot, "Running")
	if err != nil {
		t.Fatal(err)
	}
	oldObserve, oldPipe := observeInstallConsent, validateInstallResultPipe
	oldNew, oldLookup := newDeploymentVM, lookupDeploymentVM
	t.Cleanup(func() {
		observeInstallConsent, validateInstallResultPipe = oldObserve, oldPipe
		newDeploymentVM, lookupDeploymentVM = oldNew, oldLookup
	})
	held := false
	ctx := spacelock.WithObserver(t.Context(), func(_ context.Context, e spacelock.Event) { held = e.Held })
	release, err := spacelock.Acquire(ctx, boot.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	observeInstallConsent = func(context.Context, string) (installmigration.Snapshot, error) {
		if !held {
			t.Fatal("consent observed outside lock")
		}
		return expected, nil
	}
	validateInstallResultPipe = func(int) error { return nil }
	cmd, _, err := newClientRootCmd().Find([]string{"space", "start"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.SetContext(ctx)
	if err := cmd.Flags().Set(installPreconditionFlag, expected.Encode()); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set(installReceiptFlag, "3"); err != nil {
		t.Fatal(err)
	}
	if err := checkInstallPrecondition(cmd, boot.Name, "start"); err != nil {
		t.Fatal(err)
	}
	if !installBootForbidden(cmd.Context()) {
		t.Fatal("locked running consent did not forbid boot")
	}
	fake := &installDeploymentFake{stop: errors.New("stop if boot is reached unexpectedly")}
	newDeploymentVM = func() controllerDeploymentVM { return fake }
	lookupDeploymentVM = func(context.Context, string) (vmhost.Instance, bool, error) { return vmhost.Instance{}, false, nil }
	err = startControllerSpaceImpl(cmd.Context(), boot, true, nil, io.Discard)
	if classifyError(err) != installmigration.SkipStatus || fake.creates != 0 || fake.starts != 0 {
		t.Fatal("VM disappeared after consent but reached boot", err, fake)
	}
}
