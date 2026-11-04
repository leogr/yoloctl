package main

import (
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
)

func TestManagedChatStateRegistryIsAdminOnlyAndReceipted(t *testing.T) {
	for _, method := range []string{"chat.state.get", "chat.state.ensure", "chat.state.enroll", "chat.state.revoke"} {
		op, ok := controllerOperations[method]
		if !ok || op.BoxAllowed || op.Mutates != (method != "chat.state.get") {
			t.Fatal("unsafe chat operation classification", method)
		}
		req := controller.Request{ID: "0123456789abcdef", Method: method, Space: "work"}
		if err := controller.Authorize(controller.Principal{Box: "operator", Incarnation: "live"}, req, "work", controllerOperations); err == nil {
			t.Fatal("operator reached unfinished chat state API")
		}
		if err := controller.Authorize(controller.Principal{}, req, "work", controllerOperations); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range newRootCmd().Commands() {
		if cmd.Name() == "chat" {
			t.Fatal("worker CLI exposed host-only chat orchestration")
		}
	}
}

func TestManagedChatInventoryRefusesPartialRuntime(t *testing.T) {
	valid := boxstate.Box{Name: "coder", Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeMissing,
		Config: &agentbox.Config{Name: "coder", ID: agentbox.NewIdentity()}}
	if _, err := chatInventory([]boxstate.Box{valid}); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []boxstate.RuntimeState{boxstate.RuntimeUnknown, boxstate.RuntimeUnavailable} {
		bad := valid
		bad.Runtime = runtime
		if _, err := chatInventory([]boxstate.Box{bad}); err == nil {
			t.Fatal("partial runtime accepted", runtime)
		}
	}
	if _, err := chatInventory([]boxstate.Box{valid, valid}); err == nil {
		t.Fatal("duplicate inventory accepted")
	}
	valid.Declaration = boxstate.DeclarationInvalid
	if _, err := chatInventory([]boxstate.Box{valid}); err == nil {
		t.Fatal("partial declarations accepted")
	}
}

func TestBoxCopyDoesNotCloneChatDeclarationIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := &agentbox.Config{Name: "source", Space: "work", ID: agentbox.NewIdentity(),
		Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeEphemeral}}
	opts, _, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, source, "work", "copy")
	if err != nil {
		t.Fatal(err)
	}
	opts.workdir = agentbox.NewEphemeralWorkdir(opts.Name)
	copy, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if copy.ID != "" {
		t.Fatal("box copy inherited a chat enrollment identity")
	}
}
