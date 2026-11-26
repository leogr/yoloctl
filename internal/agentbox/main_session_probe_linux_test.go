package agentbox

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxendpoint"

	"golang.org/x/crypto/ssh"
)

// endpoint answers the main-session request with accept, and records every
// request it saw (nothing else may be asked).
func endpoint(t *testing.T, accept bool) (string, chan string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	socket := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	seen := make(chan string, 8)
	go func() {
		raw, err := l.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, chReqs, err := nc.Accept()
			if err != nil {
				return
			}
			go func() {
				for r := range chReqs {
					seen <- r.Type
					_ = r.Reply(accept && r.Type == boxendpoint.MainSessionRequest, nil)
				}
				_ = ch.Close()
			}()
		}
	}()
	return socket, seen
}

func TestProbeMainSessionAcceptedAndRefused(t *testing.T) {
	for _, accept := range []bool{true, false} {
		socket, seen := endpoint(t, accept)
		ok, err := probeMainSessionAt(t.Context(), "coder", socket)
		if err != nil || ok != accept {
			t.Fatalf("accept=%v: ok=%v err=%v", accept, ok, err)
		}
		if got := <-seen; got != boxendpoint.MainSessionRequest {
			t.Fatalf("asked %q", got)
		}
		select {
		case extra := <-seen:
			t.Fatalf("an extra request: %q", extra)
		default:
		}
	}
}

func TestProbeMainSessionUnreachableIsAnError(t *testing.T) {
	if _, err := probeMainSessionAt(t.Context(), "coder", filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Fatal("an unreachable endpoint was not an error")
	}
}

// The real current endpoint must answer the exact request used by the probe,
// with no process launch. A second probe must remain harmless and usable.
func TestProbeMainSessionCurrentEndpoint(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "current.sock")
	server, err := boxendpoint.NewServer(boxendpoint.Config{Workdir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := boxendpoint.ListenPrivate(socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	t.Cleanup(func() {
		server.Shutdown([]net.Listener{ln}, time.Second)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("current endpoint did not stop")
		}
	})
	for i := 0; i < 2; i++ {
		if ok, err := probeMainSessionAt(t.Context(), "current", socket); err != nil || !ok {
			t.Fatalf("probe %d: accepted=%v err=%v", i, ok, err)
		}
	}
}

// silentEndpoint accepts connections and never answers.
func silentEndpoint(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return socket
}

// An endpoint that never answers ends the probe with today's i/o timeout:
// at the fail-safe, at an earlier caller deadline, or at once on Ctrl+C.
func TestProbeMainSessionSilentEndpointEndsWithFailSafeOrCaller(t *testing.T) {
	probe := func(ctx context.Context) error {
		start := time.Now()
		_, err := probeMainSessionAt(ctx, "coder", silentEndpoint(t))
		if time.Since(start) > 30*time.Second {
			t.Fatal("probe outlived its end", time.Since(start))
		}
		return err
	}
	old := runtimeStepFailSafe
	shortRuntimeStepFailSafe(t, 200*time.Millisecond)
	if err := probe(t.Context()); err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatal("silent endpoint did not end at the fail-safe", err)
	}
	runtimeStepFailSafe = old
	caller, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := probe(caller); err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatal("earlier caller deadline did not end the probe", err)
	}
	cancelled, cancelNow := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancelNow)
	if err := probe(cancelled); err == nil {
		t.Fatal("caller cancellation did not end the probe")
	}
}
