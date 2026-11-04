package agentbox

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only the Lima transport and privileged path are substituted: the production
// guest probe runs against real Unix sockets, including a stale socket inode.
func readinessSocket(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for guest readiness probe")
	}
	// Keep below sockaddr_un's path limit even with long test names on macOS.
	dir, err := os.MkdirTemp("", "yolo-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "ssh.sock")
	script := `#!/bin/sh
set -eu
if [ "$5" = nerdctl ]; then
    [ "$6" = inspect ] && [ "$7" = --format ] && [ "$8" = '{{json .State}}' ] && [ "$9" = coder ]
    if [ "${YOLOCTL_TEST_INSPECT_HANG:-0}" = 1 ]; then exec sleep 30; fi
    printf '%s\n' "${YOLOCTL_TEST_CONTAINER_STATE:-}"
    exit 0
fi
[ "$#" = 8 ]
[ "$1" = shell ] && [ "$2" = yolo-ready ] && [ "$3" = -- ]
[ "$4" = sudo ] && [ "$5" = python3 ] && [ "$6" = -c ]
[ "$8" = /run/yoloctl/coder/ssh.sock ]
if [ "${YOLOCTL_TEST_HANG:-0}" = 1 ]; then exec sleep 30; fi
exec python3 -c 'import os,sys,time; time.sleep(float(os.environ.get("YOLOCTL_TEST_TRANSPORT_DELAY", "0"))); code=sys.argv.pop(1); exec(code)' "$7" "$YOLOCTL_TEST_SOCKET"
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_SOCKET", socketPath)
	t.Setenv("YOLOCTL_TEST_HANG", "0")
	return socketPath
}

func readinessListener(t *testing.T, path string, handle func(net.Conn)) *net.UnixListener {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			handle(conn)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return ln
}

func TestSandboxReadinessRejectsStaleSocket(t *testing.T) {
	path := readinessSocket(t)
	ln := readinessListener(t, path, func(net.Conn) {})
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("expected a stale socket inode: %v", err)
	}
	err = WaitForSandboxSocket(context.Background(), "ready", "coder", 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale socket treated as ready: %v", err)
	}
	if !strings.Contains(err.Error(), "box logs coder --space ready") {
		t.Fatalf("missing startup diagnostic hint: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	readinessListener(t, path, func(conn net.Conn) {
		_, _ = io.WriteString(conn, "SSH-2.0-test-supervisor\r\n")
		_, _ = io.Copy(io.Discard, conn)
	})
	if err := WaitForSandboxSocket(context.Background(), "ready", "coder", 3*time.Second); err != nil {
		t.Fatalf("replacement listener never became ready: %v", err)
	}
}

func TestSandboxReadinessRequiresSSHGreeting(t *testing.T) {
	for _, mode := range []string{"missing", "silent", "wrong-protocol", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			path := readinessSocket(t)
			if mode != "missing" {
				readinessListener(t, path, func(conn net.Conn) {
					switch mode {
					case "wrong-protocol":
						_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n")
					case "incomplete":
						_, _ = io.WriteString(conn, "SSH-2.0-incomplete")
					}
					_, _ = io.Copy(io.Discard, conn)
				})
			}
			err := WaitForSandboxSocket(context.Background(), "ready", "coder", 300*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s endpoint treated as ready: %v", mode, err)
			}
		})
	}
}

func TestSandboxReadinessRetriesUntilSupervisorAnswers(t *testing.T) {
	path := readinessSocket(t)
	var attempts atomic.Int32
	readinessListener(t, path, func(conn net.Conn) {
		if attempts.Add(1) == 1 {
			return // Listener is present but the supervisor is not serving yet.
		}
		_, _ = io.WriteString(conn, "SSH-2.0-test-supervisor\r\n")
		_, _ = io.Copy(io.Discard, conn)
	})
	if err := WaitForSandboxSocket(context.Background(), "ready", "coder", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("returned without retrying failed connection: %d attempts", got)
	}
}

func TestSandboxReadinessBoundsHungTransport(t *testing.T) {
	for _, mode := range []string{"timeout", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			readinessSocket(t)
			t.Setenv("YOLOCTL_TEST_HANG", "1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.DeadlineExceeded
			if mode == "canceled" {
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
				want = context.Canceled
			}
			started := time.Now()
			err := WaitForSandboxSocket(ctx, "ready", "coder", 300*time.Millisecond)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("hung transport escaped deadline: %s", elapsed)
			}
		})
	}
}

func TestSandboxReadinessAllowsColdTransport(t *testing.T) {
	path := readinessSocket(t)
	t.Setenv("YOLOCTL_TEST_TRANSPORT_DELAY", "2.2")
	readinessListener(t, path, func(conn net.Conn) {
		_, _ = io.WriteString(conn, "SSH-2.0-test-supervisor\r\n")
		_, _ = io.Copy(io.Discard, conn)
	})
	if err := WaitForSandboxSocket(t.Context(), "ready", "coder", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxReadinessReportsContainerExit(t *testing.T) {
	readinessSocket(t)
	t.Setenv("YOLOCTL_TEST_CONTAINER_STATE", `{"Status":"exited","Running":false,"ExitCode":3}`)
	start := time.Now()
	err := WaitForSandboxSocket(t.Context(), "ready", "coder", 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited with code 3") || !strings.Contains(err.Error(), "box logs coder --space ready") {
		t.Fatalf("missing early-exit diagnostic: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("waited for socket timeout after container exit")
	}
}

func TestSandboxReadinessDoesNotInferExitFromUncertainState(t *testing.T) {
	for _, state := range []string{"", "invalid", `{}`, `{"Status":"exited"}`, `{"Status":"created","ExitCode":0}`, `{"Status":"running","Running":true,"ExitCode":0}`} {
		t.Run(state, func(t *testing.T) {
			readinessSocket(t)
			t.Setenv("YOLOCTL_TEST_CONTAINER_STATE", state)
			err := WaitForSandboxSocket(t.Context(), "ready", "coder", 200*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("uncertain state reported as exit: %v", err)
			}
		})
	}
}

func TestSandboxReadinessBoundsHungInspect(t *testing.T) {
	readinessSocket(t)
	t.Setenv("YOLOCTL_TEST_INSPECT_HANG", "1")
	start := time.Now()
	err := WaitForSandboxSocket(t.Context(), "ready", "coder", 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("inspect escaped readiness deadline: %v", err)
	}
}

func TestNativeReadinessValidatesBoundedGreeting(t *testing.T) {
	for _, greeting := range []string{"SSH-2.0-supervisor\r\n", "HTTP/1.1 200 OK\r\n", "SSH-2.0-incomplete", "SSH-2.0-" + strings.Repeat("x", 300) + "\n"} {
		t.Run(greeting[:8], func(t *testing.T) {
			path := readinessSocket(t)
			readinessListener(t, path, func(c net.Conn) { _, _ = io.WriteString(c, greeting); _, _ = io.Copy(io.Discard, c) })
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			err := probeSandboxSSH(ctx, path)
			if (err == nil) != (greeting == "SSH-2.0-supervisor\r\n") {
				t.Fatalf("unexpected greeting result: %v", err)
			}
		})
	}
}
