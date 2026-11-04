package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/openshell"
)

var generationStartSequence = []string{"preflight", "stop-previous", "prepare", "before-start", "start", "activate", "ready", "forwards"}

// Callback protocol fixtures do not provide actual root/preparation/kernel
// authority. The separate real Client adapter test covers Start replies only.
func generationStartScript(t *testing.T, fail string, cause error, events *[]string) (nativeGenerationStartActions, *nativeStartedGeneration) {
	t.Helper()
	g, _ := nativeStartedGenerationFixture(t)
	g.startAttempted, g.startAcknowledged = false, false
	step := func(name string) error {
		*events = append(*events, name)
		if name == fail {
			return cause
		}
		return nil
	}
	actions := nativeGenerationStartActions{
		preflight:    func(context.Context) error { return step("preflight") },
		stopPrevious: func(context.Context) error { return step("stop-previous") },
		prepare: func(context.Context) (*nativeStartedGeneration, error) {
			if err := step("prepare"); err != nil {
				return nil, err
			}
			return g, nil
		},
		beforeStart: func(context.Context, *nativeStartedGeneration) error { return step("before-start") },
		start: func(_ context.Context, actual *nativeStartedGeneration) error {
			if actual != g || !g.startAttempted || g.startAcknowledged {
				t.Fatal("engine Start did not receive its original pending attempt")
			}
			return step("start")
		},
		activate: func(_ context.Context, actual *nativeStartedGeneration) error {
			if actual != g || !g.startAcknowledged || !g.activationAttempted {
				t.Fatal("activation preceded real Start ACK/original attempt")
			}
			g.activation = &openshell.NativeActivation{}
			return step("activate")
		},
		ready:    func(context.Context, *nativeStartedGeneration) error { return step("ready") },
		forwards: func(context.Context, *nativeStartedGeneration) error { return step("forwards") },
		cleanup: func(c context.Context, actual *nativeStartedGeneration) error {
			deadline, ok := c.Deadline()
			if actual != g || c.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 90*time.Second {
				t.Fatal("original cleanup lost bounded independent context")
			}
			return step("cleanup")
		},
	}
	return actions, g
}

