package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
	if err := state.requireNpmProbe("codex"); !errors.As(err, &needed) {
		t.Fatal(err)
	}
	if err := state.prepareNpmProbe(ctx, needed); err != nil {
		t.Fatal(err)
	}
	if err := state.requireNpmProbe("codex"); err != nil || calls != 1 {
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
		if err := state.requireNpmProbe(tc.agent); err != nil {
			t.Fatal("unnecessary npm probe", tc, err)
		}
	}
}
func TestCancelledNpmProbeCannotPopulateOrConsumeResult(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := &imageWarmState{probeEnabled: true, snapshot: "snapshot"}
	var needed *npmProbeNeeded
	if err := state.requireNpmProbe("codex"); !errors.As(err, &needed) {
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

func TestPreparedRegistryWaitIsOptional(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := probeNpmLatest
	t.Cleanup(func() { probeNpmLatest = old })
	for _, tc := range []struct {
		name  string
		delay time.Duration
	}{
		{name: "after old deadline", delay: 6 * time.Second},
		{name: "silent registry", delay: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := &imageWarmState{probeEnabled: true, snapshot: "snapshot"}
				ctx := context.WithValue(t.Context(), warmKey{}, state)
				var needed *npmProbeNeeded
				if err := state.requireNpmProbe("codex"); !errors.As(err, &needed) {
					t.Fatal(err)
				}
				probeNpmLatest = func(ctx context.Context, _ string) (*agentLatestInfo, error) {
					if tc.delay == 0 {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					select {
					case <-time.After(tc.delay):
						return &agentLatestInfo{Latest: "1.2.3"}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				start := time.Now()
				if err := state.prepareNpmProbe(ctx, needed); err != nil || ctx.Err() != nil {
					t.Fatal("optional discovery failed the operation", err, ctx.Err())
				}
				info, err := preparedAgentLatest(ctx, &needed.profile)
				if tc.delay == 0 {
					if time.Since(start) != time.Minute || err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
						t.Fatal("silent registry did not become a recorded skip", time.Since(start), info, err)
					}
				} else if err != nil || info == nil || info.Latest != "1.2.3" || time.Since(start) != tc.delay {
					t.Fatal("late reply was lost", time.Since(start), info, err)
				}
			})
		})
	}
}
