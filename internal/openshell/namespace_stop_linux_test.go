package openshell

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNamespaceStaleStopAcknowledgesOnlyCurrentStopWithoutOldEffectReplay(t *testing.T) {
	want := []string{"preflight", "stop", "inactive-and-stale"}
	for _, fail := range []string{"", "preflight", "stop", "inactive-and-stale"} {
		var events []string
		cause := errors.New("no such container: lost reply at " + fail)
		step := func(name string) error {
			events = append(events, name)
			if name == fail {
				return cause
			}
			return nil
		}
		h := stalePinStopHooks{
			beforeStop:             func(context.Context) error { return step("preflight") },
			stop:                   func(context.Context) error { return step("stop") },
			verifyInactiveAndStale: func(context.Context) error { return step("inactive-and-stale") },
		}
		p, err := runStalePinStop(context.Background(), h)
		end := 3
		if fail == "preflight" {
			end = 1
		}
		if fail == "stop" {
			end = 2
		}
		var uncertain *NativeEffectsUncertain
		if !reflect.DeepEqual(events, want[:end]) || p.completed != (fail == "") || (err == nil) != (fail == "") || p.stopAttempted != (fail != "preflight") || p.stopAcknowledged != (fail == "" || fail == "inactive-and-stale") || errors.As(err, &uncertain) != (fail == "stop" || fail == "inactive-and-stale") {
			t.Fatal("failed Stop was substituted by observation/replayed/completed", fail, p, err, events)
		}
		if p.policyAttempted || p.policyAcknowledged || p.pinUnmountAttempted || p.pinUnmountAcknowledged || p.directoryUnmountAttempted || p.directoryUnmountAcknowledged {
			t.Fatal("different boot manufactured historical policy/unmount ACKs", p)
		}
	}
}

func TestNamespaceStaleStopFreshInvocationAndBoundedCleanupAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stops, checks := 0, 0
	h := stalePinStopHooks{
		beforeStop: func(context.Context) error { return nil },
		stop:       func(context.Context) error { stops++; cancel(); return nil },
		verifyInactiveAndStale: func(c context.Context) error {
			checks++
			deadline, ok := c.Deadline()
			if c.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
				return errors.New("cleanup lost independent bounded context")
			}
			return nil
		},
	}
	if p, err := runStalePinStop(ctx, h); err != nil || !p.completed {
		t.Fatal(p, err)
	}
	if p, err := runStalePinStop(context.Background(), h); err != nil || !p.completed || stops != 2 || checks != 2 {
		t.Fatal("repeated Stop skipped a new actual ACK", p, err, stops, checks)
	}
	if p, err := runStalePinStop(ctx, h); !errors.Is(err, context.Canceled) || p.stopAttempted || stops != 2 {
		t.Fatal("canceled invocation mutated", p, err, stops)
	}
	if _, err := runStalePinStop(context.Background(), stalePinStopHooks{}); err == nil {
		t.Fatal("missing callbacks accepted")
	}
}

func TestNamespaceStaleStopRequiresExactBracketingMetadataObservation(t *testing.T) {
	_, _, lock, a, pin, record := namespacePinFilesFixture(t)
	b, err := lock.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	initial := &staleNamespacePinObservation{a, stalePinTestBoot, pinFileIdentity{a.identity.device, pin.Placeholder.Inode, a.identity.mount}}
	current := &staleNamespacePinObservation{b, stalePinTestBoot, initial.placeholder}
	if !sameStalePinObservation(initial, current) {
		t.Fatal("identical independent ordinary-file observations rejected")
	}
	for _, change := range []string{"nil", "closed", "boot", "placeholder", "directory", "lock", "incarnation", "cid", "record", "origin", "state"} {
		files := *b
		changed := *current
		changed.files = &files
		switch change {
		case "nil":
			changed.files = nil
		case "closed":
			files.directory = nil
		case "boot":
			changed.currentBoot = "33333333-4444-4555-8666-777777777777"
		case "placeholder":
			changed.placeholder.inode++
		case "directory":
			files.identity.inode++
		case "lock":
			copy := *lock
			copy.identity.inode++
			files.lock = &copy
		case "incarnation":
			copy := *lock
			copy.incarnation = strings.Repeat("b", 32)
			files.lock = &copy
		case "cid":
			files.cid = strings.Repeat("b", 64)
		case "record":
			files.record.Launch.UID++
		case "origin":
			files.origin = append(append([]byte(nil), files.origin...), ' ')
		case "state":
			files.current = append(append([]byte(nil), files.current...), ' ')
		}
		if sameStalePinObservation(initial, &changed) {
			t.Fatal("changed observation accepted after Stop", change)
		}
	}
	if sameStalePinObservation(nil, current) || sameStalePinObservation(initial, nil) {
		t.Fatal("absent observation accepted")
	}
}

func TestNamespaceOwnedStopActualEntryRequiresKernelBeforeAnyEngineEffect(t *testing.T) {
	stops := 0
	owner := nativePinStopHooks{owner: func(context.Context) error { return nil }, stop: func(context.Context) error { stops++; return nil }, inactive: func(context.Context) error { return nil }}
	if p, err := stopOwnedNativeNamespace(context.Background(), nil, nil, RuntimeRecord{}, "", owner); err == nil || p.stopAttempted || stops != 0 {
		t.Fatal("missing infrastructure reached engine", p, err, stops)
	}
	if p, err := stopStaleNativeNamespacePin(context.Background(), nil, nil, RuntimeRecord{}, "", owner); err == nil || p.stopAttempted || stops != 0 {
		t.Fatal("missing actual stale observation reached engine", p, err, stops)
	}
	if _, err := stopStaleNativeNamespacePin(context.Background(), nil, nil, RuntimeRecord{}, "", nativePinStopHooks{}); err == nil {
		t.Fatal("missing owner accepted")
	}
}
