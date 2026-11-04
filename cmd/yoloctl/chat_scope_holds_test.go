package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func callScopeFixture(t *testing.T, s *controller.Server, req controller.Request) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(ctx, server, controller.Principal{}) }()
	err := controller.Call(ctx, client, s.Hello, req, controller.ClientIO{Out: io.Discard, Err: io.Discard})
	<-done
	return err
}

func TestChatLifecycleGateChecksResolvedTargetsAndDependencies(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		byName       bool
		held         *controller.Resource
	}{
		{"default server", "chat.ensure", false, resourcePointer(boxResource("chat"))},
		{"default image", "chat.ensure", false, resourcePointer(imagesDomain)},
		{"revoke member ID", "chat.revoke", false, resourcePointer(boxResource("dev"))},
		{"revoke member name", "chat.revoke", true, resourcePointer(boxResource("dev"))},
		{"revoke actual account", "chat.revoke", false, resourcePointer(accountResource("actual"))},
		{"revoke desired account", "chat.revoke", false, resourcePointer(accountResource("desired"))},
		{"revoke cached image", "chat.revoke", false, resourcePointer(imagesDomain)},
		{"enroll actual account", "chat.enroll", false, resourcePointer(accountResource("actual"))},
		{"independent hold", "chat.revoke", false, resourcePointer(accountResource("other"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ctr := runtimeAccountFixture(t)
			cfg.ID = agentbox.NewIdentity()
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			store, inventory := roomRuntimeFixture(t)
			input := chatRuntimeRequest{DefaultHuman: "human"}
			if tc.method != "chat.ensure" {
				room, err := ensureManagedRoom(store, input, inventory)
				if err != nil {
					t.Fatal(err)
				}
				inventory.Declarations[cfg.Name] = cfg.ID
				room, err = store.Enroll(managedchat.Box{Name: cfg.Name, ID: cfg.ID}, room.Generation, inventory)
				if err != nil {
					t.Fatal(err)
				}
				member, err := room.RevokeTarget(cfg.Name)
				if err != nil {
					t.Fatal(err)
				}
				input = chatRuntimeRequest{MemberID: member.ID, Recreate: true}
				if tc.byName {
					input.MemberID = cfg.Name
				}
				if tc.method == "chat.enroll" {
					input = chatRuntimeRequest{Box: cfg.Name, Recreate: true}
				}
			}
			oldStore := nativeChatStoreFn
			t.Cleanup(func() { nativeChatStoreFn = oldStore })
			nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{
					ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
						return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
					},
					ListContainers: func(context.Context, string) ([]container.Container, error) { return []container.Container{ctr}, nil },
				}
			}
			s := &controller.Server{Hello: controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "generation"}, DiskID: store.DiskID, Receipts: controller.Receipts{Dir: t.TempDir()}, Operations: controllerOperations}
			if err := s.Receipts.Save(controller.Receipt{ID: "held-resource-0123456789", Method: "box.start", State: "uncertain", ProofVersion: 1, DiskID: store.DiskID, EffectPhase: controller.EffectStarted, Scope: &controller.Scope{Writes: []controller.Resource{*tc.held}}}); err != nil {
				t.Fatal(err)
			}
			permitted := false
			var got *controller.Scope
			s.Execute = func(ctx context.Context, _ controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
				ctx = controller.WithEffectGate(ctx, func(ctx context.Context, intent controller.EffectIntent) error {
					got = intent.Scope
					if err := streams.BeginEffects(ctx, intent); err != nil {
						return err
					}
					permitted = true
					return errors.New("fixture stops after admission, before any mutation")
				})
				_, err := runControllerChatRuntime(ctx, req, io.Discard, io.Discard)
				return 2, err
			}
			payload, _ := json.Marshal(input)
			req := controller.Request{ID: "chat-scope-request-0123456789", Method: tc.method, Space: "work", Payload: payload, Dash: -1}
			if err := callScopeFixture(t, s, req); err == nil {
				t.Fatal("fixture unexpectedly performed a mutation")
			}
			wantPermit := tc.name == "independent hold"
			if got == nil || got.Global || permitted != wantPermit {
				t.Fatalf("scope=%+v permitted=%v, want %v", got, permitted, wantPermit)
			}
			if tc.method != "chat.ensure" && (!slices.Contains(got.Reads, accountResource("actual")) || !slices.Contains(got.Reads, accountResource("desired"))) {
				t.Fatal("lifecycle omitted actual/desired account dependencies", got)
			}
			if !slices.Contains(got.Reads, imagesDomain) {
				t.Fatal("cached lifecycle omitted its image dependency", got)
			}
		})
	}
}

func resourcePointer(r controller.Resource) *controller.Resource { return &r }