func TestNativeGenerationStartProtocolOrdersOriginalACKsAndNoSuccessTeardown(t *testing.T) {
	var events []string
	actions, g := generationStartScript(t, "", nil, &events)
	if err := completeNativeGenerationStart(t.Context(), actions); err != nil || !reflect.DeepEqual(events, generationStartSequence) || !g.startAcknowledged || !g.activationAttempted || g.activation == nil {
		t.Fatal("fresh generation success lost original ACK order", err, events)
	}
}
func TestNativeGenerationStartProtocolStopsAtEveryFailureAndRetainsUncertainty(t *testing.T) {
	for index, stage := range generationStartSequence {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/typed-%t", stage, nested), func(t *testing.T) {
				var events []string
				original := errors.New("no such container: failure at " + stage)
				var cause = original
				if nested {
					cause = &NativeEffectsUncertain{Cause: original}
				}
				actions, g := generationStartScript(t, stage, cause, &events)
				err := completeNativeGenerationStart(t.Context(), actions)
				want := append([]string(nil), generationStartSequence[:index+1]...)
				cleanup := !nested && (stage == "before-start" || stage == "activate" || stage == "ready" || stage == "forwards")
				if cleanup {
					want = append(want, "cleanup")
				}
				var uncertain *NativeEffectsUncertain
				// An engine error at Start is a plain failure now (leogr's rule);
				// only a typed nested lost result stays uncertain.
				if !errors.Is(err, original) || errors.As(err, &uncertain) != nested || !reflect.DeepEqual(events, want) {
					t.Fatal("failed/lost stage continued/replayed/misclassified", err, events, want)
				}
				if g.startAcknowledged != (index > 4) {
					t.Fatal("failed Start manufactured acknowledgement", g.startAttempted, g.startAcknowledged, stage)
				}
			})
		}
	}
}
func TestNativeGenerationStartProtocolLostActivationHandleAndCleanupFailureWithholdSuffix(t *testing.T) {
	var events []string
	actions, _ := generationStartScript(t, "", nil, &events)
	actions.activate = func(context.Context, *nativeStartedGeneration) error { events = append(events, "activate"); return nil }
	err := completeNativeGenerationStart(t.Context(), actions)
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || !reflect.DeepEqual(events, generationStartSequence[:6]) {
		t.Fatal("lost activation result reached readiness/cleanup", err, events)
	}
	events = nil
	cause := errors.New("readiness failure")
	actions, _ = generationStartScript(t, "ready", cause, &events)
	cleanupFailure := errors.New("owned cleanup result unavailable")
	actions.cleanup = func(context.Context, *nativeStartedGeneration) error {
		events = append(events, "cleanup")
		return cleanupFailure
	}
	var notices []error
	oldNotice := nativeStartCleanupNotice
	nativeStartCleanupNotice = func(err error) { notices = append(notices, err) }
	defer func() { nativeStartCleanupNotice = oldNotice }()
	err = completeNativeGenerationStart(t.Context(), actions)
	// A failed cleanup is one named warning; the start keeps its plain error
	// and nothing is retried.
	if errors.As(err, &uncertain) || !errors.Is(err, cause) || len(notices) != 1 || !errors.Is(notices[0], cleanupFailure) || !reflect.DeepEqual(events, append(append([]string(nil), generationStartSequence[:7]...), "cleanup")) {
		t.Fatal("failed original cleanup completed, retried or became uncertain", err, events, notices)
	}
}
func TestNativeGenerationStartProtocolCancellationDoesNotInventMutationUncertainty(t *testing.T) {
	for _, stage := range generationStartSequence {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var events []string
			actions, g := generationStartScript(t, "", nil, &events)
			switch stage {
			case "preflight":
				actions.preflight = func(context.Context) error { events = append(events, "preflight"); cancel(); return nil }
			case "stop-previous":
				actions.stopPrevious = func(context.Context) error { events = append(events, "stop-previous"); cancel(); return nil }
			case "prepare":
				actions.prepare = func(context.Context) (*nativeStartedGeneration, error) {
					events = append(events, "prepare")
					cancel()
					return g, nil
				}
			case "before-start":
				actions.beforeStart = func(context.Context, *nativeStartedGeneration) error {
					events = append(events, "before-start")
					cancel()
					return nil
				}
			case "start":
				actions.start = func(context.Context, *nativeStartedGeneration) error {
					events = append(events, "start")
					cancel()
					return nil
				}
			case "activate":
				actions.activate = func(context.Context, *nativeStartedGeneration) error {
					events = append(events, "activate")
					g.activation = &openshell.NativeActivation{}
					cancel()
					return nil
				}
			case "ready":
				actions.ready = func(context.Context, *nativeStartedGeneration) error {
					events = append(events, "ready")
					cancel()
					return nil
				}
			case "forwards":
				actions.forwards = func(context.Context, *nativeStartedGeneration) error {
					events = append(events, "forwards")
					cancel()
					return nil
				}
			}
			err := completeNativeGenerationStart(ctx, actions)
			var uncertain *NativeEffectsUncertain
			if stage == "forwards" {
				if err != nil || events[len(events)-1] == "cleanup" {
					t.Fatal("known successful completion was undone after client cancellation", err, events)
				}
				return
			}
			if !errors.Is(err, context.Canceled) || errors.As(err, &uncertain) {
				t.Fatal("known cancellation became invented mutation uncertainty", stage, err)
			}
			if stage == "preflight" || stage == "stop-previous" {
				if events[len(events)-1] == "cleanup" {
					t.Fatal("preparation-free cancellation invoked cleanup", events)
				}
			} else if events[len(events)-1] != "cleanup" {
				t.Fatal("known prepared/start prefix did not get its one cleanup", events)
			}
		})
	}
}
func TestNativeGenerationStartActualAdapterFailedStartDoesNotInspectCleanOrActivate(t *testing.T) {
	for _, status := range []int{0, 23} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client, log, _ := nativeCleanupAdapterFixture(t, status)
			g, _ := nativeStartedGenerationFixture(t)
			g.startAttempted, g.startAcknowledged = false, false
			script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_NATIVE_CLEANUP_FIXTURE/commands.log"
case "$*" in
 'shell yolo-fixture -- sudo nerdctl start ` + g.cid + `') exit "$YOLOCTL_NATIVE_CLEANUP_STOP_STATUS" ;;
 *) exit 97 ;;
