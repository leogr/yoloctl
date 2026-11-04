package main

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
)

func TestNativeCallerStopFailureNeverRunsSuffix(t *testing.T) {
	for _, failed := range []string{"", "holder", "forwards", "stop", "state"} {
		t.Run("failure="+failed, func(t *testing.T) {
			var calls []string
			failure := errors.New("original failure")
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					calls = append(calls, name)
					if name == failed {
						return failure
					}
					return nil
				}
			}
			err := completeNativeCallerStop(t.Context(), true, step("holder"), step("forwards"), step("stop"), step("state"))
			want := []string{"holder", "forwards", "stop", "state"}
			if failed != "" {
				want = want[:slices.Index(want, failed)+1]
				if !errors.Is(err, failure) {
					t.Fatalf("original error lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestNativeCallerAlreadyStoppedSkipsEngineStop(t *testing.T) {
	noop := func(context.Context) error { return nil }
	err := completeNativeCallerStop(t.Context(), false, noop, noop,
		func(context.Context) error { t.Fatal("engine Stop repeated"); return nil }, noop)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeCallerStopNeverFallsThroughToLegacyCleanup(t *testing.T) {
	oldNative, oldHolder := lifecycleTryNativeStop, lifecycleStopHolderUnit
	t.Cleanup(func() { lifecycleTryNativeStop, lifecycleStopHolderUnit = oldNative, oldHolder })
	failure := errors.New("native acknowledgement uncertain")
	lifecycleTryNativeStop = func(context.Context, *container.Client, string, bool) (bool, error) { return true, failure }
	lifecycleStopHolderUnit = func(context.Context, *container.Client, string) error {
		t.Fatal("legacy cleanup ran after native selection")
		return nil
	}
	if err := stopAgentboxRuntimeParts(t.Context(), nil, "test", "trial", io.Discard, true, true); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
}

func TestNativeCallerMissingInventoryRowStillCleans(t *testing.T) {
	calls := recordStartFixtureStops(t)
	old := lifecycleTryNativeStop
	t.Cleanup(func() { lifecycleTryNativeStop = old })
	lifecycleTryNativeStop = func(context.Context, *container.Client, string, bool) (bool, error) {
		t.Error("known missing inventory row performed a new Inspect route")
		return false, errors.New("no such container")
	}
	if err := stopSpaceBoxes(t.Context(), "fixture", []boxstate.Box{{Name: "missing"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := []string{"holder", "ports", "session", "netns"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls=%v want=%v", *calls, want)
	}
}

func TestNativeCallerInspectionErrorDoesNotBecomeMissing(t *testing.T) {
	calls := recordStartFixtureStops(t)
	old := lifecycleTryNativeStop
	t.Cleanup(func() { lifecycleTryNativeStop = old })
	failure := errors.New("inspection failed: unavailable runtime inventory")
	lifecycleTryNativeStop = func(context.Context, *container.Client, string, bool) (bool, error) { return false, failure }
	if err := stopAgentboxRuntimeParts(t.Context(), nil, "fixture", "unknown", io.Discard, false, false); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("unproved absence ran cleanup: %v", *calls)
	}
}
