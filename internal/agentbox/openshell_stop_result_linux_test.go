package agentbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		actions.cleanup = func(c context.Context, _ *nativeStartedGeneration) error { return client.Stop(c, id) }
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
