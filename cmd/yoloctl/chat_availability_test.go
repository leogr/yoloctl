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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestChatFiniteReadsBypassHeldSpaceLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(store.Disk, layout.ChatManifest)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	oldStore, oldResolver := nativeChatStoreFn, newBoxStateResolver
	t.Cleanup(func() { nativeChatStoreFn, newBoxStateResolver = oldStore, oldResolver })
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) { return nil, nil },
		}
	}
	// This is a real kernel flock on a private fixture path. No native context
	// is configured, so neither this lock nor a subprocess can touch the VM.
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, method := range []string{"chat.status", "chat.channel.list"} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		var out bytes.Buffer
		req := controller.Request{Method: method, Space: "work"}
		if method == "chat.status" {
			_, err = runControllerChatRuntime(ctx, req, &out, io.Discard)
			var status managedchat.Status
			if json.Unmarshal(out.Bytes(), &status) != nil || status.Room == nil || status.Room.ID != room.ID || status.ServerReady {
				t.Fatal("invalid observational status", out.String())
			}
		} else {
			_, err = runControllerChatChannels(ctx, req, &out)
			var snapshot chatChannelSnapshot
			if json.Unmarshal(out.Bytes(), &snapshot) != nil || snapshot.RoomID != room.ID || snapshot.RuntimeApplied {
				t.Fatal("invalid observational catalog", out.String())
			}
		}
		cancel()
		if err != nil {
			t.Fatal(method, err)
		}
	}
	after, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observational requests mutated native state", err)
	}
	// An uncertain runtime inventory retains the valid desired catalog but
	// cannot claim either readiness or permission to perform setup.
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
			return nil, errors.New("runtime unavailable")
		}}
	}
	status, err := observeManagedChatRead(t.Context(), "work", store)
	if err != nil || status.Room == nil || status.Room.ID != room.ID || status.ServerReady || status.RuntimeApplied || status.SetupRequired || len(status.Pending) == 0 {
		t.Fatal(status, err)
	}
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
			_, err := store.SetHumanNickname("changed", room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("runtime unavailable")
		}}
	}
	if _, err := observeManagedChatRead(t.Context(), "work", store); err == nil {
		t.Fatal("published mixed-generation observation")
	}
	// Native identity errors are not converted into a plausible empty catalog.
	if err := os.Rename(manifest, manifest+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifest+".saved", manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := observeManagedChatRead(t.Context(), "work", store); err == nil {
		t.Fatal("followed substituted native manifest")
	}
}

func TestChatOpenOnlyEnsuresProvenSetupNeed(t *testing.T) {
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name            string
		status          managedchat.Status
		input           chatRuntimeRequest
		readErr         error
		ensure, failure bool
	}{
		{name: "healthy-participant-pending", status: managedchat.Status{Room: room, ServerReady: true, Pending: []string{"participant rebuilding"}}},
		{name: "absent", ensure: true},
		{name: "stopped", status: managedchat.Status{Room: room, SetupRequired: true}, ensure: true},
		{name: "unknown", status: managedchat.Status{Room: room, Pending: []string{"control unavailable"}}, failure: true},
		{name: "bad-read", readErr: errors.New("bad native identity"), failure: true},
		{name: "wrong-owner", status: managedchat.Status{Room: room, ServerReady: true}, input: chatRuntimeRequest{Human: "other"}, failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			header, err := inspectOrEnsureChat(t.Context(), test.input, func(_ context.Context, method string, _ chatRuntimeRequest) (managedchat.Status, error) {
				calls = append(calls, method)
				if method == "chat.status" {
					return test.status, test.readErr
				}
				return managedchat.Status{Room: room, ServerReady: true}, nil
			})
			if (err != nil) != test.failure || (!test.failure && header.RoomID != room.ID) {
				t.Fatal(header, err)
			}
			want := []string{"chat.status"}
			if test.ensure {
				want = append(want, "chat.ensure")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatal(calls)
			}
		})
	}
}

func TestChatMutationCancellationBeforeHeldLockKeepsNoEffectReceipt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	oldStore := nativeChatStoreFn
	t.Cleanup(func() { nativeChatStoreFn = oldStore })
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, method := range []string{"chat.ensure", "chat.channel.create"} {
		t.Run(method, func(t *testing.T) {
			hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "test-bundle", Space: "work", Generation: "test-generation"}
			receipts := controller.Receipts{Dir: t.TempDir()}
			server := &controller.Server{Hello: hello, Receipts: receipts, Operations: controllerOperations}
			started := make(chan struct{})
			gates := 0
			server.Execute = func(ctx context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
				ctx = controller.WithEffectGate(ctx, func(ctx context.Context, intent controller.EffectIntent) error {
					gates++
					return streams.BeginEffects(ctx, intent)
				})
				close(started)
				var err error
				if req.Method == "chat.ensure" {
					_, err = runControllerChatRuntime(ctx, req, streams.Out, streams.Err)
				} else {
					_, err = runControllerChatChannels(ctx, req, streams.Out)
				}
				return 2, err
			}
			req := controller.Request{ID: "cancelled-held-lock-" + strings.ReplaceAll(method, ".", "-"), Method: method, Space: "work"}
			if method == "chat.channel.create" {
				req.Payload, _ = json.Marshal(chatChannelRequest{RoomID: room.ID, Channel: "#test", Generation: room.Generation})
			}
			client, peer := net.Pipe()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); server.ServeConn(t.Context(), peer, controller.Principal{}) }()
			go func() { <-started; cancel() }()
			if err := controller.Call(ctx, client, hello, req, controller.ClientIO{Out: io.Discard, Err: io.Discard}); err == nil {
				t.Fatal("cancelled mutation reported success")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("actual chat handler did not leave the lock wait")
			}
			receipt, err := receipts.Read(req.ID)
			if err != nil || gates != 0 || receipt.State != "complete" || receipt.EffectPhase != controller.EffectWaiting || server.RecoveryRequired {
				t.Fatal("pre-effect cancellation created an uncertain hold", receipt, gates, err)
			}
			current, err := store.ReadManifestSnapshot()
			if err != nil || !reflect.DeepEqual(room, current) {
				t.Fatal("waiting mutation changed room", current, err)
			}
		})
	}
}
