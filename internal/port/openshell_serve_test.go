package port

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/openshell"
	"golang.org/x/crypto/ssh"
)

func serviceTargetFixture(t *testing.T) openshell.ForwardTarget {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return openshell.ForwardTarget{SocketPath: "/unused-fixture/ssh.sock", Port: 80, HostKey: key}
}

func availableForwardSpec(t *testing.T) Spec {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return Spec{Agentbox: "fixture", HostPort: port, ContainerPort: 80}
}

func TestOpenShellForwardServiceNeverPublishesAnOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	target := serviceTargetFixture(t)
	spec := Spec{Agentbox: "fixture", HostPort: listener.Addr().(*net.TCPAddr).Port, ContainerPort: 80}
	called := false
	err = ServeOpenShellForward(t.Context(), spec, func(context.Context) (openshell.ForwardTarget, error) { return target, nil }, func(net.Listener) error { called = true; return nil })
	if !errors.Is(err, ErrPortInUse) || called {
		t.Fatal("conflicted listener was published", err, called)
	}
}

func TestOpenShellForwardServiceRejectsInvalidTargetBeforePublication(t *testing.T) {
	spec := availableForwardSpec(t)
	target := serviceTargetFixture(t)
	target.SocketPath = "relative"
	called := false
	err := ServeOpenShellForward(t.Context(), spec, func(context.Context) (openshell.ForwardTarget, error) { return target, nil }, func(net.Listener) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("invalid private target published a listener", err, called)
	}
}

func TestOpenShellForwardServicePublicationFailureClosesItsListener(t *testing.T) {
	spec := availableForwardSpec(t)
	target := serviceTargetFixture(t)
	failure := errors.New("publication refused")
	err := ServeOpenShellForward(t.Context(), spec, func(context.Context) (openshell.ForwardTarget, error) { return target, nil }, func(net.Listener) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(spec.HostPort)))
	if err != nil {
		t.Fatal("failed startup retained its listener", err)
	}
	_ = listener.Close()
}

func TestOpenShellForwardServiceRechecksEveryConnectionAndNeverAdoptsChangedTarget(t *testing.T) {
	spec := availableForwardSpec(t)
	target := serviceTargetFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- ServeOpenShellForward(ctx, spec, func(setup context.Context) (openshell.ForwardTarget, error) {
			if _, ok := setup.Deadline(); !ok {
				return openshell.ForwardTarget{}, errors.New("unbounded connection verification")
			}
			if calls.Add(1) > 1 {
				changed := target
				changed.SocketPath = "/different-generation/ssh.sock"
				return changed, nil
			}
			return target, nil
		}, func(listener net.Listener) error { ready <- listener.Addr(); return nil })
	}()
	var address net.Addr
	select {
	case address = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("listener startup not bounded")
	}
	for range 2 {
		conn, err := net.DialTimeout("tcp4", address.String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var one [1]byte
		_, readErr := conn.Read(one[:])
		_ = conn.Close()
		if readErr == nil {
			t.Fatal("changed native target accepted a transport")
		}
		if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
			t.Fatal("changed target left incoming connection waiting")
		}
	}
	if calls.Load() != 3 {
		t.Fatal("native verification did not run once per connection", calls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not stop on explicit cancellation")
	}
}
