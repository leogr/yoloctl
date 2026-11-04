package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

type destroyRemovalFixture struct {
	remove func(context.Context) error
	close  func() error
}

func (f destroyRemovalFixture) Remove(ctx context.Context) error { return f.remove(ctx) }
func (f destroyRemovalFixture) Close() error                     { return f.close() }

func TestNativeDestroyPrefixStopsAtEveryUnacknowledgedEffect(t *testing.T) {
	for _, failure := range []string{"", "holder", "forwards", "remove", "close"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			cause := errors.New("not found: no such container: original reply lost")
			step := func(name string) error {
				events = append(events, name)
				if name == failure {
					return cause
				}
				return nil
			}
			removal := destroyRemovalFixture{
				remove: func(context.Context) error { return step("remove") },
				close:  func() error { return step("close") },
			}
			err := removePreparedNativeBox(t.Context(), removal,
				func(context.Context) error { return step("holder") },
				func(context.Context) error { return step("forwards") })
			want := []string{"holder", "forwards", "remove", "close"}
			switch failure {
			case "holder":
				want = []string{"holder", "close"}
			case "forwards":
				want = []string{"holder", "forwards", "close"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("effects after unacknowledged %q: got %v, want %v", failure, events, want)
			}
			if failure == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			// Old-path semantics: an ordinary step error stays a known failure,
			// never a blanket uncertain hold, and is still returned unchanged.
			var uncertain *agentbox.NativeEffectsUncertain
			if !errors.Is(err, cause) || errors.As(err, &uncertain) || workerEffectsUncertain(err) {
				t.Fatalf("native destroy changed an ordinary step result: %v", err)
			}
		})
	}
}

func TestNativeDestroyPrefixPropagatesOnlyTypedStepUncertainty(t *testing.T) {
	for _, failure := range []string{"holder", "forwards", "remove"} {
		t.Run(failure, func(t *testing.T) {
			cause := &agentbox.NativeEffectsUncertain{Cause: errors.New("acknowledgement lost")}
			step := func(name string) error {
				if name == failure {
					return cause
				}
				return nil
			}
			closed := 0
			removal := destroyRemovalFixture{
				remove: func(context.Context) error { return step("remove") },
				close:  func() error { closed++; return nil },
			}
			err := removePreparedNativeBox(t.Context(), removal,
				func(context.Context) error { return step("holder") },
				func(context.Context) error { return step("forwards") })
			var uncertain *agentbox.NativeEffectsUncertain
			if !errors.As(err, &uncertain) || uncertain != cause || !workerEffectsUncertain(err) || closed != 1 {
				t.Fatalf("typed %s uncertainty must propagate unchanged after one close: err=%v closes=%d", failure, err, closed)
			}
		})
	}
}

func TestNativeDestroyPrefixClosesBeforeEffectsOnRefusal(t *testing.T) {
	for _, failure := range []string{"canceled", "holder absent", "forwards absent"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "canceled" {
				cancel()
			}
			closed := 0
			removal := destroyRemovalFixture{
				remove: func(context.Context) error { t.Fatal("removed without a qualified prefix"); return nil },
				close:  func() error { closed++; return nil },
			}
			step := func(context.Context) error { t.Fatal("prefix effect on refusal"); return nil }
			holder, forward := step, step
			if failure == "holder absent" {
				holder = nil
			}
			if failure == "forwards absent" {
				forward = nil
			}
			err := removePreparedNativeBox(ctx, removal, holder, forward)
			var uncertain *agentbox.NativeEffectsUncertain
			if err == nil || errors.As(err, &uncertain) || closed != 1 {
				t.Fatalf("pre-effect refusal lost cleanup/distinction: err=%v, closes=%d", err, closed)
			}
		})
	}
}

func TestNativeDestroyPrefixPreservesSDKAndCloseFailuresTogether(t *testing.T) {
	cause := &agentbox.NativeEffectsUncertain{Cause: errors.New("lost SDK reply")}
	closeErr := errors.New("retained descriptor close failed")
	removal := destroyRemovalFixture{
		remove: func(context.Context) error { return cause },
		close:  func() error { return closeErr },
	}
	ack := func(context.Context) error { return nil }
	err := removePreparedNativeBox(t.Context(), removal, ack, ack)
	if !errors.Is(err, cause) || !errors.Is(err, closeErr) {
		t.Fatalf("native destroy discarded one original failure: %v", err)
	}
}

func TestNativeDestroyPrefixStopsOnCancellationBetweenAcknowledgements(t *testing.T) {
	for _, stage := range []string{"holder", "forwards"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var events []string
			step := func(name string) error {
				events = append(events, name)
				if name == stage {
					cancel()
				}
				return nil
			}
			removal := destroyRemovalFixture{
				remove: func(context.Context) error { t.Fatal("SDK called after cancellation"); return nil },
				close:  func() error { return step("close") },
			}
			err := removePreparedNativeBox(ctx, removal,
				func(context.Context) error { return step("holder") },
				func(context.Context) error { return step("forwards") })
			want := []string{"holder", "close"}
			if stage == "forwards" {
				want = []string{"holder", "forwards", "close"}
			}
			// The canceled worker context itself withholds completion
			// (recordWorkerCompletion), so no wrapper type is added here.
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(events, want) {
				t.Fatalf("cancellation admitted suffix or lost original result: err=%v events=%v", err, events)
			}
		})
	}
}
