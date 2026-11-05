package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestManagedAttachDefaultsAndPassthroughRouting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_TUI", "")
	t.Setenv(termio.ViewFDEnv, "")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	oldOut, oldRun := os.Stdout, runAttachedView
	defer func() { os.Stdout = oldOut; runAttachedView = oldRun }()
	os.Stdout = slave
	rendered, forwarded := 0, 0
	runAttachedView = func(_ context.Context, args []string) (bool, error) {
		rendered++
		if !strings.Contains(strings.Join(args, " "), "coder") || !strings.Contains(strings.Join(args, " "), "--space=fixture") {
			t.Fatalf("missing args: %v", args)
		}
		return false, &session.ExitError{Status: 7}
	}
	target := controllerTarget{Space: "fixture", Operator: true, Call: func(_ context.Context, req controller.Request, s controller.ClientIO) error {
		if req.Method == "box.inspect" {
			return nil // the read-only recreate-offer preflight; no drift reported
		}
		forwarded++
		if req.View || s.View != nil {
			t.Fatal("raw routing acquired private view")
		}
		return nil
	}}
	cmd := newAgentboxAttachCmd()
	cmd.SetIn(slave)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())
	req := controller.Request{ID: "view-request-fixture", Method: "box.attach", Space: "fixture", Args: []string{"coder"}, Flags: map[string][]string{}, Dash: -1}
	err = callControllerTerminal(cmd, target, req)
	if classifyError(err) != 7 || rendered != 1 || forwarded != 0 {
		t.Fatalf("default routing: %v %d %d", err, rendered, forwarded)
	}
	if err := cmd.Flags().Set("passthrough", "true"); err != nil {
		t.Fatal(err)
	}
	if err := callControllerTerminal(cmd, target, req); err != nil {
		t.Fatal(err)
	}
	if rendered != 1 || forwarded != 1 {
		t.Fatal("passthrough started renderer")
	}
	if err := cmd.Flags().Set("passthrough", "false"); err != nil {
		t.Fatal(err)
	}
	cmd.SetIn(bytes.NewReader(nil))
	if err := callControllerTerminal(cmd, target, req); err != nil {
		t.Fatal(err)
	}
	if rendered != 1 || forwarded != 2 {
		t.Fatal("nonterminal started renderer")
	}
}

func TestSessionViewFlagsAndDetachHint(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"box", "attach"}, {"attach"}, {"box", "create"}, {"box", "copy"}, {"box", "start"}, {"box", "restart"}, {"box", "recreate"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		f := cmd.Flags().Lookup("passthrough")
		if f == nil || f.DefValue != "false" {
			t.Fatalf("flag for %v: %v", path, f)
		}
	}
	hint := managedDetachHint(controller.Request{Method: "box.copy", Space: "work", Args: []string{"source", "dest"}})
	if !strings.Contains(hint, "box attach dest --space work") {
		t.Fatal(hint)
	}
}

func TestHostTerminalIdentityTravelsWithSessionRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(termio.ViewFDEnv, "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("TERM_PROGRAM_VERSION", "1.3.0")
	cmd := newAgentboxAttachCmd()
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	called := false
	target := controllerTarget{Space: "fixture", Call: func(_ context.Context, req controller.Request, _ controller.ClientIO) error {
		called = true
		// Include wire encoding: the VM must receive both fields, separately
		// from TERM, rather than inheriting the controller's environment.
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		var decoded controller.Request
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatal(err)
		}
		want := termio.TerminalIdentity{Program: "ghostty", Version: "1.3.0"}
		if decoded.Term != "xterm-256color" || decoded.Session == nil || decoded.Session.Terminal != want {
			t.Fatalf("terminal identity lost or TERM changed: %+v", decoded)
		}
		decoded.Session.Terminal.Version = "1.3.0\n"
		if decoded.Validate() == nil {
			t.Fatal("control characters accepted in terminal identity")
		}
		return nil
	}}
	req := controller.Request{ID: "terminal-identity-fixture", Method: "box.attach", Space: "fixture", Args: []string{"coder"}, Dash: -1}
	if err := callControllerTerminal(cmd, target, req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("controller was not called")
	}
}
