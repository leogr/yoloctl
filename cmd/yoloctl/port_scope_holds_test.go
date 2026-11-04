package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/spf13/cobra"
)

func TestPortFootprintsUseActualForwardsAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actual []port.Spec
		err    error
	}{
		{"verified empty", nil, nil},
		{"saved empty with actual forward", []port.Spec{{Agentbox: "dev", HostPort: 8080, ContainerPort: 8080}}, nil},
		{"inventory unavailable", nil, errors.New("supervisor unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ctr := runtimeAccountFixture(t) // desired Ports is empty
			newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
			footprintForwards = func(ctx context.Context, _, _ string) ([]port.Spec, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("port inventory was unbounded")
				}
				return tc.actual, tc.err
			}
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			for _, path := range []string{"box stop", "box start", "box restart", "box recreate", "box attach", "box set"} {
				intent := spaceOperationIntent(cmd, path, "work", []string{"dev"})
				if intent.Scope.Global != (tc.err != nil) || tc.err == nil && slices.Contains(intent.Scope.Writes, portsDomain) != (len(tc.actual) != 0) {
					t.Fatalf("%s: %+v", path, intent.Scope)
				}
			}
		})
	}
}

func TestCopiedSourceAndForcedAccountConsumersRetainPortDomain(t *testing.T) {
	cfg, ctr := runtimeAccountFixture(t)
	cfg.Ports = []agentbox.Port{{Published: 8080, Target: 8080}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	// accountConsumers' complete runtime snapshot is independently bounded.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	copyIntent := spaceOperationIntent(cmd, "box copy", "work", []string{"dev", "copy"})
	if copyIntent.Scope.Global || !slices.Contains(copyIntent.Scope.Writes, portsDomain) {
		t.Fatal("copy omitted its source declaration's ports", copyIntent.Scope)
	}
	footprintForwards = func(context.Context, string, string) ([]port.Spec, error) {
		return []port.Spec{{Agentbox: "dev", HostPort: 8080, ContainerPort: 8080}}, nil
	}
	destroy := spaceOperationIntent(cmd, "account destroy", "work", []string{"actual"})
	if destroy.Scope.Global || !slices.Contains(destroy.Scope.Writes, portsDomain) || !slices.Contains(destroy.Scope.Writes, boxResource("dev")) {
		t.Fatal("forced account consumer teardown omitted actual forwards", destroy.Scope)
	}
}

func TestInterruptedTeardownRetainsActualPortHold(t *testing.T) {
	_, ctr := runtimeAccountFixture(t)
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	footprintForwards = func(context.Context, string, string) ([]port.Spec, error) {
		return []port.Spec{{Agentbox: "dev", HostPort: 8080, ContainerPort: 8080}}, nil
	}
	const disk = "0123456789abcdef0123456789abcdef"
	s := &controller.Server{Hello: controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}, DiskID: disk, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: controllerOperations}
	s.Execute = func(ctx context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
		cmd := &cobra.Command{}
		cmd.SetContext(ctx)
		if err := streams.BeginEffects(ctx, spaceOperationIntent(cmd, "box stop", "work", req.Args)); err != nil {
			return 2, err
		}
		return 2, &controller.WorkerInterrupted{Cause: errors.New("terminated during actual forward teardown")}
	}
	req := controller.Request{ID: "port-teardown-request-0123456789", Method: "box.stop", Space: "work", Args: []string{"dev"}, Dash: -1}
	if err := callScopeFixture(t, s, req); err == nil {
		t.Fatal("interruption reported success")
	}
	r, err := s.Receipts.Read(req.ID)
	if err != nil || r.State != "uncertain" || !slices.Contains(r.Scope.Writes, portsDomain) {
		t.Fatal("interrupted teardown lost its actual-port hold", r, err)
	}
	// A future port writer intersects the durable hold even on another box.
	s.Execute = func(ctx context.Context, _ controller.Principal, _ controller.Request, streams controller.Streams) (int, error) {
		if err := streams.BeginEffects(ctx, controller.EffectIntent{Scope: &controller.Scope{Writes: []controller.Resource{boxResource("api"), portsDomain}}}); err != nil {
			return 2, err
		}
		return 0, nil
	}
	req.ID, req.Method = "port-next-request-0123456789", "port.forward"
	if err := callScopeFixture(t, s, req); err == nil {
		t.Fatal("a future writer bypassed the interrupted port hold")
	}
}
