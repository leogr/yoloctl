package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func TestHostLifecycleResumeTargetThroughClientRoot(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		terminal, operator bool
		outcome            error
		want               bool
	}{
		{"create-failed-setup", []string{"box", "create", "created", "--agent", "none"}, true, false, &controller.ExitError{Status: 2, Message: "setup failed"}, true},
		{"copy-failed-attach", []string{"box", "copy", "source", "copied", "--no-autoupdate"}, true, false, &controller.ExitError{Status: 2, Message: "attach failed"}, true},
		{"start", []string{"box", "start", "started", "--passthrough"}, true, false, nil, true},
		{"restart", []string{"box", "restart", "restarted"}, true, false, nil, true},
		{"recreate-uncertain", []string{"box", "recreate", "rebuilt", "--force"}, true, false, &controller.OutcomeUnknown{ID: "test", Cause: io.EOF}, true},
		{"no-attach", []string{"box", "start", "skipped", "--no-attach"}, true, false, nil, false},
		{"manifest-only", []string{"box", "create", "skipped", "--agent", "none", "--manifest-only"}, true, false, nil, false},
		{"help", []string{"box", "copy", "source", "skipped", "--help"}, true, false, nil, false},
		{"invalid-args", []string{"box", "copy", "source"}, true, false, nil, false},
		{"noninteractive", []string{"box", "start", "skipped"}, false, false, nil, false},
		{"operator", []string{"box", "restart", "chat"}, true, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOperatorClient(t, nil)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("YOLOCTL_TUI", "1")
			t.Setenv(termio.ViewFDEnv, "")
			parent, child, err := termio.TabRequestPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close(); _ = child.Close() }()
			ctx := context.WithValue(t.Context(), tabRequestsKey{}, child)
			var calls int
			oldTarget := controllerTargetForCommand
			t.Cleanup(func() { controllerTargetForCommand = oldTarget })
			controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
				return controllerTarget{Space: "resolved", Operator: tc.operator, Call: func(_ context.Context, req controller.Request, _ controller.ClientIO) error {
					if req.Method == "box.inspect" {
						return nil
					}
					calls++
					return tc.outcome
				}}, nil
			}
			cmd := newClientRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader(""))
			if tc.terminal {
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = master.Close(); _ = slave.Close() }()
				oldOut := os.Stdout
				os.Stdout = slave
				defer func() { os.Stdout = oldOut }()
				cmd.SetIn(slave)
			}
			cmd.SetArgs(tc.args)
			err = cmd.ExecuteContext(ctx)
			if tc.want && (calls != 1 || !errors.Is(err, tc.outcome)) {
				t.Fatalf("request outcome changed: calls=%d err=%v", calls, err)
			}
			_ = child.Close()
			var got []termio.TabRequest
			if err := termio.ReadTabRequests(parent, func(r termio.TabRequest) { got = append(got, r) }); err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("excluded target published: %+v", got)
				}
				return
			}
			box := tc.args[2]
			if tc.args[1] == "copy" {
				box = tc.args[3]
			}
			want := []termio.TabRequest{{Op: "resume-target", Space: "resolved", Box: box, NoAutoupdate: tc.args[1] == "copy", Passthrough: tc.name == "start"}}
			if !slices.Equal(got, want) {
				t.Fatalf("metadata=%+v want=%+v", got, want)
			}
		})
	}
}

func TestClaimTabRequestsProtectsDescendants(t *testing.T) {
	parent, child, err := termio.TabRequestPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close(); _ = child.Close() }()
	fd, err := unix.Dup(int(child.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the exec-inherited descriptor, without two os.Files owning it.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv(termio.TabRequestFDEnv, fmt.Sprint(fd))
	ctx, closeRequests, err := claimTabRequests(t.Context())
	if err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	defer closeRequests()
	if os.Getenv(termio.TabRequestFDEnv) != "" || tabRequests(ctx) == nil {
		t.Fatal("private descriptor was not claimed")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("descendant could inherit private channel: %d %v", flags, err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if err := publishResumeTarget(cmd, "resolved", "copied", false, false); err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	closeRequests()
	var got termio.TabRequest
	if err := termio.ReadTabRequests(parent, func(r termio.TabRequest) { got = r }); err != nil || got.Box != "copied" {
		t.Fatalf("claimed channel lost: %+v %v", got, err)
	}
}
