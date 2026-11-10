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
	"sync"
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
	var exit *ExitError
	if err := a.wait(t); !errors.As(err, &exit) || exit.Status != 3 {
		t.Fatalf("Attach = %v, want ExitError status 3", err)
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

// A completed holder-side write does not join the independent Attach consumer.
// Hold that consumer inside its output write until the real holder has exited,
// then release and join it before inspecting the final bytes.
func TestHolderEndpointDelayedAttachConsumer(t *testing.T) {
	opts, _ := startHolderEndpoint(t)
	opts.Argv = []string{"sh", "-c", `read -r line; printf 'got:%s\n' "$line"; exit 3`}
	res := runHolderAsync(context.Background(), opts)
	var conn net.Conn
	waitFor(t, "attach socket", func() bool {
		c, err := net.Dial("unix", opts.AttachSocket)
		if err != nil {
			return false
		}
		conn = c
		return true
	})
	t.Cleanup(func() { _ = conn.Close() })
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	a := &attachClient{stdinW: pw, done: make(chan error, 1)}
	w := &heldEndpointOutput{client: a, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	t.Cleanup(release)
	go func() {
		a.done <- Attach(context.Background(), conn, AttachIO{Stdin: pr, Stdout: w})
	}()
	if _, err := io.WriteString(pw, "hello\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the delayed consumer")
	}
	select {
	case r := <-res:
		if r.err != nil || r.status != 3 {
			t.Fatalf("RunHolder = %d, %v", r.status, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the holder while its consumer is held")
	}
	if got := a.output(); got != "" {
		t.Fatalf("held consumer output = %q, want empty", got)
	}
	select {
	case err := <-a.done:
		t.Fatalf("Attach returned before consumer release: %v", err)
	default:
	}
	release()
	var exit *ExitError
	if err := a.wait(t); !errors.As(err, &exit) || exit.Status != 3 {
		t.Fatalf("Attach = %v, want ExitError status 3", err)
	}
	if !strings.Contains(a.output(), "got:hello") {
		t.Fatalf("joined consumer output = %q", a.output())
	}
}

type heldEndpointOutput struct {
	client  *attachClient
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *heldEndpointOutput) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.client.Write(p)
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
