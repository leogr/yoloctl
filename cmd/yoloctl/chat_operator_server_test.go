package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

func TestChatOperatorServerReservationAndStaleBindsStayProtected(t *testing.T) {
	r := &managedchat.Room{Server: managedchat.Box{Name: "reserved"}}
	if !protectedManagedServerTarget(r, "reserved", nil) {
		t.Fatal("dormant reservation with no declaration/runtime became executable")
	}
	if protectedManagedServerTarget(r, "peer", nil) || protectedManagedServerTarget(nil, "peer", nil) {
		t.Fatal("ordinary target inherited server protection")
	}
	server := &container.Details{Labels: map[string]string{managedchat.RoleLabel: "server"}}
	if !protectedManagedServerTarget(nil, "stale", server) {
		t.Fatal("stale server without room metadata became executable")
	}
	for _, target := range []string{managedchat.HumanMount, managedchat.ServerConfigMount, managedchat.ServerDataMount, managedchat.ControlMount, "/run/yoloctl", managedchat.HumanMount + "/token"} {
		details := &container.Details{Mounts: []container.DetailMount{{Destination: target}}}
		if !protectedManagedServerTarget(nil, "stale", details) {
			t.Fatal("privileged stale bind became executable", target)
		}
	}
	for _, target := range []string{managedchat.IdentityMount, managedchat.EndpointMount, "/workdir", managedchat.ControlMount + "-ordinary"} {
		details := &container.Details{Mounts: []container.DetailMount{{Destination: target}}}
		if protectedManagedServerTarget(r, "peer", details) {
			t.Fatal("ordinary participant bind classified as human authority", target)
		}
	}
}

type operatorChatRuntimeFixture struct {
	containers []container.Container
	details    *container.Details
	listErr    error
	inspectErr error
	filter     string
	inspected  string
}

func (f *operatorChatRuntimeFixture) List(_ context.Context, filter string) ([]container.Container, error) {
	f.filter = filter
	if filter != "" {
		return nil, nil // stale runtime has no primary box label
	}
	return f.containers, f.listErr
}

func (f *operatorChatRuntimeFixture) Inspect(_ context.Context, name string) (*container.Details, error) {
	f.inspected = name
	return f.details, f.inspectErr
}

func TestChatOperatorServerInventoryIncludesRuntimeWithoutBoxLabel(t *testing.T) {
	for _, details := range []*container.Details{
		{Name: "stale", Labels: map[string]string{managedchat.RoleLabel: "server"}},
		{Name: "stale", Mounts: []container.DetailMount{{Destination: managedchat.HumanMount}}},
	} {
		f := &operatorChatRuntimeFixture{
			containers: []container.Container{{Name: "stale", Labels: ""}},
			details:    details, filter: "not called",
		}
		got, err := inspectOperatorChatTarget(t.Context(), "stale", f)
		if err != nil || f.filter != "" || f.inspected != "stale" || !protectedManagedServerTarget(nil, "stale", got) {
			t.Fatal("missing primary label hid a privileged target", err, f.filter, f.inspected, got)
		}
		f.inspected = ""
		if got, err = inspectOperatorChatTarget(t.Context(), "ordinary", f); err != nil || got != nil || f.inspected != "" {
			t.Fatal("unrelated stale runtime blocked an absent ordinary target", err, got, f.inspected)
		}
	}
	want := errors.New("inventory unavailable")
	for _, f := range []*operatorChatRuntimeFixture{
		{listErr: want},
		{containers: []container.Container{{Name: "stale"}}, inspectErr: want},
		{containers: []container.Container{{Name: "stale"}}},
		{containers: []container.Container{{Name: "stale"}}, details: &container.Details{Name: "replacement"}},
	} {
		if _, err := inspectOperatorChatTarget(t.Context(), "stale", f); err == nil {
			t.Fatal("uncertain runtime inventory became proof of absence", f)
		}
	}
}

func stubProtectedChatServer(t *testing.T, protected bool, err error) {
	t.Helper()
	previous := operatorManagedServerTargetFn
	operatorManagedServerTargetFn = func(context.Context, string, string) (bool, error) { return protected, err }
	t.Cleanup(func() { operatorManagedServerTargetFn = previous })
}

func TestChatOperatorServerLifecycleSuppressesOnlyIncidentalAttachment(t *testing.T) {
	stubProtectedChatServer(t, true, nil)
	for _, tty := range []bool{true, false} {
		t.Run(map[bool]string{true: "terminal", false: "script"}[tty], func(t *testing.T) {
			swapAttachSeams(t, tty, func(context.Context, *container.Client, string, string, io.Writer, bool, bool) error {
				t.Fatal("finite server lifecycle opened an attachment")
				return nil
			})
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			var notice bytes.Buffer
			cmd.SetErr(&notice)
			if err := prepareAutoAttach(cmd, "work", "chat", false, true); err != nil {
				t.Fatal("server lifecycle preflight refused finite work", err)
			}
			if err := maybeAutoAttach(cmd, "work", "chat", false, false, false); err != nil {
				t.Fatal("server lifecycle did not finish", err)
			}
			if !strings.Contains(notice.String(), "lifecycle completed") || !strings.Contains(notice.String(), "attachment is unavailable") || strings.Contains(notice.String(), "attach with:") {
				t.Fatal("missing finite completion diagnostic", notice.String())
			}
			if err := ensureAndAttachSession(t.Context(), nil, "work", "chat", io.Discard, false, false, sessionStartOptions{}); err == nil {
				t.Fatal("explicit attachment silently became finite success")
			}
			if err := attachToSession(t.Context(), "work", "chat"); err == nil {
				t.Fatal("live holder bypassed explicit attachment denial")
			}
			if err := execInAgentboxInteractive(t.Context(), "work", "chat", []string{"cat", "/private"}); err == nil {
				t.Fatal("explicit command reached the human server")
			}

		})
	}
}

