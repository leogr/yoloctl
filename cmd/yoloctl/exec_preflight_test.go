package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func stubExecInventory(t *testing.T, declared bool, status, spaceStatus string, listErr error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "work", "/tmp/def")
	if declared {
		saveCompletionBox(t, "work", "peer")
	}
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: spaceStatus}}, nil
			},
			ListContainers: func(ctx context.Context, _ string) ([]container.Container, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("exec inventory is unbounded")
				}
				if listErr != nil {
					return nil, listErr
				}
				if status == "absent" {
					return nil, nil
				}
				return []container.Container{{Name: "peer", Status: status}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				t.Error("exec preflight dialed a session")
				return boxstate.SessionInfo{}
			},
		}
	}
}
func stubRunningExecTarget(t *testing.T) {
	t.Helper()
	stubExecInventory(t, true, "Up", "Running", nil)
}

func TestExecPreflightRefusesBeforeSSHAndRecordsKnownFailure(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		declared                  bool
		status, spaceStatus, want string
		inventoryErr              error
		invalid                   bool
	}{
		{name: "missing", status: "absent", spaceStatus: "Running", want: `box "peer" does not exist in space "work"`},
		{name: "orphan", status: "Up", spaceStatus: "Running", want: "has no declaration"},
		{name: "stopped", declared: true, status: "Exited (0) 1 minute ago", spaceStatus: "Running", want: `box "peer" is stopped`},
		{name: "container missing", declared: true, status: "absent", spaceStatus: "Running", want: "has no container"},
		{name: "unavailable inventory", declared: true, status: "Up", spaceStatus: "Running", inventoryErr: errors.New("inventory unavailable"), want: "inventory unavailable"},
		{name: "unavailable space", declared: true, status: "Up", spaceStatus: "Stopped", want: "space runtime unavailable"},
		{name: "unknown runtime", declared: true, status: "", spaceStatus: "Running", want: "not known to be running"},
		{name: "paused", declared: true, status: "Paused", spaceStatus: "Running", want: "not known to be running"},
		{name: "invalid declaration", declared: true, status: "Up", spaceStatus: "Running", invalid: true, want: "declaration is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubExecInventory(t, tc.declared, tc.status, tc.spaceStatus, tc.inventoryErr)
			if tc.invalid {
				p, err := layout.BoxPath("work", "peer")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p, []byte("bad: ["), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stubProtectedChatServer(t, false, nil)
			var dials atomic.Int32
			previous := dialOperatorSSH
			t.Cleanup(func() { dialOperatorSSH = previous })
			dialOperatorSSH = func(context.Context, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected SSH dial")
			}
			server := execReceiptServer(t, false)
			req := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "box.exec", Space: "work", Dash: -1}
			err := callChatImageFixture(t.Context(), t.Context(), server, req, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || dials.Load() != 0 {
				t.Fatal("missing preflight refusal", err, dials.Load())
			}
			for _, bad := range []string{"handshake", "socat", "may have run", "not run again"} {
				if strings.Contains(err.Error(), bad) {
					t.Fatal("cryptic or uncertain refusal", err)
				}
			}
			receipt, e := server.Receipts.Read(req.ID)
			if e != nil || receipt.State != "complete" || receipt.Status == 0 || receipt.EffectPhase != controller.EffectWaiting || receipt.Completion != nil {
				t.Fatal("refusal became uncertain or authorized effects", receipt, e)
			}
		})
	}
}

func TestExecSSHSetupFailuresDoNotAuthorizeCommand(t *testing.T) {
	for _, mode := range []string{"socket unavailable", "handshake failed", "channel refused"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &execEndpointFixture{rejectSession: mode == "channel refused"}
			stubExecEndpoint(t, fixture)
			var gateCalls atomic.Int32
			if mode != "channel refused" {
				dialOperatorSSH = func(context.Context, string) (net.Conn, error) {
					if mode == "socket unavailable" {
						return nil, errors.New("connect: no such socket")
					}
					a, b := net.Pipe()
					_ = b.Close()
					return a, nil
				}
			}
			ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
			ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { gateCalls.Add(1); return nil })
			err := execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
			var uncertain *controller.EffectsUncertain
			if err == nil || !strings.Contains(err.Error(), "command was not started") || errors.As(err, &uncertain) || gateCalls.Load() != 0 || fixture.commands.Load() != 0 {
				t.Fatal("setup failure authorized or ambiguously ran command", err, gateCalls.Load(), fixture.commands.Load())
			}
		})
	}
}

func TestExecReleaseFailureSendsNoCommand(t *testing.T) {
	fixture := &execEndpointFixture{closed: make(chan struct{})}
	stubExecEndpoint(t, fixture)
	want := errors.New("role publication failed")
	permitted, released := false, false
	ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { permitted = true; return nil })
	ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error {
		released = true
		if !permitted {
			t.Error("released before command permission")
		}
		return want
	})
	err := execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
	var uncertain *controller.EffectsUncertain
	if !errors.Is(err, want) || errors.As(err, &uncertain) || !permitted || !released || fixture.commands.Load() != 0 {
		t.Fatal(err, permitted, released, fixture.commands.Load())
	}
	select {
	case <-fixture.closed:
	case <-time.After(time.Second):
		t.Fatal("failed release leaked endpoint")
	}
}

func TestExecLostStartReplyRemainsUncertain(t *testing.T) {
	fixture := &execEndpointFixture{loseStartReply: true}
	stubExecEndpoint(t, fixture)
	server := execReceiptServer(t, false)
	req := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "box.exec", Space: "work", Dash: -1}
	err := callChatImageFixture(t.Context(), t.Context(), server, req, nil)
	if err == nil {
		t.Fatal("lost exec reply reported success")
	}
	receipt, e := server.Receipts.Read(req.ID)
	if e != nil || receipt.State != "interrupted" || receipt.EffectPhase != controller.EffectStarted || fixture.commands.Load() != 1 {
		t.Fatal("lost request reply was declared not run", receipt, e, fixture.commands.Load())
	}
	if err := callChatImageFixture(t.Context(), t.Context(), server, req, nil); err == nil || fixture.commands.Load() != 1 {
		t.Fatal("uncertain request replayed", err, fixture.commands.Load())
	}
}

func TestExecHostMissingBoxRefusesBeforeProxy(t *testing.T) {
	stubExecInventory(t, false, "absent", "Running", nil)
	stubProtectedChatServer(t, false, nil)
	permitted := false
	ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { permitted = true; return nil })
	err := execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
	if err == nil || !strings.Contains(err.Error(), `box "peer" does not exist in space "work"`) || permitted {
		t.Fatal("host exec bypassed preflight", err, permitted)
	}
}
