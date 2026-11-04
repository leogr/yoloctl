package openshell

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Scripted original-call effects only. Actual native constructors below remain
// subject to root/kernel/storage qualification in the separate native lane.
func TestObservedNamespaceRetirementUsesOnlyOriginalACKAndCurrentVerification(t *testing.T) {
	_, _, _, scope := containerRefreshFixture(t)
	stops, verifies := 0, 0
	a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.stop = func(context.Context) error { stops++; return nil }
	var lastACK *nativeContainerStopACK
	a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
		lastACK = ack
		for _, record := range scope.records {
			parent, cancel := context.WithTimeout(ctx, time.Second)
			deadline, _ := parent.Deadline()
			p := &nativePreparedObservationRetirement{record: record, cid: scope.cid, ready: true, verify: func(c context.Context) error {
				current, ok := c.Deadline()
				if !ok || current.After(deadline) {
					return errors.New("observation verification lifted parent bound")
				}
				verifies++
				return nil
			}}
			progress, err := p.afterContainerStop(parent, ack)
			cancel()
			if err != nil || progress != (pinRetirementProgress{stopAttempted: true, stopAcknowledged: true, completed: true}) {
				t.Fatal("cold observation manufactured kernel effects or lost original ACK", progress, err)
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
	if stops != 1 || verifies != len(scope.records) || *removes != 1 {
		t.Fatal("observation retirement repeated Stop or skipped current proof", stops, verifies, *removes)
	}
	p := &nativePreparedObservationRetirement{record: scope.records[0], cid: scope.cid, ready: true, verify: func(context.Context) error { verifies++; return nil }}
	defer func() { _ = p.Close() }()
	if _, err := p.afterContainerStop(t.Context(), lastACK); err == nil || verifies != len(scope.records) {
		t.Fatal("expired Stop token reached cold observation verifier", err, verifies)
	}
}

func TestObservedNamespaceRetirementFailedProofStopsRemovalAndRetry(t *testing.T) {
	for _, change := range []string{"verification", "cancelled", "missing-ACK", "not-inactive", "foreign-CID", "foreign-record"} {
		t.Run(change, func(t *testing.T) {
			_, _, _, scope := containerRefreshFixture(t)
			cause := errors.New("lost current observation proof")
			verifies := 0
			p := &nativePreparedObservationRetirement{record: scope.records[0], cid: scope.cid, ready: true, verify: func(context.Context) error {
				verifies++
				if change == "verification" {
					return cause
				}
				return nil
			}}
			defer func() { _ = p.Close() }()
			a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
			a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
				child, cancel := context.WithCancel(ctx)
				defer cancel()
				if change == "cancelled" {
					cancel()
				}
				if change == "missing-ACK" {
					ack = nil
				}
				if change == "not-inactive" {
					copied := *ack.attempt
					copied.inactiveVerified = false
					ack = &nativeContainerStopACK{&copied}
				}
				if change == "foreign-CID" {
					p.cid = strings.Repeat("b", 64)
				}
				if change == "foreign-record" {
					p.record.Generation = "g" + strings.Repeat("e", 48)
				}
				_, err := p.afterContainerStop(child, ack)
				if p.consumed {
					before := verifies
					_, retry := p.afterContainerStop(ctx, ack)
					var uncertain *NativeEffectsUncertain
					if !errors.As(retry, &uncertain) || verifies != before {
						t.Fatal("failed cold observation was retried", retry, verifies)
					}
				}
				return err
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			expected := 0
			if change == "verification" {
				expected = 1
			}
			if err == nil || *removes != 0 || verifies != expected { // the step error ends removal before rm
				t.Fatal("unproved cold observation allowed removal or verification", change, err, *removes, verifies)
			}
			if change == "verification" && !errors.Is(err, cause) {
				t.Fatal("observation failure lost cause", err)
			}
			if change == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled parent bound was lifted for cold observation", err)
			}
		})
	}
}

func TestObservedNamespaceRetirementRequiresPreparedNativeSelection(t *testing.T) {
	if p, err := prepareNativeNoInitializationRetirement(t.Context(), nil, nil, RuntimeRecord{}, "", nativePinStopHooks{}); err == nil || p != nil {
		t.Fatal("missing native no-init selection prepared retirement", p, err)
	}
	if p, err := prepareNativeStaleRetirement(t.Context(), nil, nil, RuntimeRecord{}, "", nativePinStopHooks{}); err == nil || p != nil {
		t.Fatal("missing native stale selection prepared retirement", p, err)
	}
	var p *nativePreparedObservationRetirement
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil {
		t.Fatal("nil cold preparation accepted")
	}
	p = &nativePreparedObservationRetirement{ready: true}
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil {
		t.Fatal("unqualified cold preparation accepted")
	}
	closed, verified := 0, 0
	p.release = func() error { closed++; return nil }
	p.verify = func(context.Context) error { verified++; return nil }
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.afterContainerStop(t.Context(), nil); err == nil || closed != 1 || verified != 0 {
		t.Fatal("closed observation preparation retained verification", err, closed, verified)
	}
}

func TestNoInitializationSharedSelectionKeepsMarkerAndBootBinding(t *testing.T) {
	_, token, lock, mount, boot := noInitializationFixture(t)
	record, cid := token.Prepared().Record, token.cid
	token.cleanupAttempted = true
	noop := func(context.Context) error { return nil }
	err := token.stopUninitialized(t.Context(), NativeNamespaceStop{noop, noop, noop}, func(context.Context) error { return verifyNoInitializationEntries(lock, record) }, func(_ context.Context, p pinRetirementProgress) error {
		return publishNoInitializationAt(lock, record, cid, boot, mount, p)
	})
	if err != nil {
		t.Fatal("original no-init fixture failed before shared selection", err)
	}
	selected, found, err := readNoInitializationAt(lock, record, cid)
	if err != nil || !found {
		t.Fatal(err, found)
	}
	defer func() { _ = selected.Close() }()
	for _, currentBoot := range []string{boot, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"} {
		if err := checkUninitializedStopSelection(selected, record, cid, currentBoot, mount); err != nil {
			t.Fatal("valid current cold selection refused", err)
		}
	}
	changed := mount
	changed.Inode++
	if err := checkUninitializedStopSelection(selected, record, cid, boot, changed); err == nil {
		t.Fatal("same-boot no-init selection changed infrastructure binding")
	}
	if err := checkUninitializedStopSelection(selected, record, cid, "", mount); err == nil {
		t.Fatal("unknown boot supplied no-init observation proof")
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkUninitializedStopSelection(selected, record, cid, boot, mount); err == nil {
		t.Fatal("closed marker remained current no-init proof")
	}
}
