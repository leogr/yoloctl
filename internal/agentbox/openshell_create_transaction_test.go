package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func nativeTransactionFixture(events *[]string, failed string, failure error) nativeCreateActions {
	step := func(name string) func(context.Context) error {
		return func(context.Context) error {
			*events = append(*events, name)
			if failed == name {
				return failure
			}
			return nil
		}
	}
	return nativeCreateActions{
		preflight: step("preflight"), reserve: step("reserve"),
		create:  func(ctx context.Context) (string, error) { return strings.Repeat("a", 64), step("create")(ctx) },
		inspect: func(ctx context.Context) (string, error) { return strings.Repeat("a", 64), step("inspect")(ctx) },
		prepare: step("prepare"), start: step("start"), activate: step("activate"), ready: step("ready"), forwards: step("forwards"),
		cleanup: func(ctx context.Context, progress nativeCreateProgress) error { return step("cleanup")(ctx) },
	}
}

func TestNativeCreateTransactionUnacknowledgedEffectsNeverRetryOrCleanup(t *testing.T) {
	order := []string{"preflight", "reserve", "create", "inspect", "prepare", "start", "activate", "ready", "forwards"}
	for _, stage := range []string{"reserve", "create", "start"} {
		t.Run(stage, func(t *testing.T) {
			var events []string
			// Only a genuinely lost reply is uncertain: no cleanup, no retry.
			failure := context.DeadlineExceeded
			err := completeNativeContainerCreation(t.Context(), nativeTransactionFixture(&events, stage, failure))
			var uncertain *NativeEffectsUncertain
			if !errors.As(err, &uncertain) || !errors.Is(err, failure) || !slices.Equal(events, order[:slices.Index(order, stage)+1]) {
				t.Fatal("unacknowledged action retried, cleaned up or lost uncertainty", events, err)
			}
			// An engine error reply is a plain failure followed by cleanup.
			events = nil
			plain := errors.New("engine refused")
			err = completeNativeContainerCreation(t.Context(), nativeTransactionFixture(&events, stage, plain))
			if !errors.Is(err, plain) || errors.As(err, &uncertain) || events[len(events)-1] != "cleanup" {
				t.Fatal("engine error became uncertain or skipped cleanup", events, err)
			}
		})
	}
}

func TestNativeCreateTransactionKnownFailuresRequireOwnedCleanupAndKeepHistory(t *testing.T) {
	for _, stage := range []string{"inspect", "prepare", "activate", "ready", "forwards"} {
		for _, cleanupFails := range []bool{false, true} {
			t.Run(stage+"/"+map[bool]string{false: "clean", true: "unproved"}[cleanupFails], func(t *testing.T) {
				var events []string
				failure, cleanupError := errors.New("startup failed"), errors.New("stop acknowledgement lost")
				actions := nativeTransactionFixture(&events, stage, failure)
				actions.cleanup = func(ctx context.Context, got nativeCreateProgress) error {
					events = append(events, "cleanup")
					wantStarted := stage == "activate" || stage == "ready" || stage == "forwards"
					wantActivated := stage == "ready" || stage == "forwards"
					wantReady := stage == "forwards"
					if !got.Reserved || !got.Created || got.Started != wantStarted || got.Activated != wantActivated || got.Ready != wantReady || got.Phase != stage || got.ContainerID != strings.Repeat("a", 64) {
						t.Fatal("acknowledgement history was invented or lost", got)
					}
					if cleanupFails {
						return cleanupError
					}
					return nil
				}
				var notices []error
				old := nativeCreateCleanupNotice
				nativeCreateCleanupNotice = func(err error) { notices = append(notices, err) }
				defer func() { nativeCreateCleanupNotice = old }()
				err := completeNativeContainerCreation(t.Context(), actions)
				var uncertain *NativeEffectsUncertain
				cleanupCalls := 0
				for _, event := range events {
					if event == "cleanup" {
						cleanupCalls++
					}
				}
				// A failed cleanup is a named warning; the create keeps its
				// plain original error (decision #3).
				if !errors.Is(err, failure) || errors.As(err, &uncertain) || cleanupFails != (len(notices) == 1 && errors.Is(notices[0], cleanupError)) || events[len(events)-1] != "cleanup" || cleanupCalls != 1 {
					t.Fatal("failed startup or unproved cleanup became known success", events, err)
				}
			})
		}
	}
}

func TestNativeCreateTransactionPreflightFailurePreventsReservation(t *testing.T) {
	var events []string
	failure := errors.New("selected helper unavailable")
	err := completeNativeContainerCreation(t.Context(), nativeTransactionFixture(&events, "preflight", failure))
	if err != failure || !slices.Equal(events, []string{"preflight"}) {
		t.Fatal("preflight error reached reservation/create or changed outcome", events, err)
	}
}

func TestNativeCreateTransactionNestedUncertaintyNeverStartsMoreEffects(t *testing.T) {
	for _, stage := range []string{"inspect", "prepare", "activate", "ready", "forwards"} {
		t.Run(stage, func(t *testing.T) {
			var events []string
			failure := &NativeEffectsUncertain{Cause: errors.New("nested effect acknowledgement lost")}
			err := completeNativeContainerCreation(t.Context(), nativeTransactionFixture(&events, stage, failure))
			if err != failure || events[len(events)-1] != stage || slices.Contains(events, "cleanup") {
				t.Fatal("nested unresolved effect triggered another mutation", events, err)
			}
		})
	}
}

