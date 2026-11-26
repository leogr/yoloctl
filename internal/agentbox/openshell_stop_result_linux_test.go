package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

// Storage verification failed: the exact container, still the box's by its
// engine labels, gets its one engine stop by ID; the disk-dependent part of
// the stop is returned as a leftover, named by the box's network mode.
func TestStopWithoutStorageStillStopsTheExactContainerOnce(t *testing.T) {
	cause := errors.New("data disk identity differs")
	for _, unconfined := range []bool{false, true} {
		target := resultTargetJSON("coder", "fixture", true)
		if unconfined {
			target = strings.Replace(target, `"yoloctl.space":"fixture"`, `"yoloctl.space":"fixture","`+LabelNetworkUnconfined+`":"true"`, 1)
		}
		client, log := resultStopFixture(t, target)
		err := stopExactContainerWithoutStorage(t.Context(), client, "coder", resultTargetID, cause)
		var rest *ResultLeftover
		want := "runtime record checks"
		if unconfined {
			want = "network namespace retirement"
		}
		commands := loggedCommands(t, log)
		if !errors.As(err, &rest) || rest.What != want || !errors.Is(err, cause) ||
			strings.Count(commands, "nerdctl stop --time 30 "+resultTargetID) != 1 || strings.Contains(commands, resultReplacementID) {
			t.Fatalf("unconfined=%v: %v\n%s", unconfined, err, commands)
		}
	}
	// Not the box's container any more: no stop, a plain error.
	client, log := resultStopFixture(t, resultTargetJSON("other", "fixture", true))
	err := stopExactContainerWithoutStorage(t.Context(), client, "coder", resultTargetID, cause)
	var rest *ResultLeftover
	if err == nil || errors.As(err, &rest) || strings.Contains(loggedCommands(t, log), "nerdctl stop") {
		t.Fatalf("other owner: %v", err)
	}
}

// The engine-call record follows the real client through the native start
// transaction: a refusal before the engine start records none; a readiness
// failure whose own cleanup invokes the engine stop (failing or not) records
// the start and the stop after it, so box start's rollback stops nothing again.
func TestEngineCallsFollowTheNativeStartTransaction(t *testing.T) {
	for _, stopStatus := range []int{0, 23} {
		client, log, _ := nativeCleanupAdapterFixture(t, stopStatus)
		g, _ := nativeStartedGenerationFixture(t)
		g.startAttempted, g.startAcknowledged = false, false
		id := strings.Repeat("a", 64)
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_NATIVE_CLEANUP_FIXTURE/commands.log\"\ncase \"$*\" in\n" +
			" 'shell yolo-fixture -- sudo nerdctl start " + g.cid + "') exit 0 ;;\n" +
			" 'shell yolo-fixture -- sudo nerdctl stop --time 30 " + id + "') exit \"$YOLOCTL_NATIVE_CLEANUP_STOP_STATUS\" ;;\n" +
			" *) exit 97 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(log), "limactl"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		var events []string
		actions, _ := generationStartScript(t, "", nil, &events)
		actions.prepare = func(context.Context) (*nativeStartedGeneration, error) { return g, nil }
		actions.activate = func(context.Context, *nativeStartedGeneration) error {
			g.activation = &openshell.NativeActivation{}
			return nil
		}
		actions.ready = func(context.Context, *nativeStartedGeneration) error { return errors.New("readiness failed") }
		actions.cleanup = func(c *detachedCleanup, _ *nativeStartedGeneration) error {
			return c.step(func(stop context.Context) error { return client.Stop(stop, id) })
		}
		actions.start = func(c context.Context, actual *nativeStartedGeneration) error { return client.Start(c, actual.cid) }
		calls := &container.EngineCalls{}
		err := completeNativeGenerationStart(container.WithEngineCalls(t.Context(), calls), actions)
		commands, _ := os.ReadFile(log)
		if err == nil || !calls.Started() || !calls.StoppedAfterStart() || strings.Count(string(commands), "nerdctl stop") != 1 {
			t.Fatalf("stop status %d: %v started %v stopped %v\n%s", stopStatus, err, calls.Started(), calls.StoppedAfterStart(), commands)
		}
	}
	// A refusal before the engine start: nothing recorded.
	var events []string
	actions, _ := generationStartScript(t, "before-start", errors.New("native Start requires an independently retired SSH endpoint"), &events)
	calls := &container.EngineCalls{}
	if err := completeNativeGenerationStart(container.WithEngineCalls(t.Context(), calls), actions); err == nil || calls.Started() || calls.StoppedAfterStart() {
		t.Fatalf("pre-engine refusal: %v %v", err, calls.Started())
	}
}

