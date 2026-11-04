package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
)

func TestNativeStopUncertaintyPrecedesLegacyGoneCleanup(t *testing.T) {
	for _, text := range []string{"no such container", "not found"} {
		t.Run(text, func(t *testing.T) {
			for _, uncertain := range []bool{false, true} {
				t.Run(map[bool]string{false: "legacy-gone-control", true: "native-unknown"}[uncertain], func(t *testing.T) {
					calls := recordStartFixtureStops(t)
					cause := errors.New(text)
					var failure = cause
					if uncertain {
						failure = fmt.Errorf("wrapped stop: %w", &agentbox.NativeEffectsUncertain{Cause: cause})
					}
					if isContainerGone(failure) == uncertain {
						t.Fatal("gone classifier discarded typed uncertainty or legacy control", failure)
					}
					lifecycleStopAgentboxContainer = func(context.Context, *container.Client, string) error {
						*calls = append(*calls, "container")
						return failure
					}
					events, status, err := runCompletionWrapper(t, func(cmd *cobra.Command) error {
						return stopAgentboxRuntimeParts(cmd.Context(), container.New("fixture"), "fixture", "coder", io.Discard, true, true)
					}, nil, false)
					want := []string{"holder", "ports", "container"}
					if !uncertain {
						want = append(want, "session")
					}
					if !errors.Is(err, cause) || workerEffectsUncertain(err) != uncertain || !slices.Equal(*calls, want) {
						t.Fatal("unknown stop reached session cleanup/reap or changed legacy control", *calls, err)
					}
					if uncertain && (len(status) != 0 || !slices.Equal(events, []string{"write", "role-sync"})) {
						t.Fatal("native uncertainty published worker completion", events, status, err)
					}
					if !uncertain && len(status) != 1 {
						t.Fatal("known legacy outcome lost completion", events, status, err)
					}
				})
			}
		})
	}
}

// A concrete inner applier supplies the optional preflight; fake appliers in
// unrelated tests keep their existing surface. This test exercises the actual
// managed caller's ordering before holder/session cleanup.
type nativeRestartPreflightInner struct {
	*fakeManagedInner
	failure error
	checked bool
}

func (a *nativeRestartPreflightInner) PreflightRestart(context.Context, *agentbox.Config) error {
	a.checked = true
	return a.failure
}
func TestManagedApplierRejectsDeferredNativeRestartBeforeSessionEffects(t *testing.T) {
	failure := errors.New("native restart needs fresh generation")
	inner := &nativeRestartPreflightInner{fakeManagedInner: &fakeManagedInner{}, failure: failure}
	a := managedApplier{inner: inner, cleanup: func(context.Context, *agentbox.Config) (bool, error) {
		t.Fatal("session ended before native restart preflight")
		return false, nil
	}}
	err := a.Restart(t.Context(), &agentbox.Config{Name: "coder"}, nil)
	if !errors.Is(err, failure) || !inner.checked || len(inner.calls) != 0 {
		t.Fatal("native restart refusal did not precede effects", err, inner.calls)
	}
}
