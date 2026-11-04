package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentprofile"
)

func TestNpmProbeBindsProfileSnapshotAndReusesFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := probeNpmLatest
	t.Cleanup(func() { probeNpmLatest = old })
	calls := 0
	probeNpmLatest = func(context.Context, string) (*agentLatestInfo, error) {
		calls++
		return nil, errors.New("offline\nregistry")
	}
	state := &imageWarmState{probeEnabled: true, snapshot: "one"}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	var needed *npmProbeNeeded
	if err := state.requireNpmProbe(ctx, "codex"); !errors.As(err, &needed) {
		t.Fatal(err)
	}
	if err := state.prepareNpmProbe(ctx, needed); err != nil {
		t.Fatal(err)
	}
	if err := state.requireNpmProbe(ctx, "codex"); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
	profile, err := agentprofile.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparedAgentLatest(ctx, profile); err == nil || err.Error() != "offline registry" {
		t.Fatal("failure result was not reused", err)
	}
	changed := *profile
	changed.NpmPackage = "other-package"
	if _, err := preparedAgentLatest(ctx, &changed); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("profile change reused result", err)
	}
	state.snapshot = "two"
	if _, err := preparedAgentLatest(ctx, profile); err == nil {
		t.Fatal("snapshot change reused result")
	}
	for _, tc := range []struct {
		enabled bool
		agent   string
	}{{false, "codex"}, {true, "none"}, {true, ""}, {true, "agy"}} {
		state.probeEnabled = tc.enabled
		if err := state.requireNpmProbe(ctx, tc.agent); err != nil {
			t.Fatal("unnecessary npm probe", tc, err)
		}
	}
}
func TestCancelledNpmProbeCannotPopulateOrConsumeResult(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := &imageWarmState{probeEnabled: true, snapshot: "snapshot"}
	var needed *npmProbeNeeded
	if err := state.requireNpmProbe(t.Context(), "codex"); !errors.As(err, &needed) {
		t.Fatal(err)
	}
	old := probeNpmLatest
	t.Cleanup(func() { probeNpmLatest = old })
	ctx, cancel := context.WithCancel(t.Context())
	probeNpmLatest = func(context.Context, string) (*agentLatestInfo, error) {
		cancel()
		return &agentLatestInfo{Latest: "1.2.3"}, nil
	}
	if err := state.prepareNpmProbe(ctx, needed); !errors.Is(err, context.Canceled) || len(state.probes) != 0 {
		t.Fatal("cancelled result retained", state.probes, err)
	}
	state.probes = map[string]npmProbeEntry{needed.key: {info: &agentLatestInfo{Latest: "1.2.3"}}}
	ctx = context.WithValue(ctx, warmKey{}, state)
	if _, err := preparedAgentLatest(ctx, &needed.profile); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled apply may use result", err)
	}
}
