package openshell

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func nativeRemovalScopeFixture(t *testing.T) nativeContainerRemovalScope {
	t.Helper()
	_, g, records := containerInventoryFixture(t)
	return nativeContainerRemovalScope{cid: g.cid, currentGeneration: g.current.Generation, records: records}
}

// Actual original-call protocol; all effects/ACKs are scripted. No admitted
// native owner, engine, namespace/table or filesystem removal is exercised.
func TestNativeContainerRemovalStopsEverySuffixAfterFailedStage(t *testing.T) {
	stages := []string{"qualify", "stop", "inactive", "refresh", "retire", "remove", "remaining", "namespace-metadata", "runtime-metadata"}
	for _, failure := range append(append([]string(nil), stages...), "success") {
		t.Run(failure, func(t *testing.T) {
			scope := nativeRemovalScopeFixture(t)
			cause := errors.New("scripted failed/lost stage result")
			var events []string
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					events = append(events, name)
					if failure == name {
						return cause
					}
					return nil
				}
			}
			a := nativeContainerRemovalActions{qualify: step("qualify"), stop: step("stop"), inactive: step("inactive"), remove: step("remove"), remaining: step("remaining")}
			a.refresh = func(ctx context.Context, ack *nativeContainerStopACK) error {
				for _, record := range scope.records {
					if err := ack.covers(record, scope.cid); err != nil {
						t.Fatal("fresh Stop ACK lost a retained generation", err)
					}
				}
				return step("refresh")(ctx)
			}
			a.retire = func(ctx context.Context, ack *nativeContainerStopACK) error {
				for _, record := range scope.records {
					if err := ack.covers(record, scope.cid); err != nil {
						t.Fatal(err)
					}
				}
				return step("retire")(ctx)
			}
			a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				if err := ack.permitsMetadata(scope.records[0], scope.cid, false); err != nil {
					t.Fatal("missing original rm ACK before namespace metadata", err)
				}
				return step("namespace-metadata")(ctx)
			}
			a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				if err := ack.permitsMetadata(scope.records[0], scope.cid, true); err != nil {
					t.Fatal("missing namespace-metadata ACK before private keys", err)
				}
				return step("runtime-metadata")(ctx)
			}
			notices := captureNativeRemovalNotices(t)
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			// Up to the rm ACK a failed step ends the removal with its own
			// plain error. After it, "remaining" is a warning and cleanup goes
			// on; failed namespace metadata keeps the dependent private
			// metadata (no runtime-metadata call) and is a warning too.
			afterRemove := map[string]bool{"remaining": true, "namespace-metadata": true, "runtime-metadata": true}
			want := stages
			for n, stage := range stages {
				if stage == failure && (!afterRemove[stage] || stage == "namespace-metadata") {
					want = stages[:n+1]
					break
				}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatal("native removal step order changed", failure, events, want, err)
			}
			var uncertain *NativeEffectsUncertain
			switch {
			case failure == "success":
				if err != nil || len(*notices) != 0 {
					t.Fatal(err, *notices)
				}
			case afterRemove[failure]:
				if err != nil || len(*notices) != 1 || !errors.Is((*notices)[0], cause) {
					t.Fatal("cleanup failure after rm was not a named warning", failure, err, *notices)
				}
			default:
				if !errors.Is(err, cause) || errors.As(err, &uncertain) {
					t.Fatal("failed step before rm lost its plain error", failure, err)
				}
			}
		})
	}
}
func TestNativeContainerRemovalACKsAreScopeAndStageBound(t *testing.T) {
	scope := nativeRemovalScopeFixture(t)
	var stop *nativeContainerStopACK
	var removed *nativeContainerRemoveACK
	noop := func(context.Context) error { return nil }
	a := nativeContainerRemovalActions{qualify: noop, stop: noop, inactive: noop, remove: noop, remaining: noop}
	a.refresh = func(_ context.Context, ack *nativeContainerStopACK) error {
		stop = ack
		wrong := cloneRuntimeRecord(scope.records[0])
		wrong.Files["supervisor/policy.yaml"] = strings.Repeat("f", 64)
		if ack.covers(wrong, scope.cid) == nil || ack.covers(scope.records[0], strings.Repeat("b", 64)) == nil {
			t.Fatal("Stop ACK accepted foreign immutable record/CID")
		}
		return nil
	}
	a.retire = func(_ context.Context, ack *nativeContainerStopACK) error {
		for _, record := range scope.records {
			if err := ack.covers(record, scope.cid); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	a.namespaceMetadata = func(_ context.Context, ack *nativeContainerRemoveACK) error {
		removed = ack
		if stop.covers(scope.records[0], scope.cid) == nil {
			t.Fatal("Stop ACK remained valid after CID-removal attempt")
		}
		if err := ack.permitsMetadata(scope.records[0], scope.cid, false); err != nil {
			t.Fatal(err)
		}
		if ack.permitsMetadata(scope.records[0], scope.cid, true) == nil {
			t.Fatal("private keys allowed before namespace metadata ACK")
		}
		if ack.permitsMetadata(scope.records[0], strings.Repeat("b", 64), false) == nil {
			t.Fatal("rm ACK accepted another CID")
		}
		return nil
	}
	a.runtimeMetadata = func(_ context.Context, ack *nativeContainerRemoveACK) error {
		return ack.permitsMetadata(scope.records[0], scope.cid, true)
	}
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal(err)
	}
	if stop == nil || removed == nil {
		t.Fatal("protocol did not produce original-call ACKs")
	}
	if removed.permitsMetadata(scope.records[0], scope.cid, true) == nil {
		t.Fatal("completed native removal ACK remained reusable")
	}
	if (&nativeContainerStopACK{}).covers(scope.records[0], scope.cid) == nil || (&nativeContainerRemoveACK{}).permitsMetadata(scope.records[0], scope.cid, false) == nil {
		t.Fatal("zero value minted acknowledgement")
	}
}
func TestNativeContainerRemovalCancellationRetainsOnlyOriginalCleanupAdmission(t *testing.T) {
	for _, point := range []string{"before-qualify", "after-qualify", "after-stop"} {
		t.Run(point, func(t *testing.T) {
			scope := nativeRemovalScopeFixture(t)
			type admissionKey struct{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), admissionKey{}, "original admitted operation"))
			defer cancel()
			if point == "before-qualify" {
				cancel()
			}
			var events []string
			post := func(name string) func(context.Context) error {
				return func(check context.Context) error {
					events = append(events, name)
					deadline, ok := check.Deadline()
					if check.Err() != nil || check.Value(admissionKey{}) != "original admitted operation" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 90*time.Second {
						t.Fatal("cleanup lost original bounded admission", name, check.Err())
					}
					return nil
				}
			}
			a := nativeContainerRemovalActions{
				qualify: func(context.Context) error {
					events = append(events, "qualify")
					if point == "after-qualify" {
						cancel()
					}
					return nil
				},
				stop: func(context.Context) error {
					events = append(events, "stop")
					if point == "after-stop" {
						cancel()
					}
					return nil
				},
				inactive: post("inactive"), remove: post("remove"), remaining: post("remaining"),
			}
			a.refresh = func(check context.Context, _ *nativeContainerStopACK) error { return post("refresh")(check) }
			a.retire = func(check context.Context, _ *nativeContainerStopACK) error { return post("retire")(check) }
			a.namespaceMetadata = func(check context.Context, _ *nativeContainerRemoveACK) error {
				return post("namespace-metadata")(check)
			}
			a.runtimeMetadata = func(check context.Context, _ *nativeContainerRemoveACK) error { return post("runtime-metadata")(check) }
			err := completeNativeContainerRemoval(ctx, scope, a)
			switch point {
			case "before-qualify":
				if !errors.Is(err, context.Canceled) || len(events) != 0 {
					t.Fatal("cancelled operation began effects", err, events)
				}
			case "after-qualify":
				if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(events, []string{"qualify"}) {
					t.Fatal("cancelled operation stopped runtime", err, events)
				}
			default:
				if err != nil || !reflect.DeepEqual(events, []string{"qualify", "stop", "inactive", "refresh", "retire", "remove", "remaining", "namespace-metadata", "runtime-metadata"}) {
					t.Fatal("known Stop cleanup was abandoned or replayed", err, events)
				}
			}
		})
	}
}
func TestNativeContainerRemovalRejectsMissingWiringAndIncompleteScopeBeforeEffects(t *testing.T) {
	scope := nativeRemovalScopeFixture(t)
	if err := completeNativeContainerRemoval(t.Context(), scope, nativeContainerRemovalActions{}); err == nil {
		t.Fatal("incomplete cleanup wiring accepted")
	}
	for _, change := range []string{"cid", "current", "empty", "duplicate", "foreign"} {
		t.Run(change, func(t *testing.T) {
			s := nativeRemovalScopeFixture(t)
			switch change {
			case "cid":
				s.cid = "coder"
			case "current":
				s.currentGeneration = "g" + strings.Repeat("f", 48)
			case "empty":
				s.records = nil
			case "duplicate":
				s.records = append(s.records, s.records[0])
			case "foreign":
				s.records[0].Launch.Binding.Incarnation = strings.Repeat("f", 32)
			}
			calls := 0
			noop := func(context.Context) error { calls++; return nil }
			a := nativeContainerRemovalActions{qualify: noop, stop: noop, inactive: noop, remove: noop, remaining: noop, refresh: func(context.Context, *nativeContainerStopACK) error { calls++; return nil }, retire: func(context.Context, *nativeContainerStopACK) error { calls++; return nil }, namespaceMetadata: func(context.Context, *nativeContainerRemoveACK) error { calls++; return nil }, runtimeMetadata: func(context.Context, *nativeContainerRemoveACK) error { calls++; return nil }}
			if err := completeNativeContainerRemoval(t.Context(), s, a); err == nil || calls != 0 {
				t.Fatal("invalid removal scope reached callbacks", change, err, calls)
			}
		})
	}
}

