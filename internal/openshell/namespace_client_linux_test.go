package openshell

import (
	"context"
	"testing"
)

func TestNamespacePublicStopRefusesWithoutAdmittedNativeIdentityBeforeCallbacks(t *testing.T) {
	pin, record := namespacePinRecordFixture(t)
	calls := 0
	callback := func(context.Context) error { calls++; return nil }
	owner := NativeNamespaceStop{callback, callback, callback}
	// Only denial is tested in the ordinary project environment. Actual native
	// identity/kernel/mount/Stop/retirement require the isolated G6 lane.
	f := newMaterialFixture(t)
	if err := StopNativeNamespace(t.Context(), record, f.release, pin.Scope.ContainerID, owner); err == nil || calls != 0 {
		t.Fatal("unadmitted namespace Stop reached owner/engine callbacks", err, calls)
	}
	if err := StopNativeNamespace(t.Context(), RuntimeRecord{}, QualifiedRelease{}, "", NativeNamespaceStop{}); err == nil {
		t.Fatal("missing namespace Stop contract accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := StopNativeNamespace(ctx, record, f.release, pin.Scope.ContainerID, owner); err == nil || calls != 0 {
		t.Fatal("canceled namespace Stop reached callbacks", err, calls)
	}
}
