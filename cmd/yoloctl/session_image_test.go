package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
)

// Use the production FullDiff resolver and image comparison, not a stubbed
// stale decision. The inventory and inspected container are isolated fixtures.
func sessionImageFixture(t *testing.T, actual string, inspectErr error) *identityFixture {
	t.Helper()
	cfg := claudeBox()
	f := sessionViewFixture(t, cfg)
	original := newBoxStateResolver
	newBoxStateResolver = func() boxstate.Resolver {
		r := original()
		r.ProbeSession = func(context.Context, string, string, bool) boxstate.SessionInfo {
			return boxstate.SessionInfo{State: boxstate.SessionNone}
		}
		r.InspectActual = func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
			return agentbox.ActualState{Exists: true, Image: actual, ReadOnlyWD: true}, inspectErr
		}
		r.ComposePolicy = func(*agentbox.Config) ([]byte, error) { return nil, nil }
		r.ResolvedImage = func(*agentbox.Config) (string, bool, error) { return "local/recipe:new", false, nil }
		return r
	}
	return f
}

func TestFreshSessionImageDriftRefusesBeforeLaunchOrRecordChange(t *testing.T) {
	sessionImageFixture(t, "local/recipe:old", nil)
	old := ensureVMBinaryFn
	ensureVMBinaryFn = func(context.Context, *container.Client) error {
		t.Fatal("stale image reached binary/launch work")
		return nil
	}
	t.Cleanup(func() { ensureVMBinaryFn = old })
	_, err := ensureAgentSession(held(t.Context()), container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{})
	if err == nil {
		t.Fatal("stale image admitted fresh session")
	}
	for _, want := range []string{"image: local/recipe:old -> local/recipe:new", "yoloctl box inspect coder --space work", "yoloctl box recreate coder --space work --no-attach", "other files in the container are lost"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
	if _, exists, err := agentsession.Load("work", "coder"); err != nil || exists {
		t.Fatal("refusal changed conversation record", exists, err)
	}
}
func TestFreshSessionSameImageAndUnknownEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, actual string
		inspectErr   error
		want         string
	}{
		{"same image", "local/recipe:new", nil, ""},
		{"inspection unavailable", "local/recipe:old", errors.New("inventory unavailable"), "cannot check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionImageFixture(t, tc.actual, tc.inspectErr)
			err := guardFreshSessionImage(t.Context(), "work", "coder")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "box recreate") {
				t.Fatal("unproved observation labelled stale", err)
			}
		})
	}
}
func TestLiveHolderSkipsFreshSessionImageGuard(t *testing.T) {
	f := sessionImageFixture(t, "local/recipe:old", errors.New("should not inspect"))
	f.unit["coder"] = true
	if _, err := ensureAgentSession(held(t.Context()), container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
}
func TestRealExit137IsNotRewrittenAsImageDrift(t *testing.T) {
	old := resolveStaleSessionsFn
	resolveStaleSessionsFn = func(context.Context, string, []string) ([]boxstate.Box, error) {
		t.Fatal("exit137 triggered build-mismatch classification")
		return nil, nil
	}
	t.Cleanup(func() { resolveStaleSessionsFn = old })
	cause := &session.ExitError{Status: 137}
	if got := staleSessionGuidance(t.Context(), "work", "coder", cause); got != cause {
		t.Fatal(got)
	}
}
