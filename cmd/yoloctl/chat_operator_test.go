package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

func TestChatOperatorRegistryAndPrincipalScope(t *testing.T) {
	for method, operation := range controllerOperations {
		if strings.HasPrefix(method, "chat.") && operation.BoxAllowed != operatorChatMethod(method) {
			t.Fatal("frontend and server chat authority differ", method)
		}
	}
	for _, method := range []string{"chat.ensure", "chat.status", "chat.enroll", "chat.revoke", "chat.connect", "chat.disconnect"} {
		req := controller.Request{ID: "0123456789abcdef", Method: method, Space: "work"}
		for _, principal := range []controller.Principal{{Box: "caller", Incarnation: "live"}, {}} {
			if err := controller.Authorize(principal, req, "work", controllerOperations); err != nil {
				t.Fatal(method, err)
			}
		}
		for _, principal := range []controller.Principal{{Box: "caller"}, {Box: "caller", Incarnation: "live"}} {
			bad := req
			if principal.Incarnation != "" {
				bad.Space = "elsewhere"
			}
			if controller.Authorize(principal, bad, "work", controllerOperations) == nil {
				t.Fatal("invalid principal or foreign scope accepted", method)
			}
		}
	}
	for _, method := range []string{"chat.stream", "chat.nickname", "chat.attach-target", "chat.state.get", "chat.state.ensure", "chat.state.enroll", "chat.state.revoke", "chat.future-admin"} {
		if operatorChatMethod(method) {
			t.Fatal("implicit human or future authority", method)
		}
		req := controller.Request{ID: "0123456789abcdef", Method: method, Space: "work"}
		if controller.Authorize(controller.Principal{Box: "caller", Incarnation: "live"}, req, "work", controllerOperations) == nil {
			t.Fatal("human or recovery operation reached by operator", method)
		}
	}
}

func TestChatOperatorFrontendUsesExplicitMethodsAndCurrentSpace(t *testing.T) {
	stubOperatorClient(t, &controller.OperatorClient{Space: "work", Generation: "current"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"ensure"}, {"status"}, {"enroll", "peer"}, {"revoke", managedchat.NewID()}, {"connect", "peer"}, {"disconnect", "peer"}} {
		root := &cobra.Command{Use: "yoloctl"}
		chat := newChatCmd()
		root.AddCommand(chat)
		cmd, _, err := root.Find(append([]string{"chat"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		called := false
		cmd.RunE = func(*cobra.Command, []string) error { called = true; return nil }
		guardOperatorCommands(root)
		root.SetArgs(append([]string{"chat"}, args...))
		if err := root.ExecuteContext(t.Context()); err != nil || !called {
			t.Fatal(args, called, err)
		}
		if err := cmd.Flags().Set("space", "elsewhere"); err != nil {
			t.Fatal(err)
		}
		if _, err := selectControllerTarget(t.Context(), cmd); err == nil {
			t.Fatal("foreign chat space accepted", args)
		}
	}
	root := newClientRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"chat"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "human identity") {
		t.Fatal("operator opened human view", err)
	}
}

func TestChatOperatorEnrollmentPreflightPreservesSelfAndHumanIdentity(t *testing.T) {
	p := controller.Principal{Box: "caller", Incarnation: "live"}
	store, inventory := roomRuntimeFixture(t)
	defaultInput := chatRuntimeRequest{DefaultHuman: "sandbox"}
	if err := validateOperatorChatRuntime(p, "chat.ensure", defaultInput, nil); err == nil {
		t.Fatal("operator guest username became the human identity")
	}
	if _, err := os.Stat(filepath.Join(store.Disk, "config", "chat.json")); !os.IsNotExist(err) {
		t.Fatal("preflight rejection created a room", err)
	}
	r, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOperatorChatRuntime(p, "chat.ensure", defaultInput, r); err != nil {
		t.Fatal("existing human identity needs no replacement flag", err)
	}
	self := managedchat.Member{ID: managedchat.NewID(), BoxID: agentbox.NewIdentity(), Name: p.Box}
	r.Members = append(r.Members, self)
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		input  chatRuntimeRequest
	}{
		{"chat.enroll", chatRuntimeRequest{Box: p.Box, Recreate: true}},
		{"chat.revoke", chatRuntimeRequest{MemberID: self.ID, Recreate: true}},
		{"chat.revoke", chatRuntimeRequest{MemberID: p.Box, Recreate: true}},
	} {
		if err := validateOperatorChatRuntime(p, tc.method, tc.input, r); err == nil {
			t.Fatal("calling operator recreation accepted", tc.method)
		}
		tc.input.Recreate = false
		if err := validateOperatorChatRuntime(p, tc.method, tc.input, r); err != nil {
			t.Fatal("safe desired self membership rejected", err)
		}
	}
	if err := validateOperatorChatRuntime(p, "chat.enroll", chatRuntimeRequest{Box: "peer", Recreate: true}, r); err != nil {
		t.Fatal("peer recreation denied", err)
	}
	// Self is recognised by name even without a room or an active membership.
	if err := validateOperatorChatRuntime(p, "chat.revoke", chatRuntimeRequest{MemberID: p.Box, Recreate: true}, nil); err == nil {
		t.Fatal("calling operator recreation by name accepted without a room")
	}
	if err := validateOperatorChatRuntime(p, "chat.revoke", chatRuntimeRequest{MemberID: "peer", Recreate: true}, r); err != nil {
		t.Fatal("peer revocation recreation denied", err)
	}
	// A caller named like a member ID: that argument names the member with
	// that ID, here a peer, never the caller; the caller's own ID stays self.
	hex := controller.Principal{Box: strings.Repeat("a", 32), Incarnation: "live"}
	mine := managedchat.Member{ID: strings.Repeat("b", 32), BoxID: agentbox.NewIdentity(), Name: hex.Box}
	peer := managedchat.Member{ID: hex.Box, BoxID: agentbox.NewIdentity(), Name: "peer"}
	collide := &managedchat.Room{Members: []managedchat.Member{mine, peer}}
	if err := validateOperatorChatRuntime(hex, "chat.revoke", chatRuntimeRequest{MemberID: hex.Box, Recreate: true}, collide); err != nil {
		t.Fatal("peer recreation by a member ID equal to the caller's name denied", err)
	}
	if err := validateOperatorChatRuntime(hex, "chat.revoke", chatRuntimeRequest{MemberID: mine.ID, Recreate: true}, collide); err == nil {
		t.Fatal("calling operator recreation by its own member ID accepted")
	}
	if err := validateOperatorChatRuntime(controller.Principal{}, "chat.enroll", chatRuntimeRequest{Box: p.Box, Recreate: true}, r); err != nil {
		t.Fatal("host recreation denied", err)
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Fatal("operator preflight changed membership")
	}
	ctx := withControllerPrincipal(t.Context(), p)
	if currentControllerPrincipal(ctx) != p || !currentControllerPrincipal(t.Context()).Admin() {
		t.Fatal("trusted principal context changed")
	}
}
