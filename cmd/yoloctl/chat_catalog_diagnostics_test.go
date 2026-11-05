package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
)

func catalogDiagnosticTarget(t *testing.T, noise string, status int) controllerTarget {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}
	server := &controller.Server{Hello: hello, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: map[string]controller.Operation{"chat.channel.list": controllerOperations["chat.channel.list"]}}
	server.Execute = func(_ context.Context, _ controller.Principal, _ controller.Request, streams controller.Streams) (int, error) {
		_, _ = io.WriteString(streams.Err, noise)
		if status == 0 {
			_ = json.NewEncoder(streams.Out).Encode(chatChannelSnapshot{})
		}
		return status, nil
	}
	return controllerTarget{Space: "work", Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
		client, peer := net.Pipe()
		done := make(chan struct{})
		go func() { server.ServeConn(ctx, peer, controller.Principal{}); close(done) }()
		defer func() { <-done }()
		return controller.Call(ctx, client, hello, req, streams)
	}}
}

func TestCatalogDiagnosticsStayInsideInteractiveRead(t *testing.T) {
	for _, tc := range []struct {
		name, noise string
		status      int
	}{
		{"reason", "Error: catalog changed\n", 2},
		{"large-controls", strings.Repeat("x", 1<<20) + "\xff\x1b[31m\r\nError: latest reason\x00", 2},
		{"empty", "", 2}, {"success-noise", "success noise must disappear\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := catalogDiagnosticTarget(t, tc.noise, tc.status)
			var err error
			withoutCatalogTerminalWrites(t, func() { _, err = readChatChannelCatalogTarget(t.Context(), target) })
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var exit *controller.ExitError
			if !errors.As(err, &exit) || exit.Status != 2 {
				t.Fatalf("lost typed exit: %v", err)
			}
			if strings.ContainsAny(err.Error(), "\x1b\x00\r\n") || len(err.Error()) > chatStreamStderrLimit+100 {
				t.Fatalf("unbounded/unsafe diagnostic: %q", err)
			}
			if tc.name == "large-controls" && !strings.HasSuffix(err.Error(), "Error: latest reason") {
				t.Fatal(err)
			}
			// Finite CLI calls still explicitly forward their diagnostics.
			var terminal bytes.Buffer
			var dst chatChannelSnapshot
			err = callChatTargetInto(t.Context(), target, "chat.channel.list", chatChannelRequest{}, &dst, &terminal)
			if err == nil || terminal.String() != tc.noise {
				t.Fatal("finite CLI stderr changed", err)
			}
		})
	}
}

func TestConcurrentCatalogReadsHavePrivateDiagnosticTails(t *testing.T) {
	a, b := catalogDiagnosticTarget(t, "private-A", 2), catalogDiagnosticTarget(t, "private-B", 2)
	var wg sync.WaitGroup
	for _, tc := range []struct {
		target     controllerTarget
		own, other string
	}{{a, "private-A", "private-B"}, {b, "private-B", "private-A"}} {
		wg.Go(func() {
			_, err := readChatChannelCatalogTarget(t.Context(), tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.own) || strings.Contains(err.Error(), tc.other) {
				t.Errorf("shared capture: %v", err)
			}
		})
	}
	wg.Wait()
}

// The real interactive helper must not write either terminal sink, including
// on a successful RPC that emits diagnostics. Drain concurrently so a bug
// writing the oversized fixture cannot hang the test on a pipe quota.
func withoutCatalogTerminalWrites(t *testing.T, f func()) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(r); done <- data }()
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr; _ = w.Close(); _ = r.Close() }()
	os.Stdout, os.Stderr = w, w
	f()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = w.Close()
	if data := <-done; len(data) != 0 {
		t.Fatalf("interactive catalog wrote %d bytes outside renderer", len(data))
	}
}
