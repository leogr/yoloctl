package agentbox

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
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
