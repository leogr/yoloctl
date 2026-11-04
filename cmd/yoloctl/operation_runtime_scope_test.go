package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func runtimeAccountFixture(t *testing.T) (*agentbox.Config, container.Container) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "work", "/work")
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "dev", Space: "work", Agent: "claude", Account: "desired", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("dev")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	oldResolver, oldRoles, oldPorts := newBoxStateResolver, chatRoleBoxes, footprintForwards
	t.Cleanup(func() { newBoxStateResolver, chatRoleBoxes, footprintForwards = oldResolver, oldRoles, oldPorts })
	footprintForwards = func(context.Context, string, string) ([]port.Spec, error) { return nil, nil }
	chatRoleBoxes = func() (map[string]bool, error) { return map[string]bool{}, nil }
	ctr := container.Container{ID: "0123456789ab", Name: "dev", Status: "Up", Labels: runtimeAccountLabels("claude", "actual")}
	return cfg, ctr
}

func runtimeAccountLabels(agent, account string) string {
	return fmt.Sprintf("%s=work,%s=dev,%s=%s,%s=%s", agentbox.LabelSpace, agentbox.LabelAgentbox, agentbox.LabelAgent, agent, agentbox.LabelAccount, account)
}

func runtimeAccountResolver(t *testing.T, containers []container.Container, inventoryErr error) func() boxstate.Resolver {
	t.Helper()
	return func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(ctx context.Context, _ string) ([]container.Container, error) {
				if _, bounded := ctx.Deadline(); !bounded {
					t.Error("runtime footprint read has no deadline")
				}
				return containers, inventoryErr
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				t.Error("footprint inventory probed a session")
				return boxstate.SessionInfo{}
			},
		}
	}
}

func TestBoxFootprintIncludesActualAndDesiredAccounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*container.Container)
		absent bool
		err    error
		global bool
		actual bool
	}{
		{name: "failed repoint", actual: true},
		{name: "stopped failed repoint", actual: true, change: func(c *container.Container) { c.Status = "Exited (1)" }},
		{name: "verified absence", absent: true},
		{name: "unavailable inventory", err: errors.New("inventory unavailable"), global: true},
		{name: "missing account", global: true, change: func(c *container.Container) { c.Labels = runtimeAccountLabels("claude", "") }},
		{name: "invalid account", global: true, change: func(c *container.Container) { c.Labels = runtimeAccountLabels("claude", "../bad") }},
		{name: "real agent with none", global: true, change: func(c *container.Container) { c.Labels = runtimeAccountLabels("claude", "none") }},
		{name: "missing agent", global: true, change: func(c *container.Container) { c.Labels = runtimeAccountLabels("", "actual") }},
		{name: "missing identity", global: true, change: func(c *container.Container) { c.ID = "" }},
		{name: "foreign space", global: true, change: func(c *container.Container) { c.Labels = strings.Replace(c.Labels, "=work", "=other", 1) }},
		{name: "verified agentless", change: func(c *container.Container) { c.Labels = runtimeAccountLabels("none", "none") }},
		{name: "legacy agentless omission", change: func(c *container.Container) {
			c.Labels = strings.TrimSuffix(runtimeAccountLabels("none", ""), ","+agentbox.LabelAccount+"=")
		}},
		{name: "conflicting agentless", global: true, change: func(c *container.Container) { c.Labels = runtimeAccountLabels("none", "actual") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ctr := runtimeAccountFixture(t)
			if tc.change != nil {
				tc.change(&ctr)
			}
			containers := []container.Container{ctr}
			if tc.absent {
				containers = nil
			}
			newBoxStateResolver = runtimeAccountResolver(t, containers, tc.err)
			release, err := spacelock.Acquire(t.Context(), "work", true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.Flags().String("account", "requested", "")
			for _, input := range []struct {
				path string
				args []string
			}{
				{"box start", []string{"dev"}}, {"port forward", []string{"dev", "8080"}}, {"box copy", []string{"dev", "copy"}},
			} {
				intent := spaceOperationIntent(cmd, input.path, "work", input.args)
				if intent.Scope.Global != tc.global {
					t.Fatalf("%s: global=%v want %v", input.path, intent.Scope.Global, tc.global)
				}
				if tc.global {
					continue
				}
				for _, account := range []string{"desired", "requested"} {
					if !slices.Contains(intent.Scope.Reads, accountResource(account)) {
						t.Fatalf("%s omitted %s: %+v", input.path, account, intent.Scope)
					}
				}
				if slices.Contains(intent.Scope.Reads, accountResource("actual")) != tc.actual {
					t.Fatalf("%s actual account: %+v", input.path, intent.Scope)
				}
				for _, w := range intent.Scope.Writes {
					if w.Kind == controller.ResourceAccount {
						t.Fatalf("box usage held an account as a write: %+v", intent.Scope)
					}
				}
			}
			cfg.Account = "requested"
			if intent := boxEditIntent(t.Context(), "work", cfg); intent.Scope.Global != tc.global || !tc.global && slices.Contains(intent.Scope.Reads, accountResource("actual")) != tc.actual {
				t.Fatalf("document apply actual account: %+v", intent.Scope)
			}
		})
	}
}

func TestFailedRepointStillConflictsWithActualAccountHold(t *testing.T) {
	_, ctr := runtimeAccountFixture(t)
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	const disk = "0123456789abcdef0123456789abcdef"
	s := &controller.Server{Hello: controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "test-bundle", Space: "work", Generation: "test-generation"}, DiskID: disk, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: controllerOperations}
	if err := s.Receipts.Save(controller.Receipt{ID: "held-account-0123456789", Method: "account.auth.import", State: "uncertain", Started: time.Now().UTC(), ProofVersion: 1, DiskID: disk, EffectPhase: controller.EffectStarted, Scope: &controller.Scope{Writes: []controller.Resource{accountResource("actual")}}}); err != nil {
		t.Fatal(err)
	}
	var ran bool
	s.Execute = func(ctx context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
		release, err := spacelock.Acquire(ctx, "work", true)
		if err != nil {
			return 2, err
		}
		defer release()
		cmd := &cobra.Command{}
		cmd.SetContext(ctx)
		if err := streams.BeginEffects(ctx, spaceOperationIntent(cmd, strings.ReplaceAll(req.Method, ".", " "), "work", req.Args)); err != nil {
			return 2, err
		}
		ran = true
		return 0, nil
	}
	for _, tc := range []struct {
		method, target string
		allowed        bool
	}{
		{"box.start", "dev", false}, {"account.secret.set", "desired", true}, {"account.secret.set", "other", true},
	} {
		ran = false
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); s.ServeConn(t.Context(), server, controller.Principal{}) }()
		req := controller.Request{ID: "scope-request-" + tc.target, Method: tc.method, Space: "work", Args: []string{tc.target}, Dash: -1}
		err := controller.Call(t.Context(), client, s.Hello, req, controller.ClientIO{Out: io.Discard, Err: io.Discard})
		<-done
		if (err == nil) != tc.allowed || ran != tc.allowed {
			t.Fatalf("%s %s: err=%v ran=%v", tc.method, tc.target, err, ran)
		}
	}
}
