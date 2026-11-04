package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func roomRuntimeFixture(t *testing.T) (managedchat.Store, managedchat.Inventory) {
	t.Helper()
	disk := t.TempDir()
	if err := os.Mkdir(filepath.Join(disk, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	return managedchat.Store{Disk: disk, DiskID: managedchat.NewID(), UID: os.Geteuid(), GID: os.Getegid()}, managedchat.Inventory{Complete: true, Declarations: map[string]string{}, Runtime: map[string]bool{}}
}

func TestChatEnsureOwnsReservedServerAndNeverAdoptsNames(t *testing.T) {
	s, i := roomRuntimeFixture(t)
	if _, err := ensureManagedRoom(s, chatRuntimeRequest{DefaultHuman: "person"}, managedchat.Inventory{}); err == nil {
		t.Fatal("partial inventory accepted")
	}
	i.Declarations["chat"] = ""
	if _, err := ensureManagedRoom(s, chatRuntimeRequest{DefaultHuman: "person"}, i); err == nil {
		t.Fatal("unrelated default server adopted")
	}
	delete(i.Declarations, "chat")
	r, err := ensureManagedRoom(s, chatRuntimeRequest{ServerBox: "room", DefaultHuman: "person"}, i)
	if err != nil {
		t.Fatal(err)
	}
	if !r.RuntimeEnabled || r.Server.Name != "room" || r.Human.Name != "person" {
		t.Fatal(r)
	}
	// A later caller's OS username is only a new-room default, never an
	// implicit human identity replacement on a previously owned room.
	again, err := ensureManagedRoom(s, chatRuntimeRequest{DefaultHuman: "different"}, i)
	if err != nil || again.ID != r.ID || again.Generation != r.Generation || again.Human.ID != r.Human.ID {
		t.Fatal(again, err)
	}
	i.Declarations["room"] = agentbox.NewIdentity()
	if _, err := ensureManagedRoom(s, chatRuntimeRequest{}, i); err == nil {
		t.Fatal("reused server declaration adopted")
	}
}

func TestChatEnableRefusesExistingRuntimeEvenReservedDeclaration(t *testing.T) {
	s, i := roomRuntimeFixture(t)
	r, err := s.Ensure("", "person", i)
	if err != nil {
		t.Fatal(err)
	}
	i.Declarations[r.Server.Name] = r.Server.ID
	i.Runtime[r.Server.Name] = true
	if _, err := ensureManagedRoom(s, chatRuntimeRequest{}, i); err == nil {
		t.Fatal("preexisting container adopted")
	}
	got, err := s.Load()
	if err != nil || got.RuntimeEnabled {
		t.Fatal("failed activation changed desired runtime", err)
	}
}

func TestChatEnrollmentMintsOnlyExplicitIdentityAndRefusesNameReuse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &agentbox.Config{Name: "coder", Space: "work", Image: "img:1"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	r := &managedchat.Room{Generation: 4, Server: managedchat.Box{Name: "chat"}, Human: managedchat.Member{Name: "person"}}
	i := managedchat.Inventory{Complete: true, Declarations: map[string]string{"coder": ""}, Runtime: map[string]bool{}}
	if _, err := prepareChatEnrollment("work", "coder", r, 3, i); err == nil {
		t.Fatal("stale generation changed declaration")
	}
	before, err := agentbox.Load("work", "coder")
	if err != nil || before.ID != "" {
		t.Fatal(before, err)
	}
	got, err := prepareChatEnrollment("work", "coder", r, 4, i)
	if err != nil || got.ID == "" || !slices.Contains(got.Features, "yolo.chat.skill") {
		t.Fatal(got, err)
	}
	oldID := got.ID
	if err := got.Delete(); err != nil {
		t.Fatal(err)
	}
	replacement := &agentbox.Config{Name: "coder", Space: "work", Image: "img:1"}
	if err := replacement.Save(); err != nil {
		t.Fatal(err)
	}
	r.Members = []managedchat.Member{{Name: "coder", ID: managedchat.NewID(), BoxID: oldID}}
	if _, err := prepareChatEnrollment("work", "coder", r, 4, i); err == nil {
		t.Fatal("reused name inherited enrollment")
	}
	r.Members[0].Revoked = true
	next, err := prepareChatEnrollment("work", "coder", r, 4, i)
	if err != nil || next.ID == oldID || next.ID == "" {
		t.Fatal(next, err)
	}
}

func TestChatRuntimeRegistrySeparatesOperatorsFromHumanIdentity(t *testing.T) {
	for _, method := range []string{"chat.ensure", "chat.status", "chat.enroll", "chat.revoke", "chat.connect", "chat.disconnect", "chat.stream", "chat.nickname", "chat.attach-target"} {
		op, ok := controllerOperations[method]
		allowed := operatorChatMethod(method)
		if !ok || op.BoxAllowed != allowed || op.Mutates != (method != "chat.status" && method != "chat.attach-target") {
			t.Fatal(method, op)
		}
		req := controller.Request{ID: "0123456789abcdef", Method: method, Space: "work"}
		if err := controller.Authorize(controller.Principal{Box: "operator", Incarnation: "live"}, req, "work", controllerOperations); (err == nil) != allowed {
			t.Fatal("unexpected operator authority", method, err)
		}
	}
	root := newClientRootCmd()
	for _, args := range [][]string{{"chat"}, {"chat", "ensure"}, {"chat", "status"}, {"chat", "enroll"}, {"chat", "revoke"}, {"chat", "connect"}, {"chat", "disconnect"}, {"chat", "nickname"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil || len(remaining) != 0 || cmd.RunE == nil {
			t.Fatal(args, err)
		}
	}
}

func TestManagedServerDeclarationRequiresExactConfinedWorkload(t *testing.T) {
	r := &managedchat.Room{Server: managedchat.Box{Name: "chat", ID: agentbox.NewIdentity()}}
	cfg := agentbox.Config{Name: "chat", ID: r.Server.ID, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("chat"), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
	if err := validateManagedServer(&cfg, r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*agentbox.Config){func(c *agentbox.Config) { c.ID = agentbox.NewIdentity() }, func(c *agentbox.Config) { c.UnconfinedNetwork = true }, func(c *agentbox.Config) { c.Privileged = true }, func(c *agentbox.Config) { c.SessionCommand = []string{"sh"} }, func(c *agentbox.Config) { c.Capabilities = []string{"yolo.space.operator"} }} {
		copy := cfg
		change(&copy)
		if validateManagedServer(&copy, r) == nil {
			t.Fatal("nonreserved workload accepted")
		}
	}
}
