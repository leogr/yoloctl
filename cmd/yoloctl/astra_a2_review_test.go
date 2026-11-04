package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/port"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func TestAstraA2RegisteredFootprints(t *testing.T) {
	for _, which := range []string{"nickname", "attach-ports", "chat-build"} {
		t.Run(which, func(t *testing.T) {
			cfg, ctr := runtimeAccountFixture(t)
			cfg.Ports = []agentbox.Port{{Target: 8080, Published: 8080}}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
			method := "chat.nickname"
			intent := chatIntent("", "chat-server")
			if which == "attach-ports" {
				method = "box.attach"
				cmd := &cobra.Command{}
				cmd.SetContext(t.Context())
				intent = spaceOperationIntent(cmd, "box attach", "work", []string{"dev"})
			}
			if which == "chat-build" {
				method = "chat.ensure"
			}
			s := &controller.Server{Hello: controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "test", Space: "work", Generation: "test"}, DiskID: "0123456789abcdef0123456789abcdef", Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: controllerOperations}
			s.Execute = func(ctx context.Context, _ controller.Principal, _ controller.Request, streams controller.Streams) (int, error) {
				if err := streams.BeginEffects(ctx, intent); err != nil {
					return 2, err
				}
				if which == "chat-build" {
					if err := streams.RecordEvidence.ExpandEffects(ctx, []controller.Resource{imagesDomain}); err != nil {
						return 2, err
					}
				}
				return 0, nil
			}
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), server, controller.Principal{}) }()
			req := controller.Request{ID: "astra-review-" + which, Method: method, Space: "work", Dash: -1}
			err := controller.Call(t.Context(), client, s.Hello, req, controller.ClientIO{Out: io.Discard, Err: io.Discard})
			<-done
			if err != nil {
				t.Fatalf("valid production intent rejected: %v; scope=%+v", err, intent.Scope)
			}
		})
	}
}

// Captures the actual dispatcher intent before any mutation, not a hand-built scope.
func TestAstraA2ChatResolvedTargets(t *testing.T) {
	for _, which := range []string{"default-server", "revoke-member"} {
		t.Run(which, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			store, inventory := roomRuntimeFixture(t)
			method := "chat.ensure"
			input := chatRuntimeRequest{DefaultHuman: "human"}
			want := "chat"
			if which == "revoke-member" {
				room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
				if err != nil {
					t.Fatal(err)
				}
				id := agentbox.NewIdentity()
				inventory.Declarations["victim"] = id
				room, err = store.Enroll(managedchat.Box{Name: "victim", ID: id}, room.Generation, inventory)
				if err != nil {
					t.Fatal(err)
				}
				member, err := room.RevokeTarget("victim")
				if err != nil {
					t.Fatal(err)
				}
				method = "chat.revoke"
				input = chatRuntimeRequest{MemberID: member.ID, Recreate: true}
				want = "victim"
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
			stop := errors.New("stop before effects")
			var got *controller.Scope
			ctx := controller.WithEffectGate(t.Context(), func(_ context.Context, i controller.EffectIntent) error { got = i.Scope; return stop })
			payload, _ := json.Marshal(input)
			_, err := runControllerChatRuntime(ctx, controller.Request{Method: method, Space: "work", Payload: payload}, io.Discard, io.Discard)
			if !errors.Is(err, stop) {
				t.Fatalf("did not reach gate: %v", err)
			}
			if got == nil || (!got.Global && !slices.Contains(got.Writes, boxResource(want))) {
				t.Fatalf("actual dispatcher omitted effect target %q: %+v", want, got)
			}
		})
	}
}

func TestAstraA2UndeclaredPortFootprint(t *testing.T) {
	_, ctr := runtimeAccountFixture(t)
	footprintForwards = func(context.Context, string, string) ([]port.Spec, error) {
		return []port.Spec{{Agentbox: "dev", HostPort: 8080, ContainerPort: 8080}}, nil
	}
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	for _, path := range []string{"box stop", "box recreate"} {
		intent := spaceOperationIntent(cmd, path, "work", []string{"dev"})
		// These paths unconditionally enumerate/remove actual forwards, even when
		// desired Ports is empty (e.g. a saved edit whose reconcile failed).
		if !intent.Scope.Global && !slices.Contains(intent.Scope.Writes, portsDomain) {
			t.Errorf("%s reaches actual port cleanup without any port inventory or hold: %+v", path, intent.Scope)
		}
	}
}
