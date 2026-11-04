package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

func TestBoxRunRemovedFromCommandHelpAndCompletion(t *testing.T) {
	stubOperatorClient(t, nil)
	configRoot := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("PATH", t.TempDir())
	for _, factory := range []struct {
		name string
		new  func() *cobra.Command
	}{{"worker", newRootCmd}, {"client", newClientRootCmd}} {
		t.Run(factory.name, func(t *testing.T) {
			root := factory.new()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"box", "run"})
			if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), `unknown command "run"`) {
				t.Fatalf("removed command succeeded or reached runtime: %v", err)
			}

			root = factory.new()
			out.Reset()
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"box", "--help"})
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "create") || !strings.Contains(out.String(), "attach") {
				t.Fatalf("supported box lifecycle missing from help: %s", &out)
			}
			for _, line := range strings.Split(out.String(), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 0 && fields[0] == "run" {
					t.Fatalf("removed command still advertised: %s", line)
				}
			}

			// The palette and shell both consume this real Cobra protocol.
			root = factory.new()
			out.Reset()
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"__complete", "box", "r"})
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "recreate") {
				t.Fatalf("completion did not exercise supported lifecycle: %s", &out)
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.SplitN(line, "\t", 2)[0] == "run" {
					t.Fatalf("completion still exposes run: %s", &out)
				}
			}

			create, _, err := root.Find([]string{"box", "create"})
			if err != nil || create.Name() != "create" || create.Flags().Lookup("session-command") == nil {
				t.Fatalf("named agentless creation lost session_command: %v, %v", create, err)
			}
		})
	}
	if _, err := os.Stat(configRoot); !os.IsNotExist(err) {
		t.Fatalf("removed command/help/completion created state: %v", err)
	}
}

func TestBoxRunRemovedFromControllerOperations(t *testing.T) {
	if _, exists := controllerOperations["box.run"]; exists {
		t.Fatal("removed command still grants a controller operation")
	}
	request := controller.Request{ID: "removed-box-run-request", Method: "box.run", Space: "work", Dash: -1}
	for _, principal := range []controller.Principal{{}, {Box: "operator", Incarnation: "current"}} {
		if err := controller.Authorize(principal, request, "work", controllerOperations); err == nil || !strings.Contains(err.Error(), "unsupported controller operation") {
			t.Fatalf("removed operation accepted for %+v: %v", principal, err)
		}
	}
	if controller.ManagedSessionMethod("box.run") {
		t.Fatal("removed operation still classified as managed session setup")
	}
}

func TestBoxCommandTree(t *testing.T) {
	root := newRootCmd()
	box, _, err := root.Find([]string{"box"})
	if err != nil {
		t.Fatalf("find box: %v", err)
	}
	if box.Name() != "box" {
		t.Errorf("box command Name() = %q, want %q", box.Name(), "box")
	}
	sub, _, err := root.Find([]string{"box", "attach"})
	if err != nil {
		t.Fatalf("find box attach: %v", err)
	}
	if sub.Name() != "attach" {
		t.Errorf("box attach resolved to %q, want attach", sub.Name())
	}
}
