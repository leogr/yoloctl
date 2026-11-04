package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// The chat runtime dispatcher owns completion: a failed role publication at
// either synchronization site withholds it, and any other outcome is recorded
// before its native lock is released.
func TestChatRuntimeWithholdsCompletionAfterFailedRolePublication(t *testing.T) {
	sentinel := errors.New("roles saved; publication pending")
	for _, tc := range []struct {
		name    string
		syncErr error
		want    []string
	}{
		{"failed publication", sentinel, []string{"role-sync"}},
		{"published", nil, []string{"role-sync", "complete-locked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			store, inventory := roomRuntimeFixture(t)
			if _, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory); err != nil {
				t.Fatal(err)
			}
			oldStore, oldResolver, oldSync := nativeChatStoreFn, newBoxStateResolver, synchronizeChatRolesFn
			t.Cleanup(func() {
				nativeChatStoreFn, newBoxStateResolver, synchronizeChatRolesFn = oldStore, oldResolver, oldSync
			})
			nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{
					ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
						return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
					},
					ListContainers: func(context.Context, string) ([]container.Container, error) { return nil, nil },
				}
			}
			var events []string
			synchronizeChatRolesFn = func(context.Context, string, managedchat.Store, []boxstate.Box, io.Writer) error {
				events = append(events, "role-sync")
				return tc.syncErr
			}
			locked := func() bool {
				probe, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer stop()
				unlock, err := spacelock.Acquire(probe, "work", true)
				if err == nil {
					unlock()
				}
				return err != nil
			}
			recorder := &lockedCompletionRecorder{locked: locked, events: &events}
			ctx := controller.WithEffectRecorder(t.Context(), recorder)
			ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { return nil })
			completion := &workerCompletion{}
			ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
			// After publication the server step fails ordinarily in this
			// fixture (no runtime), which is a known failed result.
			_, err := runControllerChatRuntime(ctx, controller.Request{Method: "chat.ensure", Space: "work"}, io.Discard, io.Discard)
			if err == nil {
				t.Fatal("fixture chat ensure unexpectedly succeeded")
			}
			if !slices.Equal(events, tc.want) || !completion.owned {
				t.Fatalf("events %v owned=%v err=%v", events, completion.owned, err)
			}
		})
	}
}

// A saved channel change whose publication or application is unconfirmed
// withholds completion; another failed result is recorded before unlock.
func TestChatChannelsWithholdCompletionAfterUnconfirmedPublication(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"unconfirmed", &publicationPending{errors.New("desired channel access saved; application unconfirmed")}, []string{"apply"}},
		{"refused", errors.New("channel room/generation changed"), []string{"apply", "complete-locked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			store, inventory := roomRuntimeFixture(t)
			room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
			if err != nil {
				t.Fatal(err)
			}
			oldStore, oldResolver, oldApply := nativeChatStoreFn, newBoxStateResolver, applyChatChannelChangeFn
			t.Cleanup(func() {
				nativeChatStoreFn, newBoxStateResolver, applyChatChannelChangeFn = oldStore, oldResolver, oldApply
			})
			nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{
					ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
						return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
					},
					ListContainers: func(context.Context, string) ([]container.Container, error) { return nil, nil },
				}
			}
			var events []string
			applyChatChannelChangeFn = func(context.Context, string, string, chatChannelRequest, managedchat.Store, managedchat.Inventory, []boxstate.Box) error {
				events = append(events, "apply")
				return tc.err
			}
			locked := func() bool {
				probe, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer stop()
				unlock, err := spacelock.Acquire(probe, "work", true)
				if err == nil {
					unlock()
				}
				return err != nil
			}
			recorder := &lockedCompletionRecorder{locked: locked, events: &events}
			ctx := controller.WithEffectRecorder(t.Context(), recorder)
			ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { return nil })
			completion := &workerCompletion{}
			ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
			payload, _ := json.Marshal(chatChannelRequest{RoomID: room.ID, Channel: "#build", Generation: room.Generation})
			if _, err := runControllerChatChannels(ctx, controller.Request{Method: "chat.channel.create", Space: "work", Payload: payload}, io.Discard); !errors.Is(err, tc.err) {
				t.Fatal(err)
			}
			if !slices.Equal(events, tc.want) || !completion.owned {
				t.Fatalf("events %v owned=%v", events, completion.owned)
			}
		})
	}
}
