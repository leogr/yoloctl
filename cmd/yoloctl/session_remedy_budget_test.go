package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// callerDeadlines are the caller shapes every changed single wait must follow:
// none, a short one, a long one.
var callerDeadlines = []struct {
	name   string
	budget time.Duration
}{{"no deadline", 0}, {"short deadline", 50 * time.Millisecond}, {"long deadline", time.Hour}}

func withCaller(t *testing.T, budget time.Duration) (context.Context, time.Time) {
	t.Helper()
	if budget == 0 {
		return t.Context(), time.Time{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	t.Cleanup(cancel)
	deadline, _ := ctx.Deadline()
	return ctx, deadline
}

// The guidance after a build-mismatch attach resolves under the caller only:
// the 30 s bound is gone, a caller deadline is passed unchanged, and
// cancellation reaches the resolver.
func TestStaleSessionGuidanceFollowsItsCaller(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	for _, tc := range callerDeadlines {
		t.Run(tc.name, func(t *testing.T) {
			ctx, want := withCaller(t, tc.budget)
			var seen time.Time
			var had bool
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{
					ListSpaces: func(ctx context.Context) ([]vmhost.Instance, error) {
						seen, had = ctx.Deadline()
						return nil, errors.New("listing stopped by the test")
					},
				}
			}
			_ = staleSessionGuidance(ctx, "work", "coder", session.ErrBuildMismatch)
			if had != !want.IsZero() || (had && !seen.Equal(want)) {
				t.Fatalf("resolver deadline %v (set %v), want the caller's %v", seen, had, want)
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		newBoxStateResolver = func() boxstate.Resolver {
			return boxstate.Resolver{ListSpaces: func(ctx context.Context) ([]vmhost.Instance, error) {
				cancel()
				<-ctx.Done()
				return nil, ctx.Err()
			}}
		}
		// The fake blocks until the cancellation reaches it, so returning at
		// all proves it did; the evidence is then undetermined.
		err := staleSessionGuidance(ctx, "work", "coder", session.ErrBuildMismatch)
		if err == nil || !errors.Is(err, session.ErrBuildMismatch) || !strings.Contains(err.Error(), "could not be determined") {
			t.Fatalf("cancelled guidance = %v", err)
		}
	})
}

// An ordinary fresh-session guard follows its caller; only controller-owned
// attach setup gets the fail-safe.
func TestFreshSessionGuardFollowsItsCallerOutsideOwnedSetup(t *testing.T) {
	for _, tc := range callerDeadlines {
		t.Run(tc.name, func(t *testing.T) {
			sessionImageFixture(t, "local/recipe:new", nil)
			ctx, want := withCaller(t, tc.budget)
			var seen time.Time
			var had bool
			wrapInspect(t, func(ctx context.Context) error {
				seen, had = ctx.Deadline()
				return nil
			})
			if err := guardFreshSessionImage(ctx, "work", "coder"); err != nil {
				t.Fatal(err)
			}
			if had != !want.IsZero() || (had && !seen.Equal(want)) {
				t.Fatalf("guard deadline %v (set %v), want the caller's %v", seen, had, want)
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		sessionImageFixture(t, "local/recipe:new", nil)
		ctx, cancel := context.WithCancel(t.Context())
		wrapInspect(t, func(ctx context.Context) error { cancel(); <-ctx.Done(); return ctx.Err() })
		if err := guardFreshSessionImage(ctx, "work", "coder"); err == nil || !strings.Contains(err.Error(), "cannot check") {
			t.Fatalf("cancelled guard = %v", err)
		}
	})
}

// In controller-owned attach setup a silent resolution ends at the fail-safe:
// the guard refuses, no launch work starts and no conversation record changes.
func TestFreshSessionGuardOwnedSetupFailSafe(t *testing.T) {
	previous := ownedSetupGuardFailSafe
	t.Cleanup(func() { ownedSetupGuardFailSafe = previous })
	ownedSetupGuardFailSafe = 100 * time.Millisecond
	sessionImageFixture(t, "local/recipe:new", nil)
	wrapInspect(t, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	old := ensureVMBinaryFn
	ensureVMBinaryFn = func(context.Context, *container.Client) error {
		t.Fatal("a guard without evidence reached launch work")
		return nil
	}
	t.Cleanup(func() { ensureVMBinaryFn = old })
	owned := context.WithValue(held(t.Context()), controllerOwnedSetupKey{}, true)
	start := time.Now()
	_, err := ensureAgentSession(owned, container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{})
	if err == nil || !strings.Contains(err.Error(), "no session was started") {
		t.Fatalf("owned guard at its fail-safe = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("owned guard outlived its fail-safe: %v", elapsed)
	}
	if _, exists, err := agentsession.Load("work", "coder"); err != nil || exists {
		t.Fatal("refusal changed the conversation record", exists, err)
	}
}

// wrapInspect replaces the fixture resolver's inspection with one that first
// runs observe on its context.
func wrapInspect(t *testing.T, observe func(context.Context) error) {
	t.Helper()
	fixture := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = fixture })
	newBoxStateResolver = func() boxstate.Resolver {
		r := fixture()
		inspect := r.InspectActual
		r.InspectActual = func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
			if err := observe(ctx); err != nil {
				return agentbox.ActualState{}, err
			}
			return inspect(ctx, cfg)
		}
		return r
	}
}

// Both box.drift views (the post-update restart offer and space upgrade) keep
// the rows resolved within their minute and mark only the remaining boxes
// unavailable; the caller's cancellation still ends the view.
func TestBoxDriftViewsArePartialAfterTheirMinute(t *testing.T) {
	previousBudget, previousResolver, previousTarget := driftViewBudget, resolveStaleSessionsFn, readUpgradeTargetFn
	t.Cleanup(func() {
		driftViewBudget, resolveStaleSessionsFn, readUpgradeTargetFn = previousBudget, previousResolver, previousTarget
	})
	driftViewBudget = 100 * time.Millisecond
	resolveStaleSessionsFn = func(ctx context.Context, _ string, names []string) ([]boxstate.Box, error) {
		// alpha resolves at once; beta's evidence is still being read when
		// the minute ends.
		alpha := upgradeTestBox("alpha", boxstate.RuntimeRunning, "")
		alpha.Drift = boxstate.DriftNeedsRestart
		<-ctx.Done()
		beta := upgradeTestBox("beta", boxstate.RuntimeRunning, "")
		beta.Drift, beta.DriftError = boxstate.DriftError, ctx.Err().Error()
		return []boxstate.Box{alpha, beta}, nil
	}
	readUpgradeTargetFn = func(_ context.Context, b boxstate.Box) (*upgradeTarget, bool, error) {
		return upgradeTestDrift(b, b.Drift).Upgrade, false, nil
	}
	for _, upgrade := range []bool{false, true} {
		start := time.Now()
		view, err := collectBoxDriftView(t.Context(), "test", []string{"alpha", "beta"}, upgrade)
		if err != nil {
			t.Fatalf("upgrade=%v: partial view failed as a whole: %v", upgrade, err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("upgrade=%v: view outlived its minute: %v", upgrade, elapsed)
		}
		if len(view) != 2 || view[0].Name != "alpha" || view[0].Error != "" || view[0].Drift != boxstate.DriftNeedsRestart {
			t.Fatalf("upgrade=%v: resolved row lost: %+v", upgrade, view)
		}
		if r, reason := view[1].remedy(); view[1].Name != "beta" || r != remedyUnknown || !strings.Contains(reason, "deadline exceeded") {
			t.Fatalf("upgrade=%v: unresolved row = %+v (%v %q)", upgrade, view[1], r, reason)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	resolveStaleSessionsFn = func(ctx context.Context, _ string, names []string) ([]boxstate.Box, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := collectBoxDriftView(ctx, "test", []string{"alpha"}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled caller got %v, want its cancellation", err)
	}
}
