package main

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
)

// A worker that fails before the room header reports its reason only on
// stderr. Through the real controller server and client transport, the view's
// stream error carries that reason, bounded and sanitized, instead of a bare
// exit status.
func TestChatStreamFailureCarriesWorkerStderr(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	ctx := t.Context()
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}
	server := &controller.Server{Hello: hello, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: map[string]controller.Operation{
		"chat.stream": controllerOperations["chat.stream"],
	}}
	noise := strings.Repeat("progress \x1b[31mline\r\n", 400) // more than the kept tail
	server.Execute = func(_ context.Context, _ controller.Principal, _ controller.Request, streams controller.Streams) (int, error) {
		// As RunWorker reports a failed worker: status 2, reason on stderr.
		_, _ = io.WriteString(streams.Err, noise+"Error: invalid AIRC client feature probe\n")
		return 2, nil
	}
	target := controllerTarget{Space: "work", Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
		client, peer := net.Pipe()
		done := make(chan struct{})
		go func() { server.ServeConn(ctx, peer, controller.Principal{}); close(done) }()
		defer func() { <-done }()
		return controller.Call(ctx, client, hello, req, streams)
	}}
	requestRead, requestWrite := io.Pipe()
	responseRead, responseWrite := io.Pipe()
	go func() {
		err := callChatStream(ctx, target, false, requestRead, responseWrite)
		_ = responseWrite.CloseWithError(err)
		_ = requestRead.CloseWithError(err)
	}()
	err := chatstream.RunChannels(ctx, responseRead, requestWrite, nil, nil, nil, nil, func(chatstream.Event) {})
	var exit *controller.ExitError
	if !errors.As(err, &exit) || exit.Status != 2 {
		t.Fatalf("the controller's exit status stays inspectable: %v", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "managed chat stream failed: command exited with status 2: ") || !strings.HasSuffix(msg, "Error: invalid AIRC client feature probe") {
		t.Fatalf("the view sees the worker's reason: %q", msg)
	}
	if strings.ContainsAny(msg, "\x1b\r\n") || len(msg) > chatStreamStderrLimit+200 {
		t.Fatalf("the reason is one bounded, control-free line: %d bytes", len(msg))
	}
	if receipt, err := server.Receipts.List(); err != nil || len(receipt) != 1 || receipt[0].Status != 2 {
		t.Fatalf("the controller recorded the failed stream: %+v %v", receipt, err)
	}
}

func TestStderrTailKeepsTheEnd(t *testing.T) {
	var tail stderrTail
	for range 100 {
		_, _ = tail.Write([]byte(strings.Repeat("a", 100)))
	}
	_, _ = tail.Write([]byte("\x00the end\x07"))
	if len(tail.buf) != chatStreamStderrLimit {
		t.Fatalf("kept %d bytes", len(tail.buf))
	}
	if err := tail.annotate(errors.New("failed")); !strings.HasSuffix(err.Error(), "the end") || strings.ContainsAny(err.Error(), "\x00\x07") {
		t.Fatalf("annotated: %q", err)
	}
	if err := (&stderrTail{}).annotate(nil); err != nil {
		t.Fatal("no error stays no error")
	}
}

// One oversized write keeps only its suffix, without retaining a buffer of
// that write's size.
func TestStderrTailBoundsOneOversizedWrite(t *testing.T) {
	var tail stderrTail
	_, _ = tail.Write([]byte("old "))
	huge := append([]byte(strings.Repeat("x", 1<<20)), "Error: last"...)
	if n, err := tail.Write(huge); n != len(huge) || err != nil {
		t.Fatalf("write reported %d, %v", n, err)
	}
	if len(tail.buf) != chatStreamStderrLimit || cap(tail.buf) > chatStreamStderrLimit {
		t.Fatalf("kept %d bytes in a buffer of %d", len(tail.buf), cap(tail.buf))
	}
	if !strings.HasSuffix(string(tail.buf), "Error: last") || strings.Contains(string(tail.buf), "old") {
		t.Fatal("the suffix of the oversized write replaces older bytes")
	}
	_, _ = tail.Write([]byte(" more"))
	if len(tail.buf) != chatStreamStderrLimit || cap(tail.buf) > chatStreamStderrLimit || !strings.HasSuffix(string(tail.buf), "last more") {
		t.Fatalf("a small write after it stays within the bound: %d/%d", len(tail.buf), cap(tail.buf))
	}
}
