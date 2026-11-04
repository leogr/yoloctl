package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
)

func TestPrivateViewSurvivesRPCWorkerAndPTY(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "pipes", true: "PTY"}[tty], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, client := net.Pipe()
			private, renderer, err := termio.ViewPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = private.Close() }()
			defer func() { _ = renderer.Close() }()
			inputR, inputW := io.Pipe()
			defer func() { _ = inputR.Close(); _ = inputW.Close() }()
			raw := bytes.Repeat([]byte("\x00\xff\r\n\x1b]2;opaque\x1b\\"), 65536)
			checked := make(chan error, 1)
			go func() {
				ready := make([]byte, len("PRIVATE_READY"))
				_, err := io.ReadFull(renderer, ready)
				if err == nil && string(ready) != "PRIVATE_READY" {
					err = errors.New("private bytes mixed with stdout")
				}
				if err == nil {
					_, err = inputW.Write([]byte("stdin\x00\r\n\xfforder"))
					_ = inputW.Close()
				}
				if err == nil {
					var header [4]byte
					binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
					err = writeAll(renderer, header[:])
				}
				if err == nil {
					err = writeAll(renderer, raw)
				}
				if err == nil {
					got := make([]byte, len(raw))
					_, err = io.ReadFull(renderer, got)
					if err == nil && !bytes.Equal(got, raw) {
						err = errors.New("view bytes changed")
					}
				}
				checked <- err
			}()
			s := &Server{Hello: hello(), Operations: map[string]Operation{"box.attach": {Mutates: true}}, Receipts: Receipts{t.TempDir()}, Execute: RunWorker}
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(ctx, server, Principal{}) }()
			req := request("box.attach")
			req.Args = []string{"with-stdin"}
			req.View = true
			req.Terminal = tty
			req.Cols = 80
			req.Rows = 24
			req.Term = "xterm-256color"
			var out, stderr bytes.Buffer
			err = Call(ctx, client, hello(), req, ClientIO{In: inputR, Out: &out, Err: &stderr, View: private})
			var exit *ExitError
			// A worker error (for example a lost checkpoint) carries a message.
			if !errors.As(err, &exit) || exit.Status != 17 || exit.Message != "" {
				t.Fatalf("exit=%v out=%q err=%q", err, out.String(), stderr.String())
			}
			if err := <-checked; err != nil {
				t.Fatal(err)
			}
			<-done
			if !strings.Contains(out.String(), "stdout-only\x00tail") || strings.Contains(out.String(), "PRIVATE_READY") {
				t.Fatalf("ordinary output=%q", out.String())
			}
			if !tty && stderr.String() != "stderr-only" {
				t.Fatalf("stderr=%q", stderr.String())
			}
			receipt, err := s.Receipts.Read(req.ID)
			if err != nil || !receipt.SetupComplete || receipt.Status != 17 {
				t.Fatalf("receipt=%+v %v", receipt, err)
			}
		})
	}
}

func TestPrivateViewCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, client := net.Pipe()
	private, renderer, err := termio.ViewPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = private.Close() }()
	defer func() { _ = renderer.Close() }()
	s := &Server{Hello: hello(), Operations: map[string]Operation{"box.attach": {}}, Execute: RunWorker}
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(ctx, server, Principal{}) }()
	req := request("box.attach")
	req.View = true
	req.Args = []string{"wait"}
	result := make(chan error, 1)
	go func() {
		result <- Call(ctx, client, hello(), req, ClientIO{Out: io.Discard, Err: io.Discard, View: private})
	}()
	ready := make([]byte, len("PRIVATE_READY"))
	if _, err := io.ReadFull(renderer, ready); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled attachment succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker/server did not cancel")
	}
}