// The storage verifier runs at each of the stop's points. A failure before
// the engine stop (at the entry, or after an earlier success) takes the
// engine-only fallback once; one after it leaves the rest as a leftover and
// stops nothing again; other errors pass through untouched.
func TestResultStopStorageFailureIsDecidedByTheEngineStop(t *testing.T) {
	down := errors.New("disk identity differs")
	sequence := func(results ...error) func(context.Context) error {
		i := 0
		return func(context.Context) error {
			defer func() { i++ }()
			if i < len(results) {
				return results[i]
			}
			return nil
		}
	}
	// The route's own order: entry check, ownership check, engine stop,
	// inactivity check.
	route := func(ctx context.Context, verify func(context.Context) error, invoked func()) error {
		for range 2 {
			if err := verify(ctx); err != nil {
				return err
			}
		}
		invoked()
		if err := verify(ctx); err != nil {
			return &NativeEffectsUncertain{Cause: fmt.Errorf("acknowledged native stop cleanup remains unproved: %w", err)}
		}
		return nil
	}
	for label, c := range map[string]struct {
		verifier  func(context.Context) error
		fallbacks int
		leftover  string
	}{
		"fails at entry":              {sequence(down), 1, "fallback"},
		"succeeds then fails":         {sequence(nil, down), 1, "fallback"},
		"fails after the engine stop": {sequence(nil, nil, down), 0, "checks after the stop"},
		"never fails":                 {sequence(), 0, ""},
	} {
		fallbacks := 0
		err := stopForResultWithStorageFallback(t.Context(), c.verifier, route, func(context.Context, error) error {
			fallbacks++
			return &ResultLeftover{What: "fallback", Cause: down}
		})
		var rest *ResultLeftover
		var marker *NativeEffectsUncertain
		if fallbacks != c.fallbacks || (c.leftover == "") != (err == nil) || err != nil && (!errors.As(err, &rest) || rest.What != c.leftover || errors.As(err, &marker)) {
			t.Fatalf("%s: %v fallbacks %d", label, err, fallbacks)
		}
	}
	other := errors.New("runtime record differs")
	err := stopForResultWithStorageFallback(t.Context(), sequence(), func(context.Context, func(context.Context) error, func()) error { return other },
		func(context.Context, error) error { t.Fatal("fallback for a non-storage error"); return nil })
	if !errors.Is(err, other) {
		t.Fatalf("other error: %v", err)
	}
}

// The real result-mode entry with a failing storage verifier, against a fake
// engine: one engine stop of the exact ID, the disk work a leftover.
func TestResultStopEntryFallsBackWhenStorageFails(t *testing.T) {
	previous := nativeVMRootAdmitted
	nativeVMRootAdmitted = func() bool { return true }
	t.Cleanup(func() { nativeVMRootAdmitted = previous })
	client, log := resultStopFixture(t, resultTargetJSON("coder", "fixture", true))
	a := LiveApplier{Client: client, VerifyStorage: func(context.Context) error { return errors.New("disk identity differs") }}
	err := a.stopNativeRuntimeForResult(t.Context(), "coder", resultTargetID)
	var rest *ResultLeftover
	commands := loggedCommands(t, log)
	if !errors.As(err, &rest) || rest.What != "runtime record checks" || strings.Count(commands, "nerdctl stop") != 1 ||
		!strings.Contains(commands, "nerdctl stop --time 30 "+resultTargetID) {
		t.Fatalf("%v\n%s", err, commands)
	}
	// Storage verified, but the runtime record of this fake container cannot
	// be read: that ownership boundary is reported and nothing is stopped.
	client, log = resultStopFixture(t, resultTargetJSON("coder", "fixture", true))
	a = LiveApplier{Client: client, VerifyStorage: func(context.Context) error { return nil }}
	if err := a.stopNativeRuntimeForResult(t.Context(), "coder", resultTargetID); err == nil || errors.As(err, &rest) ||
		strings.Contains(loggedCommands(t, log), "nerdctl stop") {
		t.Fatalf("unreadable record: %v", err)
	}
}

func TestForegroundResultStopStorageUsesCallerDeadline(t *testing.T) {
	shortRuntimeStepFailSafe(t, time.Millisecond)
	old := nativeVMRootAdmitted
	nativeVMRootAdmitted = func() bool { return true }
	t.Cleanup(func() { nativeVMRootAdmitted = old })
	client, log := resultStopFixture(t, resultTargetJSON("coder", "fixture", true))
	caller, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deadline, _ := caller.Deadline()
	checked := false
	applier := LiveApplier{Client: client, VerifyStorage: func(ctx context.Context) error {
		checked = true
		if got, ok := ctx.Deadline(); !ok || got != deadline {
			t.Fatalf("foreground storage was shortened: %v, want %v", got, deadline)
		}
		time.Sleep(5 * time.Millisecond)
		return ctx.Err()
	}}
	// This fixture lacks a native record after storage approves. It must
	// refuse there without taking the engine-only storage-failure route.
	err := applier.stopNativeRuntimeForResult(caller, "coder", resultTargetID)
	var leftover *ResultLeftover
	if !checked || err == nil || errors.As(err, &leftover) || strings.Contains(loggedCommands(t, log), "nerdctl stop") {
		t.Fatal("foreground stop fell back after shortening the storage check", err)
	}
}
