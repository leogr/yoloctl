package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/spf13/cobra"
)

// stdinIsTerminal must be a REAL terminal probe: /dev/null is a character
// device (isInteractive would pass it) but not a terminal, and a pipe is
// neither. The positive case uses a PTY slave — the representative "user at
// a terminal" fd — and skips where PTYs are unavailable (sandboxes).
func TestStdinIsTerminal(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	if stdinIsTerminal(devnull) {
		t.Errorf("%s must not count as a terminal (char device != tty)", os.DevNull)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()
	if stdinIsTerminal(pr) {
		t.Error("a pipe must not count as a terminal")
	}

	if stdinIsTerminal(strings.NewReader("x")) {
		t.Error("a non-file reader must not count as a terminal")
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no PTY available (%v); the positive case runs where PTYs exist", err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	if !stdinIsTerminal(slave) {
		t.Error("a PTY slave must count as a terminal")
	}
}

// swapAttachSeams replaces the auto-attach seams for one test, restoring
// them on cleanup. Seam-swapping tests must not run in parallel.
func swapAttachSeams(t *testing.T, tty bool, attach func(ctx context.Context, c *container.Client, space, name string, errw io.Writer, noAutoupdate, alreadyUpdated bool) error) {
	t.Helper()
	oldTTY, oldAttach := stdinIsTerminalFn, ensureAndAttachSessionFn
	stdinIsTerminalFn = func(io.Reader) bool { return tty }
	ensureAndAttachSessionFn = func(ctx context.Context, c *container.Client, space, name string, errw io.Writer, noAutoupdate, alreadyUpdated bool, _ sessionStartOptions) error {
		return attach(ctx, c, space, name, errw, noAutoupdate, alreadyUpdated)
	}
	t.Cleanup(func() {
		stdinIsTerminalFn = oldTTY
		ensureAndAttachSessionFn = oldAttach
	})
}

func TestMaybeAutoAttach(t *testing.T) {
	newCmd := func() (*cobra.Command, *bytes.Buffer) {
		cmd := &cobra.Command{}
		var errBuf bytes.Buffer
		cmd.SetErr(&errBuf)
		cmd.SetIn(strings.NewReader(""))
		return cmd, &errBuf
	}

	t.Run("no-attach opts out silently", func(t *testing.T) {
		swapAttachSeams(t, true, func(context.Context, *container.Client, string, string, io.Writer, bool, bool) error {
			t.Fatal("--no-attach must not attach")
			return nil
		})
		cmd, errBuf := newCmd()
		if err := maybeAutoAttach(cmd, "default", "coder", true, false, false); err != nil {
			t.Fatalf("maybeAutoAttach: %v", err)
		}
		if errBuf.Len() != 0 {
			t.Errorf("explicit opt-out must print nothing, got %q", errBuf.String())
		}
	})

	t.Run("non-terminal stdin skips with a space-qualified hint", func(t *testing.T) {
		swapAttachSeams(t, false, func(context.Context, *container.Client, string, string, io.Writer, bool, bool) error {
			t.Fatal("non-terminal stdin must not attach")
			return nil
		})
		cmd, errBuf := newCmd()
		if err := maybeAutoAttach(cmd, "staging", "coder", false, false, false); err != nil {
			t.Fatalf("maybeAutoAttach: %v", err)
		}
		if !strings.Contains(errBuf.String(), "attach with: yoloctl box attach coder --space staging") {
			t.Errorf("hint must name the box AND its space, got %q", errBuf.String())
		}
	})

	t.Run("terminal attaches through the seam with the right arguments", func(t *testing.T) {
		type call struct {
			space, name  string
			noAutoupdate bool
			updated      bool
		}
		var got *call
		swapAttachSeams(t, true, func(_ context.Context, _ *container.Client, space, name string, _ io.Writer, noAutoupdate, alreadyUpdated bool) error {
			got = &call{space: space, name: name, noAutoupdate: noAutoupdate, updated: alreadyUpdated}
			return nil
		})
		cmd, errBuf := newCmd()
		if err := maybeAutoAttach(cmd, "staging", "coder", false, true, true); err != nil {
			t.Fatalf("maybeAutoAttach: %v", err)
		}
		want := &call{space: "staging", name: "coder", noAutoupdate: true, updated: true}
		if got == nil || *got != *want {
			t.Errorf("attach called with %+v, want %+v", got, want)
		}
		if !strings.Contains(errBuf.String(), "notice: attaching to box coder (skip with --no-attach)") {
			t.Errorf("missing the attaching notice, got %q", errBuf.String())
		}
	})

	t.Run("attach outcome propagates", func(t *testing.T) {
		wantErr := &session.ExitError{Status: 7}
		swapAttachSeams(t, true, func(context.Context, *container.Client, string, string, io.Writer, bool, bool) error {
			return wantErr
		})
		cmd, _ := newCmd()
		err := maybeAutoAttach(cmd, "default", "coder", false, false, false)
		var se *session.ExitError
		if !errors.As(err, &se) || se.Status != 7 {
			t.Errorf("err = %v, want the session's ExitError{7}", err)
		}
	})
}

func TestShouldWarnPostAutoupdate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updated bool
		err     error
		want    bool
	}{
		{"no update, session failed", false, &session.ExitError{Status: 7}, false},
		{"updated, clean exit", true, nil, false},
		{"updated, detach", true, nil, false},
		{"updated, nonzero exit", true, &session.ExitError{Status: 7}, true},
		{"updated, wrapped exit", true, fmt.Errorf("attach: %w", &session.ExitError{Status: 1}), true},
		{"updated, holder lost", true, session.ErrSessionClosed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWarnPostAutoupdate(tc.updated, tc.err); got != tc.want {
				t.Errorf("shouldWarnPostAutoupdate(%v, %v) = %v, want %v", tc.updated, tc.err, got, tc.want)
			}
		})
	}
}