func TestNativeCreateTransactionCancellationBeforeEffectsAndDuringKnownCleanup(t *testing.T) {
	var events []string
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := completeNativeContainerCreation(ctx, nativeTransactionFixture(&events, "", nil)); !errors.Is(err, context.Canceled) || len(events) != 0 {
		t.Fatal("cancelled admission reached an effect", events, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	type admissionKey struct{}
	ctx = context.WithValue(ctx, admissionKey{}, "same-admitted-scope")
	actions := nativeTransactionFixture(&events, "", nil)
	actions.prepare = func(context.Context) error { cancel(); return context.Canceled }
	cleaned := false
	actions.cleanup = func(cleanup context.Context, progress nativeCreateProgress) error {
		deadline, bounded := cleanup.Deadline()
		if cleanup.Err() != nil || !bounded || time.Until(deadline) > 91*time.Second || cleanup.Value(admissionKey{}) != "same-admitted-scope" || !progress.Created || progress.Started {
			t.Fatal("owned cleanup lost its bound/admission/history", progress)
		}
		cleaned = true
		return nil
	}
	if err := completeNativeContainerCreation(ctx, actions); !errors.Is(err, context.Canceled) || !cleaned {
		t.Fatal("known owned creation did not receive bounded cancellation cleanup", err)
	}
}

func TestNativeCreateTransactionSuccessIsIndependentOfClientExit(t *testing.T) {
	var events []string
	ctx, cancel := context.WithCancel(t.Context())
	if err := completeNativeContainerCreation(ctx, nativeTransactionFixture(&events, "", nil)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if !slices.Equal(events, []string{"preflight", "reserve", "create", "inspect", "prepare", "start", "activate", "ready", "forwards"}) {
		t.Fatal("success scheduled teardown or lost phase order", events)
	}
}

func TestNativeCreateTransactionRefusesIncompleteWiringAndInvalidIdentity(t *testing.T) {
	var events []string
	actions := nativeTransactionFixture(&events, "", nil)
	actions.cleanup = nil
	if err := completeNativeContainerCreation(t.Context(), actions); err == nil || len(events) != 0 {
		t.Fatal("partial wiring reached effects", events, err)
	}
	for _, id := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		events = nil
		actions = nativeTransactionFixture(&events, "", nil)
		actions.create = func(context.Context) (string, error) { return id, nil }
		var uncertain *NativeEffectsUncertain
		if err := completeNativeContainerCreation(t.Context(), actions); err == nil || errors.As(err, &uncertain) || slices.Contains(events, "inspect") || slices.Contains(events, "prepare") {
			t.Fatal("invalid create acknowledgement allowed another action", events, err)
		}
	}
}

func TestNativeCreateTransactionInspectionCannotReplaceAcknowledgedIdentity(t *testing.T) {
	for _, observed := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("b", 64)} {
		var events []string
		actions := nativeTransactionFixture(&events, "", nil)
		actions.inspect = func(context.Context) (string, error) { return observed, nil }
		cleaned := false
		actions.cleanup = func(_ context.Context, progress nativeCreateProgress) error {
			if progress.ContainerID != strings.Repeat("a", 64) || !progress.Created || progress.Started {
				t.Fatal("observation selected cleanup identity or invented start history", progress)
			}
			cleaned = true
			return nil
		}
		if err := completeNativeContainerCreation(t.Context(), actions); err == nil || !cleaned || slices.Contains(events, "prepare") || slices.Contains(events, "start") {
			t.Fatal("foreign observation entered later phases", events, err)
		}
	}
}

// A cancelled, already-started engine process (create or start) returns its
// exit error, not a context sentinel: it stays a lost reply, without cleanup.
// A real nonzero engine reply with a live context is plain and cleaned up.
func TestNativeCreateTransactionCancelledEngineProcessIsLost(t *testing.T) {
	for _, stage := range []string{"create", "start"} {
		for _, cancelled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/cancelled-%t", stage, cancelled), func(t *testing.T) {
				var events []string
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				run := func(effect context.Context) error {
					events = append(events, stage)
					if !cancelled {
						return exec.CommandContext(effect, "false").Run()
					}
					cmd := exec.CommandContext(effect, "sleep", "10")
					if err := cmd.Start(); err != nil {
						return err
					}
					cancel()
					return cmd.Wait()
				}
				actions := nativeTransactionFixture(&events, "", nil)
				if stage == "create" {
					actions.create = func(effect context.Context) (string, error) { return "", run(effect) }
				} else {
					actions.start = run
				}
				err := completeNativeContainerCreation(ctx, actions)
				var exit *exec.ExitError
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &exit) || errors.As(err, &uncertain) != cancelled || slices.Contains(events, "cleanup") == cancelled {
					t.Fatal("cancelled started engine process misclassified", stage, cancelled, events, err)
				}
			})
		}
	}
}
