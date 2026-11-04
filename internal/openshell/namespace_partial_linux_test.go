package openshell

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

var partialPinSequence = []string{"preflight", "stop", "check-1", "persist:partial-private-attempt", "check-2", "private", "verify-private", "persist:partial-private", "check-3", "persist:partial-unmount-attempt", "check-4", "unmount", "verify-bare", "persist:partial-unmounted", "check-5"}

func partialPinScript(fail string, events *[]string, cause error) partialPinHooks {
	step := func(name string) error {
		*events = append(*events, name)
		if name == fail {
			return cause
		}
		return nil
	}
	checks := 0
	return partialPinHooks{
		beforeStop: func(context.Context) error { return step("preflight") }, stop: func(context.Context) error { return step("stop") },
		check: func(context.Context, partialPinProgress) error {
			checks++
			return step(fmt.Sprintf("check-%d", checks))
		},
		persist:     func(phase namespacePinPhase) error { return step("persist:" + string(phase)) },
		makePrivate: func() error { return step("private") }, verifyPrivate: func(context.Context) error { return step("verify-private") },
		unmount: func() error { return step("unmount") }, verifyBare: func(context.Context) error { return step("verify-bare") },
	}
}

func TestNamespacePartialRetirementStopsAtEveryLostACKWithoutReplay(t *testing.T) {
	for index, where := range partialPinSequence {
		t.Run(where, func(t *testing.T) {
			var events []string
			cause := errors.New("lost reply at " + where)
			p, err := runPartialPinRetirement(context.Background(), true, true, partialPinScript(where, &events, cause))
			var uncertain *NativeEffectsUncertain
			if !errors.Is(err, cause) || errors.As(err, &uncertain) != (index != 0) || p.ready || !reflect.DeepEqual(events, partialPinSequence[:index+1]) {
				t.Fatal("failed partial cleanup continued/completed", p, err, events)
			}
			if p.stopAttempted != (index >= 1) || p.stopAcknowledged != (index > 1) || p.privateAttempted != (index >= 5) || p.privateAcknowledged != (index > 5) || p.unmountAttempted != (index >= 11) || p.unmountAcknowledged != (index > 11) {
				t.Fatal("attempt manufactured positive ACK", p, where)
			}
		})
	}
	var events []string
	if p, err := runPartialPinRetirement(context.Background(), true, true, partialPinScript("", &events, nil)); err != nil || !p.ready || !reflect.DeepEqual(events, partialPinSequence) {
		t.Fatal(p, err, events)
	}
}

func TestNamespacePartialRetirementBareAndAlreadyPrivateDoNotInventEffects(t *testing.T) {
	for _, bound := range []bool{false, true} {
		var events []string
		p, err := runPartialPinRetirement(context.Background(), bound, false, partialPinScript("", &events, nil))
		want := []string{"preflight", "stop", "check-1"}
		if bound {
			want = append(want, "persist:partial-unmount-attempt", "check-2", "unmount", "verify-bare", "persist:partial-unmounted", "check-3")
		}
		if err != nil || !p.ready || !p.stopAcknowledged || p.privateAttempted || p.privateAcknowledged || p.unmountAcknowledged != bound || !reflect.DeepEqual(events, want) {
			t.Fatal("bare/private cleanup replayed creation or skipped Stop", p, err, events)
		}
	}
	var events []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := runPartialPinRetirement(ctx, true, true, partialPinScript("", &events, nil)); !errors.Is(err, context.Canceled) || p.stopAttempted || len(events) != 0 {
		t.Fatal(p, err, events)
	}
	if _, err := runPartialPinRetirement(context.Background(), false, true, partialPinScript("", &events, nil)); err == nil {
		t.Fatal("private cleanup requested without a directory bind")
	}
	if _, err := runPartialPinRetirement(context.Background(), true, true, partialPinHooks{}); err == nil {
		t.Fatal("missing callbacks accepted")
	}
}

