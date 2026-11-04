//go:build linux

package session

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxendpoint"
)

// startHolderEndpoint serves an isolated, unprivileged endpoint for a holder.
func startHolderEndpoint(t *testing.T) (HolderOptions, string) {
	t.Helper()
	opts, dir := testPaths(t)
	env, err := boxendpoint.ChildEnvironment(dir, boxendpoint.SandboxUser, os.Getenv("PATH"), "")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := boxendpoint.NewServer(boxendpoint.Config{Workdir: dir, Env: env, HangupGrace: 300 * time.Millisecond, DrainTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := boxendpoint.ListenPrivate(opts.SSHSocket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Shutdown([]net.Listener{ln}, 2*time.Second) })
	return opts, dir
}

// The real holder drives the privileged box endpoint unchanged: PTY request
// with measured pixels, resize, keepalives, input, output and exit status.
// Host integration helpers are not started here: their guest directory is
// shared with the coding session that runs these tests.
func TestHolderAgainstPrivilegedEndpoint(t *testing.T) {
	opts, _ := startHolderEndpoint(t)
	opts.KeepaliveInterval = 20 * time.Millisecond
	opts.LaunchToken = strings.Repeat("a", 32)
	opts.Argv = []string{"sh", "-c", `stty size; read -r line; printf 'got:%s\n' "$line"; stty size; exit 3`}
	res := runHolderAsync(context.Background(), opts)
	a := attachWhenReady(t, opts.AttachSocket)
	waitFor(t, "initial size", func() bool { return strings.Contains(a.output(), "30 100") })
	time.Sleep(100 * time.Millisecond) // several keepalives on the live connection
	if _, err := io.WriteString(a.stdinW, "hello\r"); err != nil {
		t.Fatal(err)
	}
	r := <-res
	if r.err != nil || r.status != 3 {
		t.Fatalf("RunHolder = %d, %v; output %q", r.status, r.err, a.output())
	}
	if !strings.Contains(a.output(), "got:hello") {
		t.Fatalf("output = %q", a.output())
	}
	ts, err := readSessionFile[Tombstone](opts.TombstonePath)
	if err != nil || ts.ExitCode != 3 {
		t.Fatalf("tombstone = %+v, %v", ts, err)
	}
	if _, err := readSessionFile[Marker](opts.MarkerPath); !os.IsNotExist(err) {
		t.Fatalf("marker after a clean exit: %v", err)
	}
}

// Unlike openshell-sandbox, the endpoint hangs up a session whose holder
// disconnects, so a stopped holder leaves its marker (the lost-holder rule is
// unchanged) but no orphaned agent behind.
func TestHolderStopEndsPrivilegedAgent(t *testing.T) {
	opts, _ := startHolderEndpoint(t)
	opts.Argv = []string{"sh", "-c", `echo "pid:$$"; exec sleep 300`}
	ctx, cancel := context.WithCancel(context.Background())
	res := runHolderAsync(ctx, opts)
	a := attachWhenReady(t, opts.AttachSocket)
	pid := 0
	waitFor(t, "agent pid", func() bool {
		m := regexp.MustCompile(`pid:(\d+)`).FindStringSubmatch(a.output())
		if m == nil {
			return false
		}
		pid, _ = strconv.Atoi(m[1])
		return true
	})
	cancel()
	if r := <-res; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("RunHolder err = %v", r.err)
	}
	if _, err := readSessionFile[Marker](opts.MarkerPath); err != nil {
		t.Fatalf("marker must survive a holder stop: %v", err)
	}
	waitFor(t, "agent exit", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
}
