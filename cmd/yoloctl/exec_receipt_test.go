package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func TestBoxExecRegistrationGatesCommandEffects(t *testing.T) {
	// internal/controller's real-worker exec regressions use this exact policy.
	want := controller.Operation{BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox}
	if got := controllerOperations["box.exec"]; got != want {
		t.Fatalf("box.exec policy %+v, want %+v", got, want)
	}
}

// execEndpointFixture is a confined SSH endpoint that counts commands. It
// reports exit, sends no exit status when exit is negative, or holds the
// command until its transport closes.
type execEndpointFixture struct {
	config   *ssh.ServerConfig
	commands atomic.Int32
	dials    atomic.Int32
	exit     int
	hold     bool
	running  chan struct{}
}

func (f *execEndpointFixture) dial() (net.Conn, error) {
	f.dials.Add(1)
	exit, hold := f.exit, f.hold
	// SOCK_CLOEXEC is Linux-only; mark each descriptor separately.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	conns := make([]net.Conn, 2)
	for i, fd := range fds {
		unix.CloseOnExec(fd)
		file := os.NewFile(uintptr(fd), "exec-endpoint")
		conns[i], err = net.FileConn(file)
		_ = file.Close()
		if err != nil {
			return nil, err
		}
	}
	go func() {
		defer func() { _ = conns[1].Close() }()
		server, channels, requests, err := ssh.NewServerConn(conns[1], f.config)
		if err != nil {
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			ch, requests, err := channel.Accept()
			if err != nil {
				return
			}
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				f.commands.Add(1)
				_ = req.Reply(true, nil)
				if hold {
					close(f.running)
					_ = server.Wait()
					return
				}
				if exit >= 0 {
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
				}
				_ = ch.Close()
				break
			}
		}
	}()
	return conns[0], nil
}

// stubExecEndpoint routes an operator exec to fixture with no terminal input.
func stubExecEndpoint(t *testing.T, fixture *execEndpointFixture) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	fixture.config = &ssh.ServerConfig{NoClientAuth: true}
	fixture.config.AddHostKey(signer)
	stubProtectedChatServer(t, false, nil)
	previous := dialOperatorSSH
	dialOperatorSSH = func(context.Context, string) (net.Conn, error) { return fixture.dial() }
	input, closed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	stdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { dialOperatorSSH, os.Stdin = previous, stdin; _ = input.Close() })
}

// execReceiptServer uses the real wire, admission, receipts and production
// registry. Its trusted handler runs the shared exec path with the worker's
// gate and release seams, then reports the outcome the way the worker does.
func execReceiptServer(t *testing.T, loseLifecycle bool) *controller.Server {
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}
	server := &controller.Server{Hello: hello, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: map[string]controller.Operation{
		"box.exec": controllerOperations["box.exec"], "box.create": controllerOperations["box.create"], "box.list": controllerOperations["box.list"],
	}}
	server.Execute = func(run context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
		switch req.Method {
		case "box.list":
			return 0, nil
		case "box.create":
			if loseLifecycle {
				// Permitted without a bounded footprint, then lost.
				if err := streams.BeginEffects(run, controller.EffectIntent{}); err != nil {
					return 2, err
				}
				return 2, &controller.WorkerInterrupted{Cause: errors.New("lifecycle worker lost after its effect")}
			}
			return 0, nil
		}
		ctx := withControllerPrincipal(run, controller.Principal{Box: "caller", Incarnation: "live"})
		ctx = controller.WithEffectGate(ctx, streams.BeginEffects)
		ctx = context.WithValue(ctx, controllerReleaseKey{}, streams.Release)
		err := execInAgentboxInteractive(ctx, "work", "peer", []string{"marker"})
		var uncertain *controller.EffectsUncertain
		if errors.As(err, &uncertain) {
			return classifyError(err), err // the worker's private uncertain checkpoint
		}
		return classifyError(err), nil
	}
	return server
}