func TestChatOperatorServerUncertainScopeRefusesExecution(t *testing.T) {
	want := errors.New("room metadata unavailable")
	stubProtectedChatServer(t, false, want)
	if err := guardOperatorChatExecution(t.Context(), "work", "peer"); !errors.Is(err, want) {
		t.Fatal("uncertain reservation was ignored", err)
	}
	p := controller.Principal{Box: "caller", Incarnation: "live"}
	if _, err := operatorManagedServerTarget(withControllerPrincipal(t.Context(), p), "work", "peer"); err == nil {
		t.Fatal("operator selected a target without the native setup lock")
	}
}

func TestChatOperatorTargetSelectionPrecedesSetupRelease(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	want := errors.New("fixture selected target refused connection")
	for _, action := range []string{"attach", "exec"} {
		t.Run(action, func(t *testing.T) {
			released := false
			ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
			ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error { released = true; return nil })
			previousSession, previousSSH := dialSessionVMFn, dialOperatorSSH
			t.Cleanup(func() { dialSessionVMFn = previousSession; dialOperatorSSH = previousSSH })
			selected := false
			selectTarget := func() error {
				selected = true
				if released {
					t.Fatal("name-based target selection ran after setup lock release")
				}
				return want
			}
			dialSessionVMFn = func(context.Context, string, string) (io.ReadWriteCloser, error) { return nil, selectTarget() }
			dialOperatorSSH = func(context.Context, string) (net.Conn, error) { return nil, selectTarget() }
			var err error
			if action == "attach" {
				err = attachToSession(ctx, "work", "peer")
			} else {
				err = execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
			}
			if !errors.Is(err, want) || !selected || released {
				t.Fatal("target selection/release ordering changed", err, selected, released)
			}
		})
	}
}

