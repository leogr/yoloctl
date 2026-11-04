package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
)

// This uses the real controller wire, admission and durable receipt store with
// a trusted direct Handler. Confined SSH and the private worker checkpoint are
// separately tested by their owners; the fixture never opens a VM/container.
func TestChatImageReceiptSeparatesPreEffectCancellationFromPossiblePOST(t *testing.T) {
	for _, mode := range []string{"cancel before gate", "reply lost after POST", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			p, input, room := chatImageFixture(t)
			beforeGate := make(chan struct{})
			conn := &imageTestConnection{run: func(_ []string, in *os.File, out, _ io.Writer) error {
				if _, err := io.Copy(io.Discard, in); err != nil {
					return err
				}
				if mode == "reply lost after POST" {
					return errors.New("HTTP response lost after publication")
				}
				_, err := out.Write(chatUploadReply(input))
				return err
			}}
			hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}
			safeCalls := 0
			server := &controller.Server{Hello: hello, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: map[string]controller.Operation{
				"chat.artifact.upload": controllerOperations["chat.artifact.upload"], "safe.mutate": {Mutates: true},
			}}
			server.Execute = func(run context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
				if req.Method == "safe.mutate" {
					safeCalls++
					return 0, nil
				}
				data, err := readChatImage(streams.In, input)
				if err != nil {
					return 2, err
				}
				run = controller.WithEffectGate(run, streams.BeginEffects)
				result, err := executeChatImageUpload(run, req.ID, input, data, room, conn, func() error {
					if mode == "cancel before gate" {
						close(beforeGate)
						<-run.Done()
						return run.Err()
					}
					return nil
				})
				if err != nil {
					return 2, err
				}
				return 0, json.NewEncoder(streams.Out).Encode(result)
			}
			request := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "chat.artifact.upload", Space: "work", Dash: -1, Payload: chatImagePayload(input)}
			clientCtx, stopClient := context.WithCancel(ctx)
			defer stopClient()
			callDone := make(chan error, 1)
			go func() { callDone <- callChatImageFixture(clientCtx, ctx, server, request, chatImageFrame(p)) }()
			if mode == "cancel before gate" {
				select {
				case <-beforeGate:
					stopClient()
				case err := <-callDone:
					t.Fatal("fixture ended before the pre-effect barrier", err)
				case <-ctx.Done():
					t.Fatal("fixture did not reach the pre-effect barrier")
				}
			}
			err := <-callDone
			if (err == nil) != (mode == "confirmed") {
				t.Fatal("wrong live result", err)
			}
			receipt, err := server.Receipts.Read(request.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantPhase, wantUploads := "complete", controller.EffectStarted, 1
			if mode == "cancel before gate" {
				wantPhase, wantUploads = controller.EffectWaiting, 0
			}
			if mode == "reply lost after POST" {
				wantState = "uncertain"
			}
			if receipt.State != wantState || receipt.EffectPhase != wantPhase || conn.uploads != wantUploads || server.RecoveryRequired != (wantState == "uncertain") {
				t.Fatal(receipt, "uploads", conn.uploads, "recovery", server.RecoveryRequired)
			}
			raw, err := os.ReadFile(filepath.Join(server.Receipts.Dir, request.ID+".json"))
			if err != nil || bytes.Contains(raw, []byte(input.PublicationToken)) || bytes.Contains(raw, []byte(input.SHA256)) {
				t.Fatal("receipt retained result/payload or could not be read", err)
			}
			if err := callChatImageFixture(ctx, ctx, server, request, nil); err == nil || conn.uploads != wantUploads {
				t.Fatal("duplicate ID replayed publication", err, conn.uploads)
			}
			next := controller.Request{ID: "fedcba9876543210fedcba9876543210", Method: "safe.mutate", Space: "work", Dash: -1}
			err = callChatImageFixture(ctx, ctx, server, next, nil)
			if (err == nil) != (wantState != "uncertain") || safeCalls != boolInt(wantState != "uncertain") {
				t.Fatal("incorrect recovery fence", err, safeCalls)
			}
		})
	}
}

func callChatImageFixture(clientCtx, serverCtx context.Context, server *controller.Server, req controller.Request, input io.Reader) error {
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() { server.ServeConn(serverCtx, peer, controller.Principal{}); close(done) }()
	err := controller.Call(clientCtx, client, server.Hello, req, controller.ClientIO{In: input, Out: io.Discard, Err: io.Discard})
	<-done
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