func captureNativeRemovalNotices(t *testing.T) *[]error {
	t.Helper()
	var notices []error
	old := nativeRemovalNotice
	nativeRemovalNotice = func(err error) { notices = append(notices, err) }
	t.Cleanup(func() { nativeRemovalNotice = old })
	return &notices
}

// A typed lost-result error is never downgraded to a warning.
func TestNativeContainerRemovalKeepsTypedUncertaintyAfterRemove(t *testing.T) {
	scope := nativeRemovalScopeFixture(t)
	ok := func(context.Context) error { return nil }
	lost := &NativeEffectsUncertain{Cause: errors.New("lost reply")}
	a := nativeContainerRemovalActions{qualify: ok, stop: ok, inactive: ok, remove: ok, remaining: func(context.Context) error { return lost }}
	a.refresh = func(context.Context, *nativeContainerStopACK) error { return nil }
	a.retire = func(context.Context, *nativeContainerStopACK) error { return nil }
	a.namespaceMetadata = func(context.Context, *nativeContainerRemoveACK) error {
		t.Fatal("continued after lost result")
		return nil
	}
	a.runtimeMetadata = func(context.Context, *nativeContainerRemoveACK) error { return nil }
	notices := captureNativeRemovalNotices(t)
	var uncertain *NativeEffectsUncertain
	if err := completeNativeContainerRemoval(t.Context(), scope, a); !errors.As(err, &uncertain) || len(*notices) != 0 {
		t.Fatal("typed uncertainty downgraded", err, *notices)
	}
}

// decision3Outcome: a failure up to the rm ACK returns its own plain error; a
// cleanup failure after it is exactly one named warning and no error.
func decision3Outcome(err error, notices []error, cause error, afterRemove bool) bool {
	var uncertain *NativeEffectsUncertain
	if afterRemove {
		return err == nil && len(notices) == 1 && errors.Is(notices[0], cause)
	}
	return errors.Is(err, cause) && !errors.As(err, &uncertain) && len(notices) == 0
}