func TestChatOperatorAttachmentSelectionReleasesBeforeStreaming(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	previous, previousNative := dialSessionVMFn, nativeSessionTransportFn
	nativeSessionTransportFn = func() bool { return true }
	t.Cleanup(func() { dialSessionVMFn = previous; nativeSessionTransportFn = previousNative })
	selected, remote := net.Pipe()
	t.Cleanup(func() { _ = selected.Close(); _ = remote.Close() })
	released := false
	ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
	ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error { released = true; return nil })
	var setupCtx context.Context
	dialSessionVMFn = func(ctx context.Context, _, _ string) (io.ReadWriteCloser, error) {
		setupCtx = ctx
		deadline, bounded := ctx.Deadline()
		if released || !bounded || time.Until(deadline) > operatorTargetSetupTimeout {
			t.Fatal("native target selection needs a bounded context under its setup lock")
		}
		return selected, nil
	}
	transport, err := selectSessionTransport(ctx, "work", "peer")
	if err != nil || transport != selected || !released {
		t.Fatal("selected transport reached streaming before unlock", err, released)
	}
	if !errors.Is(setupCtx.Err(), context.Canceled) || ctx.Err() != nil {
		t.Fatal("selection deadline leaked into stream lifetime", setupCtx.Err(), ctx.Err())
	}
	if err := selected.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := remote.Write([]byte("x")); writeDone <- err }()
	var b [1]byte
	if _, err := io.ReadFull(transport, b[:]); err != nil || b[0] != 'x' {
		t.Fatal("canceling setup context closed the selected transport", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestChatNativeAdminAttachmentSelectsTargetBeforeNameCanBeReused(t *testing.T) {
	previous, previousNative := dialSessionVMFn, nativeSessionTransportFn
	nativeSessionTransportFn = func() bool { return true }
	t.Cleanup(func() { dialSessionVMFn = previous; nativeSessionTransportFn = previousNative })
	original, replacement := &operatorChatTransportFixture{}, &operatorChatTransportFixture{}
	target := original
	released := false
	ctx := context.WithValue(t.Context(), spaceReleaseKey{}, func() error {
		released = true
		target = replacement // the now-unlocked name can be replaced
		return nil
	})
	dialSessionVMFn = func(ctx context.Context, _, _ string) (io.ReadWriteCloser, error) {
		deadline, bounded := ctx.Deadline()
		if released || !bounded || time.Until(deadline) > operatorTargetSetupTimeout {
			t.Fatal("native admin selected a mutable name outside bounded setup")
		}
		return target, nil
	}
	transport, err := selectSessionTransport(ctx, "work", "peer")
	if err != nil || !released || transport != original || target != replacement {
		t.Fatal("native admin followed a reused name after validation", err)
	}
}

type operatorChatTransportFixture struct{ bytes.Buffer }

func (*operatorChatTransportFixture) Close() error { return nil }

func TestChatHostBridgeRetainsParentContext(t *testing.T) {
	previous, previousNative := dialSessionVMFn, nativeSessionTransportFn
	nativeSessionTransportFn = func() bool { return false }
	t.Cleanup(func() { dialSessionVMFn = previous; nativeSessionTransportFn = previousNative })
	ctx := t.Context()
	dialSessionVMFn = func(got context.Context, _, _ string) (io.ReadWriteCloser, error) {
		if got != ctx {
			t.Fatal("host bridge received a setup context instead of its stream lifetime")
		}
		return &operatorChatTransportFixture{}, nil
	}
	transport, err := selectSessionTransport(ctx, "work", "peer")
	if err != nil || ctx.Err() != nil {
		t.Fatal("host bridge lifetime ended after selection", err)
	}
	_ = transport.Close()
}

func TestChatOperatorStalledAttachmentReturnsForOperationCleanup(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	previous, previousNative := dialSessionVMFn, nativeSessionTransportFn
	nativeSessionTransportFn = func() bool { return true }
	t.Cleanup(func() { dialSessionVMFn = previous; nativeSessionTransportFn = previousNative })
	parent, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	ctx := withControllerPrincipal(parent, controller.Principal{Box: "caller", Incarnation: "live"})
	released := false
	ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error { released = true; return nil })
	dialSessionVMFn = func(ctx context.Context, _, _ string) (io.ReadWriteCloser, error) {
		<-ctx.Done()
		if released {
			t.Fatal("setup released before stalled target selection returned")
		}
		return nil, ctx.Err()
	}
	// registerSpaceOperations owns this deferred cleanup on setup errors.
	err := func() error {
		defer func() {
			if err := releaseSpaceOperation(ctx); err != nil {
				t.Error(err)
			}
		}()
		_, err := selectSessionTransport(ctx, "work", "peer")
		return err
	}()
	if !errors.Is(err, context.DeadlineExceeded) || !released {
		t.Fatal("stalled selection prevented operation cleanup", err, released)
	}
}

type chatReleaseTransport struct {
	net.Conn
	closed   bool
	streamed bool
}

func (c *chatReleaseTransport) Close() error {
	c.closed = true
	return c.Conn.Close()
}

func (c *chatReleaseTransport) Read([]byte) (int, error) {
	c.streamed = true
	return 0, errors.New("unexpected stream after setup failure")
}

func (c *chatReleaseTransport) Write([]byte) (int, error) {
	c.streamed = true
	return 0, errors.New("unexpected stream after setup failure")
}

func (c *chatReleaseTransport) SetDeadline(time.Time) error {
	c.streamed = true
	return errors.New("unexpected SSH handshake after setup failure")
}

func TestChatOperatorReleaseFailureClosesSelectedTransportBeforeStreaming(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, action := range []string{"select", "attach", "exec"} {
		t.Run(action, func(t *testing.T) {
			previousDial, previousSSH, previousNative := dialSessionVMFn, dialOperatorSSH, nativeSessionTransportFn
			nativeSessionTransportFn = func() bool { return true }
			t.Cleanup(func() {
				dialSessionVMFn, dialOperatorSSH, nativeSessionTransportFn = previousDial, previousSSH, previousNative
			})
			local, remote := net.Pipe()
			selected := &chatReleaseTransport{Conn: local}
			t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
			selectionDone, permitted, releaseCalls := false, false, 0
			want := errors.New("role publication failed before streaming")
			ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
			ctx = context.WithValue(ctx, spaceReleaseKey{}, func() error {
				releaseCalls++
				if !selectionDone || action == "exec" && !permitted {
					t.Fatal("setup released before transport selection or command permission")
				}
				return want
			})
			ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { permitted = true; return nil })
			dialSessionVMFn = func(context.Context, string, string) (io.ReadWriteCloser, error) {
				selectionDone = true
				return selected, nil
			}
			dialOperatorSSH = func(context.Context, string) (net.Conn, error) {
				selectionDone = true
				return selected, nil
			}
			var err error
			switch action {
			case "select":
				var transport io.ReadWriteCloser
				transport, err = selectSessionTransport(ctx, "work", "peer")
				if transport != nil {
					t.Fatal("failed release returned a live transport")
				}
			case "attach":
				err = attachToSession(ctx, "work", "peer")
			case "exec":
				err = execInAgentboxInteractive(ctx, "work", "peer", []string{"true"})
			}
			if !errors.Is(err, want) || releaseCalls != 1 || !selected.closed || selected.streamed {
				t.Fatal("failed release leaked a transport or reached streaming", err, releaseCalls, selected.closed, selected.streamed)
			}
		})
	}
}