// The detach hint must work verbatim under any pinned/default space, so it
// is always space-qualified.
func TestFinishAttachDetachHint(t *testing.T) {
	var buf bytes.Buffer
	if err := finishAttach(context.Canceled, &buf, "staging", "coder"); err != nil {
		t.Fatalf("a cancellation is a detach, not an error: %v", err)
	}
	if !strings.Contains(buf.String(), "reattach: yoloctl box attach coder --space staging") {
		t.Errorf("detach hint = %q, want it space-qualified", buf.String())
	}

	buf.Reset()
	if err := finishAttach(nil, &buf, "default", "coder"); err != nil || buf.Len() != 0 {
		t.Errorf("clean exit must pass through silently: err=%v out=%q", err, buf.String())
	}

	exitErr := &session.ExitError{Status: 3}
	if err := finishAttach(exitErr, &buf, "default", "coder"); !errors.Is(err, exitErr) {
		t.Errorf("a session exit must pass through, got %v", err)
	}
}

// A generic-shell box revives yoloctl's built-in login shell — the same argv
// `box create NAME --agent none` starts — instead of failing the profile lookup.
func TestResolveSessionArgvGenericShell(t *testing.T) {
	argv, err := resolveSessionArgv("default", &agentbox.Config{Name: "shellbox", Agent: "none"})
	if err != nil {
		t.Fatalf("resolveSessionArgv: %v", err)
	}
	if want := []string{"bash", "-l"}; !slices.Equal(argv, want) {
		t.Errorf("generic-shell argv = %v, want %v", argv, want)
	}
}

// Every lifecycle command that auto-attaches must expose the opt-out.
func TestLifecycleNoAttachFlagWired(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
	}{
		{"box create", newAgentboxCreateCmd()},
		{"box copy", newAgentboxCopyCmd()},
		{"box start", newAgentboxStartCmd()},
		{"box restart", newAgentboxRestartCmd()},
		{"box recreate", newAgentboxRecreateCmd()},
	} {
		if tc.cmd.Flags().Lookup("no-attach") == nil {
			t.Errorf("%s: missing --no-attach", tc.name)
		}
	}
}