esac
`
			if err := os.WriteFile(filepath.Join(filepath.Dir(log), "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			var events []string
			actions, _ := generationStartScript(t, "", nil, &events)
			actions.prepare = func(context.Context) (*nativeStartedGeneration, error) {
				events = append(events, "prepare")
				return g, nil
			}
			actions.start = func(c context.Context, actual *nativeStartedGeneration) error {
				if actual != g {
					t.Fatal("Start used another prepared CID")
				}
				events = append(events, "start")
				return client.Start(c, actual.cid)
			}
			actions.activate = func(context.Context, *nativeStartedGeneration) error {
				events = append(events, "activate")
				g.activation = &openshell.NativeActivation{}
				return nil
			}
			// Ready/forwards fixtures do not grant authority; no native runtime runs.
			actions.beforeStart = func(context.Context, *nativeStartedGeneration) error {
				events = append(events, "before-start")
				return nil
			}
			actions.cleanup = func(context.Context, *nativeStartedGeneration) error { events = append(events, "cleanup"); return nil }
			err := completeNativeGenerationStart(t.Context(), actions)
			var uncertain *NativeEffectsUncertain
			if status != 0 {
				if err == nil || errors.As(err, &uncertain) || g.startAcknowledged || !reflect.DeepEqual(events, generationStartSequence[:5]) {
					t.Fatal("actual failed Start reply reached cleanup/activation or manufactured ACK", err, events, g.startAcknowledged)
				}
			} else if err != nil || !g.startAcknowledged || !reflect.DeepEqual(events, generationStartSequence) {
				t.Fatal("actual Start ACK lost", err, events)
			}
			commands, err := os.ReadFile(log)
			if err != nil || string(commands) != "shell yolo-fixture -- sudo nerdctl start "+g.cid+"\n" {
				t.Fatal("Start replayed/looked up/removed container", string(commands), err)
			}
		})
	}
}

// A cancelled, already-started engine process returns its exit error (signal:
// killed), not a context sentinel; it may have reached the engine, so the
// Start stays uncertain. A real nonzero engine reply with a live context is a
// plain failure.
func TestNativeGenerationStartCancelledEngineProcessIsLost(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancelled-%t", cancelled), func(t *testing.T) {
			var events []string
			actions, g := generationStartScript(t, "", nil, &events)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			actions.start = func(start context.Context, _ *nativeStartedGeneration) error {
				events = append(events, "start")
				if !cancelled {
					return exec.CommandContext(start, "false").Run()
				}
				cmd := exec.CommandContext(start, "sleep", "10")
				if err := cmd.Start(); err != nil {
					return err
				}
				cancel()
				return cmd.Wait()
			}
			err := completeNativeGenerationStart(ctx, actions)
			var exit *exec.ExitError
			var uncertain *NativeEffectsUncertain
			if !errors.As(err, &exit) || errors.Is(err, context.Canceled) {
				t.Fatal("fixture did not return a plain process exit error", err)
			}
			if errors.As(err, &uncertain) != cancelled || g.startAcknowledged {
				t.Fatal("cancelled started engine process misclassified", cancelled, err)
			}
		})
	}
}