func TestNamespacePartialSelectionRequiresActualUnconsumedCreatorAttempts(t *testing.T) {
	_, _, _, r, _, _ := namespacePinReservationFixture(t)
	if selected, err := selectKnownUnboundPin(r, nil, nil); err != nil || selected.directoryMount != 0 || selected.reservation != r {
		t.Fatal("original exclusive bare reservation rejected", selected, err)
	}
	r.consumed = true
	if _, err := selectKnownUnboundPin(r, nil, nil); err == nil {
		t.Fatal("cold consumed metadata minted no-attempt proof")
	}
	// Scripted creator progress; no real bind/private effect is claimed.
	d := &nativePinDirectoryAttempt{reservation: r, failure: errors.New("known check failure")}
	if _, err := selectKnownUnboundPin(r, d, nil); err != nil {
		t.Fatal("known before-bind attempt rejected", err)
	}
	d.progress = pinDirectoryProgress{bindAttempted: true, bindAcknowledged: true, directoryMount: 20, boundRecorded: true}
	if selected, err := selectKnownUnboundPin(r, d, nil); err != nil || selected.directoryMount != 20 || selected.privateAcknowledged {
		t.Fatal(selected, err)
	}
	d.progress.privateAttempted, d.progress.privateAcknowledged, d.progress.ready = true, true, true
	d.namespaceConsumed = true
	if _, err := selectKnownUnboundPin(r, d, nil); err == nil {
		t.Fatal("missing consumed namespace attempt accepted")
	}
	n := &nativePinAttempt{directory: d, failure: errors.New("known failure before bind")}
	if _, err := selectKnownUnboundPin(r, d, n); err != nil {
		t.Fatal("actual pre-bind namespace attempt rejected", err)
	}
	for _, change := range []string{"directory-lost-reply", "namespace-lost-reply", "bind-without-ack", "private-without-ack", "namespace-attempted", "namespace-acked", "policy-attempted", "proof", "closed", "foreign-attempt", "already-cleaned"} {
		copyR, copyD, copyN := *r, *d, *n
		copyD.reservation = &copyR
		copyN.directory = &copyD
		switch change {
		case "directory-lost-reply":
			copyD.failure = &NativeEffectsUncertain{Cause: errors.New("unknown bind")}
		case "namespace-lost-reply":
			copyN.failure = &NativeEffectsUncertain{Cause: errors.New("unknown namespace effect")}
		case "bind-without-ack":
			copyD.progress.bindAcknowledged = false
		case "private-without-ack":
			copyD.progress.privateAcknowledged = false
		case "namespace-attempted":
			copyN.progress.bindAttempted = true
		case "namespace-acked":
			copyN.progress.bindAcknowledged = true
		case "policy-attempted":
			copyN.progress.policyAttempted = true
		case "proof":
			copyN.proof.Schema = BootstrapSchema
		case "closed":
			copyD.closed = true
		case "foreign-attempt":
			copyN.directory = d
		case "already-cleaned":
			copyR.cleanupAttempted = true
		}
		if _, err := selectKnownUnboundPin(&copyR, &copyD, &copyN); err == nil {
			t.Fatal("unsafe partial ownership accepted", change)
		}
	}
	stops := 0
	owner := nativePinStopHooks{owner: func(context.Context) error { return nil }, stop: func(context.Context) error { stops++; return nil }, inactive: func(context.Context) error { return nil }}
	if got, err := retireKnownUnboundNativePin(context.Background(), nil, r, d, n, owner); err == nil || got != nil || stops != 0 {
		t.Fatal("missing actual kernel reached Stop", got, err, stops)
	}
	if err := (*nativeUnboundPinRetirement)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamespacePartialCleanupPoisonsBothCreationEntriesBeforeBackendAccess(t *testing.T) {
	_, _, _, r, _, _ := namespacePinReservationFixture(t)
	r.cleanupAttempted = true
	// Bare reservation consumed is still false. Backend/prerequisites are
	// deliberately absent: cleanup uncertainty must win before those checks,
	// rather than becoming an ordinary retryable creation error.
	d, err := createNativePinDirectory(context.Background(), nil, r, nil)
	var uncertain *NativeEffectsUncertain
	if d != nil || !errors.As(err, &uncertain) {
		t.Fatal("consumed cleanup lost typed refusal at directory creator", d, err)
	}
	r.consumed = true
	d = &nativePinDirectoryAttempt{reservation: r, progress: pinDirectoryProgress{bindAttempted: true, bindAcknowledged: true, directoryMount: 20, boundRecorded: true, privateAttempted: true, privateAcknowledged: true, ready: true}}
	n, err := createNativeNamespacePin(context.Background(), nil, d, nil, nil)
	uncertain = nil
	if n != nil || !errors.As(err, &uncertain) {
		t.Fatal("consumed cleanup lost typed refusal at namespace creator", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := createNativePinDirectory(ctx, nil, r, nil); !errors.As(err, &uncertain) {
		t.Fatal("cancellation hid prior cleanup uncertainty", err)
	}
}
