package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func TestBoxCacheClearRequiresKnownStoppedRuntime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		runtime   boxstate.RuntimeState
		readErr   error
		decl      boxstate.DeclarationState
		wantClear bool
	}{
		{"running", boxstate.RuntimeRunning, nil, boxstate.DeclarationDeclared, false},
		{"stopped", boxstate.RuntimeStopped, nil, boxstate.DeclarationDeclared, true},
		{"absent runtime", boxstate.RuntimeMissing, nil, boxstate.DeclarationDeclared, true},
		{"unknown", boxstate.RuntimeUnknown, nil, boxstate.DeclarationDeclared, false},
		{"unavailable", boxstate.RuntimeUnavailable, nil, boxstate.DeclarationDeclared, false},
		{"failed inventory", boxstate.RuntimeStopped, errors.New("inventory failed"), boxstate.DeclarationDeclared, false},
		{"missing declaration", boxstate.RuntimeMissing, nil, boxstate.DeclarationMissing, false},
		{"unknown declaration", boxstate.RuntimeStopped, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			n, err := clearStoppedBoxCache(t.Context(), "work", "coder", func(context.Context, string, string) (boxstate.Box, error) {
				box := boxstate.Box{Declaration: tc.decl, Runtime: tc.runtime}
				if tc.runtime == boxstate.RuntimeStopped {
					box.Container = &container.Container{Name: "coder", Status: "Exited (0) 1 minute ago"}
				}
				return box, tc.readErr
			}, func(context.Context, string) (uint64, error) { calls++; return 123, nil })
			if tc.wantClear {
				if err != nil || calls != 1 || n != 123 {
					t.Fatal(n, err, calls)
				}
				return
			}
			if err == nil || calls != 0 || n != 0 {
				t.Fatal("unproved runtime allowed deletion", n, err, calls)
			}
			if tc.runtime == boxstate.RuntimeRunning && !strings.Contains(err.Error(), "stop it first") {
				t.Fatal("running refusal omits remedy", err)
			}
		})
	}
}

func TestBoxCacheClearUsesOrdinaryBoxAuthorizationAndEffectGate(t *testing.T) {
	root := newClientRootCmd()
	cmd, _, err := root.Find([]string{"box", "cache", "clear"})
	if err != nil || commandMethod(cmd) != "box.cache.clear" {
		t.Fatal("missing shared command", err)
	}
	op := controllerOperations["box.cache.clear"]
	if !op.Mutates || !op.BoxAllowed || !op.EffectGated || op.Footprint != controller.FootprintBox {
		t.Fatal("clear bypasses ordinary mutation authority", op)
	}
	if !wrapperGatesEffects("box cache clear") {
		t.Fatal("cache clear bypasses the effect gate")
	}
	cmd.SetContext(t.Context())
	intent := spaceOperationIntent(cmd, "box cache clear", "work", []string{"coder"})
	if intent.Scope == nil || intent.Scope.Global || len(intent.Scope.Writes) != 1 || intent.Scope.Writes[0] != boxResource("coder") {
		t.Fatal("wrong cache clear footprint", intent)
	}
}

func TestRecreateCacheFlagIsNeverImplied(t *testing.T) {
	cmd := newAgentboxRecreateCmd()
	cmd.SetContext(t.Context())
	if agentbox.RecreateCacheClearRequested(recreateCacheContext(cmd, "coder")) {
		t.Fatal("default recreates clear cache")
	}
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	if agentbox.RecreateCacheClearRequested(recreateCacheContext(cmd, "coder")) {
		t.Fatal("force implies clear cache")
	}
	if err := cmd.Flags().Set("clear-cache", "true"); err != nil {
		t.Fatal(err)
	}
	if !agentbox.RecreateCacheClearRequested(recreateCacheContext(cmd, "coder")) {
		t.Fatal("explicit flag lost")
	}
	offer := &cobra.Command{}
	offer.SetContext(t.Context())
	if agentbox.RecreateCacheClearRequested(recreateCacheContext(offer, "coder")) {
		t.Fatal("directive56 offer implies clear cache")
	}
}

func TestBoxCacheClearHoldsNativeGuardWithoutChatRoleEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := syncNativeChatRolesFn
	t.Cleanup(func() { syncNativeChatRolesFn = old })
	syncNativeChatRolesFn = func(context.Context, string) error { t.Fatal("cache clear changed chat roles"); return nil }
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	cache := newAgentboxCacheCmd()
	box.AddCommand(cache)
	root.AddCommand(box)
	cmd, _, err := root.Find([]string{"box", "cache", "clear"})
	if err != nil {
		t.Fatal(err)
	}
	gated, ran := false, false
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ran = true
		if !gated || !spaceOperationHeld(cmd.Context()) {
			t.Fatal("cache removal ran outside native guard/effect permission")
		}
		return nil
	}
	registerSpaceOperations(root)
	ctx := context.WithValue(t.Context(), controllerRequestKey{}, "request-0123456789abcdef")
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { gated = true; return nil })
	root.SetArgs([]string{"box", "cache", "clear", "coder", "--space", "work"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("cache clear was not dispatched")
	}
}

func TestBoxCacheClearUsesAffirmativeActualInventory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedSetBox(t)
	for _, status := range []string{"", "Unknown", "Paused", "Restarting", "Exited ()", "Exited (x)", "Stopped but unknown", "Created", "Exited (0) 1 minute ago", "Stopped"} {
		t.Run("status="+status, func(t *testing.T) {
			r := boxstate.NewResolver()
			r.ListSpaces = func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "default", Status: "Running"}}, nil
			}
			r.ListContainers = func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{ID: "fixture", Name: "coder", Status: status}}, nil
			}
			calls := 0
			_, err := clearStoppedBoxCache(t.Context(), "default", "coder", func(ctx context.Context, space, name string) (boxstate.Box, error) {
				return r.ResolveOne(ctx, space, name, boxstate.RuntimeInventory)
			}, func(context.Context, string) (uint64, error) { calls++; return 123, nil })
			want := status == "Created" || status == "Exited (0) 1 minute ago" || status == "Stopped"
			if want {
				if err != nil || calls != 1 {
					t.Fatal("known stopped observation refused", err, calls)
				}
			} else if err == nil || calls != 0 {
				t.Fatal("unknown actual observation authorized deletion", err, calls)
			}
		})
	}
}
