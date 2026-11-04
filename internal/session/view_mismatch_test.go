package session

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A view's admission is classified like the stats handshake: the real
// holder's refusal of another build, alone or with the advice its build
// appends, and a well-formed HELLO of another protocol match
// ErrBuildMismatch and keep their messages, so the caller can replace that
// advice. A malformed HELLO, a look-alike or another refusal does not.
func TestViewRefusalsClassifyOnlyExplicitBuildAnswers(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.bundle = "previous-build"
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	ln.connections <- server
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	err := attachView(context.Background(), peer, AttachIO{Stdout: io.Discard})
	if !errors.Is(err, ErrBuildMismatch) || !strings.HasPrefix(err.Error(), holderMismatchReason+"; ") {
		t.Fatalf("the previous build's holder refusal: %v", err)
	}
	refuse := func(reason string) func(net.Conn) {
		return func(c net.Conn) {
			_ = writeFrame(c, frameHello, []byte{protocolVersion})
			_, _ = readFrame(c)
			_ = writeFrame(c, frameError, []byte(reason))
		}
	}
	cases := []struct {
		name     string
		serve    func(net.Conn)
		mismatch bool
		message  string
	}{
		{"protocol", func(c net.Conn) { _ = writeFrame(c, frameHello, []byte{protocolVersion + 1}) }, true, protocolMismatch},
		{"bare reason", refuse(holderMismatchReason), true, holderMismatchReason},
		{"older advice", refuse(holderMismatchReason + "; quit the old agent session"), true, holderMismatchReason + "; quit the old agent session"},
		{"malformed hello", func(c net.Conn) { _ = writeFrame(c, frameHello, []byte{protocolVersion, 0}) }, false, protocolMismatch},
		{"look-alike", refuse(holderMismatchReason + "ed"), false, holderMismatchReason + "ed"},
		{"other refusal", refuse("session terminal unavailable"), false, "session terminal unavailable"},
	}
	fake := func(serve func(net.Conn)) net.Conn {
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		go func() { defer func() { _ = server.Close() }(); serve(server) }()
		_ = peer.SetDeadline(time.Now().Add(time.Second))
		return peer
	}
	for _, c := range cases {
		err := attachView(context.Background(), fake(c.serve), AttachIO{Stdout: io.Discard})
		if errors.Is(err, ErrBuildMismatch) != c.mismatch || err == nil || err.Error() != c.message {
			t.Errorf("attach %s: %v", c.name, err)
		}
	}
	// The controller relays a managed view without parsing its handshake,
	// yet names the same answers for its caller.
	for _, c := range cases {
		if c.name == "malformed hello" {
			continue // the view side refuses it; the relay just forwards
		}
		local, view := net.Pipe()
		go func() {
			// The view subscribes after the HELLO, as a real one does.
			if _, err := readFrame(view); err == nil {
				_ = writeFrame(view, frameSubscribe, []byte("{}"))
			}
			_, _ = io.Copy(io.Discard, view)
		}()
		t.Cleanup(func() { _ = view.Close() })
		err := Relay(context.Background(), fake(c.serve), local, nil)
		if errors.Is(err, ErrBuildMismatch) != c.mismatch || err == nil || err.Error() != c.message {
			t.Errorf("relay %s: %v", c.name, err)
		}
	}
}
