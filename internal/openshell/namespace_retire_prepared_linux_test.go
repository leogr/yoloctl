package openshell

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Only project protocol fixtures: production construction additionally requires
// actual native kernel/owner/NSFS qualification. No syscall probe or root effect.
func preparedRetirementFixture(record RuntimeRecord, cid string, hooks pinRetirementHooks, completed bool) *nativePreparedPinRetirement {
	return &nativePreparedPinRetirement{record: cloneRuntimeRecord(record), cid: cid, hooks: hooks, completed: completed, ready: true, release: func() error { return nil }}
}

func TestPreparedNamespaceRetirementSharesOneOriginalContainerStopAcrossGenerations(t *testing.T) {
	_, _, _, scope := containerRefreshFixture(t)
	stopCalls := 0
	a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.stop = func(context.Context) error { stopCalls++; return nil }
	var lastACK *nativeContainerStopACK
	a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
		lastACK = ack
		for _, record := range scope.records {
			var events []string
			hooks := namespaceRetireScript("", &events, nil)
			// Neither an actual second Stop nor a no-op Stop is part of this path.
			hooks.stop = func(context.Context) error { stopCalls++; return errors.New("unexpected second Stop") }
			parent, cancel := context.WithTimeout(ctx, time.Second)
			deadline, _ := parent.Deadline()
			check := hooks.check
			hooks.check = func(c context.Context, progress pinRetirementProgress) error {
				current, ok := c.Deadline()
				if !ok || current.After(deadline) {
					return errors.New("per-generation cleanup extended parent deadline")
				}
				return check(c, progress)
			}
			p := preparedRetirementFixture(record, scope.cid, hooks, false)
			progress, err := p.afterContainerStop(parent, ack)
			cancel()
			if err != nil || !progress.completed || !reflect.DeepEqual(events, namespaceRetireSequence[2:]) {
				t.Fatal("shared Stop ACK did not run exactly the existing effect suffix", progress, err, events)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 || *removes != 1 {
		t.Fatal("multi-generation retirement replayed engine effects", stopCalls, *removes)
	}
	var events []string
	p := preparedRetirementFixture(scope.records[0], scope.cid, namespaceRetireScript("", &events, nil), false)
	defer func() { _ = p.Close() }()
	if _, err := p.afterContainerStop(t.Context(), lastACK); err == nil || len(events) != 0 {
		t.Fatal("expired original ACK remained retirement authority", err, events)
	}
}

func TestPreparedNamespaceRetirementRejectsUnscopedACKBeforeEffects(t *testing.T) {
	for _, change := range []string{"nil", "empty", "not-inactive", "foreign-record", "foreign-CID"} {
		t.Run(change, func(t *testing.T) {
			_, _, _, scope := containerRefreshFixture(t)
			var events []string
			p := preparedRetirementFixture(scope.records[0], scope.cid, namespaceRetireScript("", &events, nil), false)
			defer func() { _ = p.Close() }()
			a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
				switch change {
				case "nil":
					ack = nil
				case "empty":
					ack = &nativeContainerStopACK{}
				case "not-inactive":
					copied := *ack.attempt
					copied.inactiveVerified = false
					ack = &nativeContainerStopACK{&copied}
				case "foreign-record":
					p.record.Generation = "g" + strings.Repeat("e", 48)
				case "foreign-CID":
					p.cid = strings.Repeat("b", 64)
				}
				_, err := p.afterContainerStop(ctx, ack)
				return err
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			var uncertain *NativeEffectsUncertain
			if err == nil || errors.As(err, &uncertain) || len(events) != 0 || p.consumed || *removes != 0 { // plain step error before rm (decision #3)
				t.Fatal("unscoped Stop ACK reached retirement/removal effects", change, err, events, p.consumed, *removes)
			}
		})
	}
}

func TestPreparedNamespaceRetirementFailureStopsSuffixAndPoisonsRetry(t *testing.T) {
	for index, where := range namespaceRetireSequence[2:] {
		t.Run(where, func(t *testing.T) {
			_, _, _, scope := containerRefreshFixture(t)
			cause := errors.New("lost original retirement reply at " + where)
			var events []string
			p := preparedRetirementFixture(scope.records[0], scope.cid, namespaceRetireScript(where, &events, cause), false)
			defer func() { _ = p.Close() }()
			a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
				_, err := p.afterContainerStop(ctx, ack)
				before := len(events)
				_, retry := p.afterContainerStop(ctx, ack)
				var uncertain *NativeEffectsUncertain
				if !errors.As(retry, &uncertain) || len(events) != before {
					t.Fatal("failed original retirement was retried", retry, events)
				}
				return err
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			var uncertain *NativeEffectsUncertain
			if !errors.Is(err, cause) || !errors.As(err, &uncertain) || !p.consumed || *removes != 0 || !reflect.DeepEqual(events, namespaceRetireSequence[2:index+3]) {
				t.Fatal("failed shared retirement continued or manufactured completion", where, err, events, *removes)
			}
		})
	}
}

func TestPreparedNamespaceRetirementCompletedAndCancelledStillRequireLiveACK(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "cancelled"}[cancelled], func(t *testing.T) {
			_, _, _, scope := containerRefreshFixture(t)
			var events []string
			p := preparedRetirementFixture(scope.records[0], scope.cid, namespaceRetireScript("", &events, nil), true)
			defer func() { _ = p.Close() }()
			a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
				child, cancel := context.WithCancel(ctx)
				defer cancel()
				if cancelled {
					cancel()
				}
				progress, err := p.afterContainerStop(child, ack)
				if !cancelled && !progress.completed {
					t.Fatal("completed marker did not reach bounded inactive verification", progress, err)
				}
				return err
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			if cancelled {
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &uncertain) || !errors.Is(err, context.Canceled) || len(events) != 0 || *removes != 0 {
					t.Fatal("cancelled parent bound was lifted before retirement", err, events, *removes)
				}
			} else if err != nil || *removes != 1 || !reflect.DeepEqual(events, []string{"check-1"}) {
				t.Fatal("completed generation repeated pin/table effects or skipped inactivity", err, events, *removes)
			}
		})
	}
}

func TestPreparedNamespaceRetirementClosedOrUnqualifiedCannotRun(t *testing.T) {
	if p, err := prepareNativePinRetirement(t.Context(), nil, nil, nativePinStopHooks{}); err == nil || p != nil {
		t.Fatal("missing native qualification prepared effects", p, err)
	}
	var p *nativePreparedPinRetirement
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil {
		t.Fatal("nil prepared handle accepted")
	}
	var events []string
	hooks := namespaceRetireScript("", &events, nil)
	p = &nativePreparedPinRetirement{ready: true, hooks: hooks}
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil {
		t.Fatal("missing retained native handle accepted")
	}
	closed := 0
	p.release = func() error { closed++; return nil }
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil || closed != 1 || len(events) != 0 {
		t.Fatal("closed prepared scope retained effects", err, closed, events)
	}
	for _, progress := range []pinRetirementProgress{{}, {stopAttempted: true}, {stopAttempted: true, stopAcknowledged: true, policyAcknowledged: true}} {
		if _, err := continueAcknowledgedPinRetirement(t.Context(), false, hooks, progress); err == nil || len(events) != 0 {
			t.Fatal("effect suffix accepted missing/reused initial ACK stage", progress, err, events)
		}
	}
}
