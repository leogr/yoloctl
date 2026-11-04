package openshell

import (
	"context"
	"errors"
	"testing"
)

func TestNativeActivationGenerationStopFallbackIsOnlyUntouchedNamespaceState(t *testing.T) {
	calls := 0
	callback := func(context.Context) error { calls++; return nil }
	owner := NativeNamespaceStop{callback, callback, callback}
	for _, activation := range []*NativeActivation{nil, {}, {failure: errors.New("known pre-reservation read failure")}} {
		if handled, err := activation.StopStartedGeneration(t.Context(), owner); handled || err != nil || calls != 0 {
			t.Fatal("untouched generation acquired cleanup authority", handled, err, calls)
		}
	}
	cause := errors.New("lost mutation reply")
	for _, activation := range []*NativeActivation{{closed: true}, {cleanupAttempted: true}, {lock: &namespacePinLock{}}, {failure: &NativeEffectsUncertain{Cause: cause}}} {
		handled, err := activation.StopStartedGeneration(t.Context(), owner)
		var uncertain *NativeEffectsUncertain
		if !handled || !errors.As(err, &uncertain) || calls != 0 {
			t.Fatal("generation cleanup uncertainty permitted fallback", handled, err, calls)
		}
		if activation.failure != nil && !errors.Is(err, cause) {
			t.Fatal("original uncertainty lost", err)
		}
	}
	_, _, _, reservation, _, _ := namespacePinReservationFixture(t)
	activation := &NativeActivation{reservation: reservation}
	if handled, err := activation.StopStartedGeneration(t.Context(), NativeNamespaceStop{}); !handled || err == nil {
		t.Fatal("reserved generation accepted missing callbacks", handled, err)
	}
	if handled, err := activation.StopStartedGeneration(t.Context(), owner); !handled || err == nil || calls != 0 {
		t.Fatal("unadmitted generation reached actual callbacks", handled, err, calls)
	}
}
