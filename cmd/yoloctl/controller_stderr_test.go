package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestHostControllerAttachRendersLimaStderr(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_DEBUG", "")
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_CONTROLLER_FRAMES", filepath.Join(dir, "frames"))
	const message = "provisioning scripts should not reference the LIMA_CIDATA variables"
	const upstream = `time="2026-11-26T17:28:17+01:00" level=warning msg="` + message + `"` + "\n"
	const ordinary = "ordinary SSH stderr\r\n"
	const unrecognized = "time=invalid level=warning msg=raw\n"
	const bareSuffix = `time="2026-11-26T17:28:17+01:00" level=debug msg="probe" trailing plain text` + "\n"
	const badQuote = `time="2026-11-26T17:28:17+01:00" level=debug msg="probe" error="unfinished` + "\n"
	const terminalBytes = "session\r\n\x1b[2C"
	script := `#!/bin/sh
test "$1" = shell || exit 2
printf '%s' '` + upstream + `' >&2
printf '%s\n' 'time="2026-11-26T17:28:17+01:00" level=error msg="could not open \"socket\"" error="no such file"' >&2
printf '%s' '` + bareSuffix + badQuote + `' >&2
printf 'ordinary SSH stderr\r\ntime=invalid level=warning msg=raw\nlast fragment' >&2
cat "$YOLOCTL_TEST_CONTROLLER_FRAMES"
exec cat >/dev/null
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	boot := &spaceconfig.Bootstrap{Name: "work", Bundle: "fixture", Generation: "fixture"}
	var frames bytes.Buffer
	wire := controller.NewWire(nil, &frames)
	for _, frame := range []controller.Frame{
		{Type: "hello", Hello: &controller.Hello{Protocol: controller.ProtocolVersion, Bundle: boot.Bundle, Space: boot.Name, Generation: boot.Generation}},
		{Type: "stdout", Data: []byte(terminalBytes)},
		{Type: "stderr", Data: []byte(upstream)},
		{Type: "exit"},
	} {
		if err := wire.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "frames"), frames.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method string
		verbosity    prettylog.Verbosity
		visible      bool
	}{
		{"attach", "box.attach", prettylog.Normal, true},
		{"private telemetry", "space.telemetry", prettylog.Errors, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr lockedBuffer
			logger := prettylog.New(&stderr, logging.Presentation(prettylog.Options{
				Format: prettylog.FormatPlain, Verbosity: tc.verbosity,
			}))
			restore := logging.SetLogger(logger)
			t.Cleanup(func() { _ = logger.Close(); restore() })
			streams := controller.ClientIO{Out: &stdout, Err: &stderr}
			if tc.visible {
				streams.Log = controller.HostLogSink()
			}
			req := controller.Request{ID: "lima-attach-stderr-test", Method: tc.method, Space: boot.Name, Dash: -1}
			if err := callHostController(t.Context(), boot, req, streams); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != terminalBytes {
				t.Fatalf("controller stdout = %q, want %q", &stdout, terminalBytes)
			}
			got := stderr.String()
			for _, raw := range []string{ordinary, unrecognized, bareSuffix, badQuote, "last fragment"} {
				if !strings.Contains(got, raw) {
					t.Fatalf("stderr lost %q: %q", raw, got)
				}
			}
			const rendered = "warning: host>vm: lima: " + message + "\n"
			if tc.visible {
				if strings.Count(got, rendered) != 1 || strings.Count(got, upstream) != 1 ||
					!strings.Contains(got, `error: host>vm: lima: could not open "socket" error="no such file"`+"\n") {
					t.Fatalf("unexpected attach stderr: %q", got)
				}
			} else if strings.Count(got, upstream) != 2 || strings.Contains(got, rendered) {
				t.Fatalf("private capture changed: %q", got)
			}
		})
	}
}

// Exercise the actual raw-mode CLI path on a real PTY, then render the captured
// bytes with the pinned VT. A buffer-only writer test misses kernel ONLCR and
// would not have caught the original staircase in interactive preparation.
func TestControllerTerminalProgressColumns(t *testing.T) {
	for _, frontend := range []string{"tui", "passthrough", "managed"} {
		for _, method := range []string{"box.create", "box.recreate", "box.start", "box.attach"} {
			t.Run(frontend+"/"+method, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv(termio.ViewFDEnv, "")
				t.Setenv("YOLOCTL_TUI", "")
				if frontend == "tui" {
					t.Setenv("YOLOCTL_TUI", "1")
				}
				var private *os.File
				if frontend == "managed" {
					local, remote, err := termio.ViewPair()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = local.Close(); _ = remote.Close() }()
					// InheritedView owns this duplicate, just as it owns the
					// descriptor inherited by the standalone renderer's child.
					fd, err := unix.Dup(int(remote.Fd()))
					if err != nil {
						t.Fatal(err)
					}
					t.Setenv(termio.ViewFDEnv, strconv.Itoa(fd))
					private = local
				}
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = master.Close(); _ = slave.Close() }()
				if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 12}); err != nil {
					t.Fatal(err)
				}
				before, err := term.GetState(int(slave.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				oldOut := os.Stdout
				os.Stdout = slave
				defer func() { os.Stdout = oldOut }()
				cmd, _, err := newRootCmd().Find(strings.Split(method, "."))
				if err != nil {
					t.Fatal(err)
				}
				if frontend == "passthrough" {
					if err := cmd.Flags().Set("passthrough", "true"); err != nil {
						t.Fatal(err)
					}
				}
				cmd.SetContext(t.Context())
				cmd.SetIn(slave)
				cmd.SetOut(slave)
				cmd.SetErr(slave)
				captured := make(chan string, 1)
				go func() {
					data, _ := bufio.NewReader(master).ReadString('\x04')
					captured <- data
				}()
				const sessionBytes = "session\nraw\r\n\x1b[2C!"
				called := false
				target := controllerTarget{Operator: true, Call: func(_ context.Context, req controller.Request, s controller.ClientIO) error {
					called = true
					state, err := term.GetState(int(slave.Fd()))
					if err != nil || reflect.DeepEqual(state, before) || !req.Terminal {
						t.Fatalf("client did not enter raw mode: terminal=%v state=%v err=%v", req.Terminal, state, err)
					}
					// CRLF split at a frame boundary, ordinary bare LF, and a
					// carriage-return update must all retain the right columns.
					for _, chunk := range []string{"Preparing image\n", "Still preparing\r", "\n#14 build\n", "old\rnew\n"} {
						if _, err := io.WriteString(s.Err, chunk); err != nil {
							return err
						}
					}
					if private != nil {
						if !req.View || s.View == nil {
							t.Fatal("managed route lost its private channel")
						}
						if _, err := io.WriteString(s.View, sessionBytes); err != nil {
							return err
						}
					}
					_, err = io.WriteString(s.Out, sessionBytes+"\x04")
					return err
				}}
				if err := callControllerTerminal(cmd, target, controller.Request{Method: method}); err != nil {
					t.Fatal(err)
				}
				if !called {
					t.Fatal("controller route was bypassed")
				}
				if private != nil {
					if err := private.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					got := make([]byte, len(sessionBytes))
					_, err := io.ReadFull(private, got)
					if err != nil || string(got) != sessionBytes {
						t.Fatalf("private view bytes changed: %q, %v", got, err)
					}
				}
				after, err := term.GetState(int(slave.Fd()))
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("terminal state not restored: %v", err)
				}
				var got string
				select {
				case got = <-captured:
				case <-time.After(3 * time.Second):
					t.Fatal("PTY output did not complete")
				}
				const progress = "Preparing image\r\nStill preparing\r\n#14 build\r\nold\rnew\r\n"
				if got != progress+sessionBytes+"\x04" {
					t.Fatalf("progress/session bytes changed: %q", got)
				}
				vt, err := terminal.New(12, 80, 0, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = vt.Close() }()
				if _, err := vt.Write([]byte(got)); err != nil {
					t.Fatal(err)
				}
				lines, err := vt.Lines(false, true, 0, 4)
				if err != nil {
					t.Fatal(err)
				}
				for i, want := range []string{"Preparing image", "Still preparing", "#14 build", "new"} {
					if strings.TrimRight(lines[i], " ") != want {
						t.Fatalf("row %d is not at column zero: %q (want %q)", i, lines[i], want)
					}
				}
			})
		}
	}
}

func TestControllerTerminalRedirectedStderr(t *testing.T) {
	t.Setenv(termio.ViewFDEnv, "")
	t.Setenv("YOLOCTL_TUI", "1")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	oldOut := os.Stdout
	os.Stdout = slave
	defer func() { os.Stdout = oldOut }()
	for _, interactive := range []bool{true, false} {
		cmd := newAgentboxAttachCmd()
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader(""))
		if interactive {
			cmd.SetIn(slave)
		}
		var output bytes.Buffer
		cmd.SetErr(&output)
		const original = "bare\nexisting\r\nupdate\r\n"
		target := controllerTarget{Operator: true, Call: func(_ context.Context, _ controller.Request, s controller.ClientIO) error {
			_, err := io.WriteString(s.Err, original)
			return err
		}}
		if err := callControllerTerminal(cmd, target, controller.Request{Method: "box.attach"}); err != nil {
			t.Fatal(err)
		}
		if output.String() != original {
			t.Fatalf("interactive=%v redirected bytes changed: %q", interactive, output.String())
		}
	}
}

type stderrFailWriter struct {
	writes int
	failAt int
	err    error
}

func (w *stderrFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestRawTerminalStderrFailure(t *testing.T) {
	broken := errors.New("closed terminal")
	for _, err := range []error{broken, nil} {
		for failAt := 1; failAt <= 3; failAt++ {
			out := &stderrFailWriter{failAt: failAt, err: err}
			w := &rawTerminalStderr{out: out}
			n, got := w.Write([]byte("a\n"))
			want := broken
			if err == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(got, want) || n != min(failAt-1, 1) {
				t.Fatalf("failAt=%d: consumed %d, error %v", failAt, n, got)
			}
		}
	}
}