func TestExecLostResultIsQueryableWithoutFencingTheSpace(t *testing.T) {
	for _, mode := range []string{"known zero", "known nonzero", "missing exit status", "client lost"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			fixture := &execEndpointFixture{running: make(chan struct{})}
			switch mode {
			case "known nonzero":
				fixture.exit = 3
			case "missing exit status":
				fixture.exit = -1
			case "client lost":
				fixture.hold = true
			}
			stubExecEndpoint(t, fixture)
			server := execReceiptServer(t, false)
			req := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "box.exec", Space: "work", Dash: -1}
			clientCtx, stopClient := context.WithCancel(ctx)
			defer stopClient()
			callDone := make(chan error, 1)
			go func() { callDone <- callChatImageFixture(clientCtx, ctx, server, req, nil) }()
			if mode == "client lost" {
				select {
				case <-fixture.running:
					stopClient() // the TUI closes while the command runs
				case err := <-callDone:
					t.Fatal("command ended before it was running", err)
				case <-ctx.Done():
					t.Fatal("command never started")
				}
			}
			err := <-callDone
			var exit *controller.ExitError
			var unknown *controller.OutcomeUnknown
			wantState, wantStatus := "complete", 0
			switch mode {
			case "known zero":
				if err != nil {
					t.Fatal(err)
				}
			case "known nonzero":
				wantStatus = 3
				if !errors.As(err, &exit) || exit.Status != 3 {
					t.Fatal("remote exit status was not preserved", err)
				}
			case "missing exit status":
				wantState = "command-uncertain"
				if !errors.As(err, &exit) || exit.Status == 0 {
					t.Fatal("missing remote status became a known success", err)
				}
			case "client lost":
				wantState = "command-uncertain"
				if !errors.As(err, &unknown) || unknown.ID != req.ID {
					t.Fatal("client loss reported a known result", err)
				}
			}
			receipt, err := server.Receipts.Read(req.ID)
			if err != nil || receipt.State != wantState || receipt.EffectPhase != controller.EffectStarted || server.RecoveryRequired {
				t.Fatal(receipt, server.RecoveryRequired, err)
			}
			if wantState == "complete" && receipt.Status != wantStatus {
				t.Fatal("known command status changed", receipt.Status)
			}
			if fixture.commands.Load() != 1 {
				t.Fatal("command count", fixture.commands.Load())
			}
			fixture.hold, fixture.exit = false, 0
			if err := callChatImageFixture(ctx, ctx, server, req, nil); err == nil || fixture.commands.Load() != 1 || fixture.dials.Load() != 1 {
				t.Fatal("recorded exec request ran again", err)
			}
			next := controller.Request{ID: "fedcba9876543210fedcba9876543210", Method: "box.create", Space: "work", Dash: -1}
			if err := callChatImageFixture(ctx, ctx, server, next, nil); err != nil {
				t.Fatal("unknown command result fenced a lifecycle mutation", err)
			}
			// A separate command runs without acknowledging the earlier one.
			req.ID = "00112233445566778899aabbccddeeff"
			if err := callChatImageFixture(ctx, ctx, server, req, nil); err != nil || fixture.commands.Load() != 2 {
				t.Fatal("unrelated exec was refused", err)
			}
		})
	}
}

func TestExecStillWaitsForStructuralRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixture := &execEndpointFixture{}
	stubExecEndpoint(t, fixture)
	server := execReceiptServer(t, true)
	create := controller.Request{ID: "fedcba9876543210fedcba9876543210", Method: "box.create", Space: "work", Dash: -1}
	_ = callChatImageFixture(ctx, ctx, server, create, nil)
	if receipt, err := server.Receipts.Read(create.ID); err != nil || receipt.State != "uncertain" || !server.RecoveryRequired {
		t.Fatal(receipt, server.RecoveryRequired, err)
	}
	req := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "box.exec", Space: "work", Dash: -1}
	if err := callChatImageFixture(ctx, ctx, server, req, nil); err == nil || fixture.dials.Load() != 0 || fixture.commands.Load() != 0 {
		t.Fatal("exec reached its endpoint behind an unreviewed lifecycle outcome", err)
	}
	list := controller.Request{ID: "00112233445566778899aabbccddeeff", Method: "box.list", Space: "work", Dash: -1}
	if err := callChatImageFixture(ctx, ctx, server, list, nil); err != nil {
		t.Fatal("reads blocked by recovery", err)
	}
}

func TestExecDeniedPermissionClosesEndpointBeforeCommand(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	selected := &chatReleaseTransport{Conn: local}
	previous := dialOperatorSSH
	dialOperatorSSH = func(context.Context, string) (net.Conn, error) { return selected, nil }
	t.Cleanup(func() { dialOperatorSSH = previous })
	want := errors.New("receipt could not authorize the command")
	released := false
	ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
	ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error { released = true; return nil })
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { return want })
	err := execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
	if !errors.Is(err, want) || released || !selected.closed || selected.streamed {
		t.Fatal("denied command released setup or reached its endpoint", err, released, selected.closed, selected.streamed)
	}
	var uncertain *controller.EffectsUncertain
	if errors.As(err, &uncertain) {
		t.Fatal("unpermitted command reported an unknown result", err)
	}
}

func TestSpaceOperationHelpNamesHostReviewPath(t *testing.T) {
	cmd := newSpaceOperationCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Help(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"yoloctl space operation SPACE --acknowledge-all", "yoloctl space operation SPACE ID --acknowledge", "neither retries nor undoes", "never acknowledged", "command-uncertain", "optional", "never granted permission", "success or failure", "recovered, applied", "operations depending on them", "without a restart", "a timer never stands in for evidence"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSpaceOperationRefusesConflictingAcknowledgmentBeforeContact(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no space exists to contact
	for _, args := range [][]string{
		{"work", "0123456789abcdef", "--acknowledge-all"},
		{"work", "--acknowledge", "--acknowledge-all"},
		{"work", "--acknowledge"},
	} {
		cmd := newSpaceOperationCmd()
		cmd.SetContext(t.Context())
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if !IsUserError(err) || !strings.Contains(err.Error(), "--acknowledge") {
			t.Fatal(args, err)
		}
	}
}
