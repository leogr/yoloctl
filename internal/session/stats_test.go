package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
)

func pollStats(t *testing.T, sock string) Stats {
	t.Helper()
	var s Stats
	waitFor(t, "session stats", func() bool {
		var err error
		s, err = ReadStats(context.Background(), sock)
		return err == nil
	})
	return s
}

func waitStats(t *testing.T, sock, what string, cond func(Stats) bool) Stats {
	t.Helper()
	var s Stats
	waitFor(t, what, func() bool {
		var err error
		s, err = ReadStats(context.Background(), sock)
		return err == nil && cond(s)
	})
	return s
}

// Counters move only for bytes the agent's PTY produced and input the holder
// accepted. Holder-made titles, query replies, replay, snapshots and stats
// reads themselves change nothing.
func TestStatsCountOnlyWorkloadBytes(t *testing.T) {
	opts, _ := testPaths(t)
	opts.TitleTemplate = "" // the holder publishes its own initial title
	if len(newTitleRewriter(opts.Agentbox).InitialTitle()) == 0 {
		t.Fatal("fixture needs a synthetic initial title")
	}
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	start := pollStats(t, opts.AttachSocket)
	if start.OutputBytes != 0 || start.InputPackets != 0 || start.InputBytes != 0 || start.Session == "" {
		t.Fatalf("synthetic title counted or session missing: %+v", start)
	}

	fs.emit("\x1b[6nhello")
	waitFor(t, "query reply", func() bool { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.stdin.String() == "\x1b[1;1R" })
	afterOutput := waitStats(t, opts.AttachSocket, "output counted", func(s Stats) bool { return s.OutputBytes == 9 })
	if afterOutput.InputPackets != 0 || afterOutput.InputBytes != 0 {
		t.Fatalf("the holder's own query reply counted as input: %+v", afterOutput)
	}

	raw := attachWhenReady(t, opts.AttachSocket)
	waitFor(t, "replay", func() bool { return strings.Contains(raw.output(), "hello") })
	managed := attachScreenConn(t, dialSocket(t, opts.AttachSocket))
	for range 3 {
		if s := pollStats(t, opts.AttachSocket); s != afterOutput {
			t.Fatalf("replay, snapshot or a stats read changed counters: %+v != %+v", s, afterOutput)
		}
	}
	managed.mu.Lock()
	inputs := len(managed.inputs)
	managed.mu.Unlock()

	if _, err := raw.stdinW.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	typed := waitStats(t, opts.AttachSocket, "input counted", func(s Stats) bool { return s.InputPackets == 1 })
	if typed.InputBytes != 2 || typed.OutputBytes != afterOutput.OutputBytes || typed.Session != start.Session {
		t.Fatalf("input counters: %+v", typed)
	}
	// Stats reads are not participants: viewers saw only the typed input.
	waitFor(t, "peer activity", func() bool { managed.mu.Lock(); defer managed.mu.Unlock(); return len(managed.inputs) == inputs+1 })
	pollStats(t, opts.AttachSocket)
	time.Sleep(50 * time.Millisecond)
	managed.mu.Lock()
	if len(managed.inputs) != inputs+1 {
		t.Errorf("stats read emitted activity: %+v", managed.inputs)
	}
	managed.mu.Unlock()

	managed.detach()
	fs.exit(0)
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
	_ = raw.wait(t)
}

func dialSocket(t *testing.T, sock string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A stats request is answered once and closed; it never becomes a viewer.
func TestStatsRequestAdmitsNoViewer(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.broadcast([]byte("synthetic"))
	h.mu.Lock()
	sequence := h.sequence
	h.mu.Unlock()
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	ln.connections <- server
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	s, err := readStats(peer)
	if err != nil {
		t.Fatal(err)
	}
	if s.OutputBytes != 0 || s.Session != sessionDigest(h.incarnation) || s.Session == h.incarnation {
		t.Fatalf("stats: %+v", s)
	}
	if _, err := readFrame(peer); !errors.Is(err, io.EOF) {
		t.Fatalf("stats connection stayed open: %v", err)
	}
	h.admissions.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) != 0 || h.activity != 0 || h.sequence != sequence || h.inputBytes != 0 {
		t.Fatalf("stats read changed the session: clients=%d activity=%d sequence=%d", len(h.clients), h.activity, h.sequence)
	}
}

func TestStatsRequestRequiresTheExactBuild(t *testing.T) {
	h, ln := fanoutHolder(t)
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		req  string
	}{
		{"bundle", `{"bundle":"other","core":"` + terminal.CoreIdentity() + `"}`},
		{"core", `{"bundle":"` + bundle + `","core":"other"}`},
		{"unknown field", `{"bundle":"` + bundle + `","core":"` + terminal.CoreIdentity() + `","subscribe":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			server, peer := net.Pipe()
			t.Cleanup(func() { _ = peer.Close() })
			ln.connections <- server
			_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
			if f, err := readFrame(peer); err != nil || f.Type != frameHello {
				t.Fatalf("hello: %v", err)
			}
			if err := writeFrame(peer, frameStats, []byte(c.req)); err != nil {
				t.Fatal(err)
			}
			f, err := readFrame(peer)
			if err == nil && f.Type == frameStats {
				t.Fatalf("answered a mismatched request: %s", f.Payload)
			}
		})
	}
	h.admissions.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) != 0 {
		t.Fatal("a refused stats request was admitted")
	}
}

// Accepted participant input counts; empty input does not.
func TestStatsCountAcceptedInput(t *testing.T) {
	h, ln := fanoutHolder(t)
	raw, _ := pipeAttach(t, ln)
	expectAttachFrame(t, raw, frameHello, []byte{protocolVersion})
	for _, p := range []string{"", "abc"} {
		if err := writeFrame(raw, frameData, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "input", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.activity == 1 })
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	ln.connections <- server
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	s, err := readStats(peer)
	if err != nil || s.InputPackets != 1 || s.InputBytes != 3 || s.OutputBytes != 0 {
		t.Fatalf("stats %+v %v", s, err)
	}
}

// A holder from another build closes on the unknown request: evidence is
// unavailable, never zero counters.
func TestReadStatsReportsOlderHolders(t *testing.T) {
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	go func() {
		defer func() { _ = server.Close() }()
		_ = writeFrame(server, frameHello, []byte{protocolVersion})
		_, _ = readFrame(server)
	}()
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	if s, err := readStats(peer); err == nil || s != (Stats{}) {
		t.Fatalf("older holder: %+v %v", s, err)
	}
	server, peer = net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	go func() {
		defer func() { _ = server.Close() }()
		_ = writeFrame(server, frameHello, []byte{protocolVersion})
		_, _ = readFrame(server)
		p, _ := json.Marshal(Stats{Bundle: "other", Session: "s", OutputBytes: 7})
		_ = writeFrame(server, frameStats, p)
	}()
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	if s, err := readStats(peer); err == nil || s != (Stats{}) {
		t.Fatalf("foreign build reply accepted: %+v %v", s, err)
	}
}

// A holder that never answers costs one bounded read, and cancellation ends
// it at once.
func TestReadStatsIsBounded(t *testing.T) {
	dir, err := os.MkdirTemp("", "ys")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = writeFrame(c, frameHello, []byte{protocolVersion})
				_, _ = readFrame(c)
				_, _ = io.Copy(io.Discard, c) // never replies
			}()
		}
	}()
	start := time.Now()
	if _, err := ReadStats(context.Background(), sock); err == nil {
		t.Fatal("silent holder produced stats")
	}
	if elapsed := time.Since(start); elapsed < StatsTimeout/2 || elapsed > StatsTimeout+time.Second {
		t.Fatalf("silent holder read took %v, bound %v", elapsed, StatsTimeout)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if _, err := ReadStats(ctx, sock); err == nil || time.Since(start) > StatsTimeout/2 {
		t.Fatalf("canceled read: %v after %v", err, time.Since(start))
	}
}

// Only a holder's explicit, well-formed answer classifies its build: the
// real holder's refusal of this build, a valid HELLO with another protocol
// version or a complete reply from another bundle match ErrBuildMismatch,
// keeping their messages. A holder that closes, stays silent, refuses for
// another reason or sends a malformed HELLO or an incomplete reply (qa's
// reproductions, including this bundle without a session) is not a mismatch.
func TestReadStatsClassifiesOnlyExplicitBuildAnswers(t *testing.T) {
	h, ln := fanoutHolder(t)
	ask := func() (Stats, error) {
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		ln.connections <- server
		_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
		return readStats(peer)
	}
	if _, err := ask(); err != nil {
		t.Fatalf("this build's holder: %v", err)
	}
	h.bundle = "previous-build" // published to the next admission by the channel send
	if _, err := ask(); !errors.Is(err, ErrBuildMismatch) || err.Error() != "session stats refused: session build/core mismatch" {
		t.Fatalf("previous build's holder: %v", err)
	}
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	reply := func(kind byte, p []byte) func(net.Conn) {
		return func(c net.Conn) {
			_ = writeFrame(c, frameHello, []byte{protocolVersion})
			_, _ = readFrame(c)
			_ = writeFrame(c, kind, p)
		}
	}
	same, _ := json.Marshal(Stats{Bundle: bundle})
	fake := func(serve func(net.Conn)) error {
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		go func() { defer func() { _ = server.Close() }(); serve(server) }()
		_ = peer.SetDeadline(time.Now().Add(time.Second))
		_, err := readStats(peer)
		return err
	}
	for _, c := range []struct {
		name     string
		serve    func(net.Conn)
		mismatch bool
	}{
		{"protocol", func(c net.Conn) { _ = writeFrame(c, frameHello, []byte{protocolVersion - 1}) }, true},
		{"foreign reply", func(c net.Conn) {
			_ = writeFrame(c, frameHello, []byte{protocolVersion})
			_, _ = readFrame(c)
			p, _ := json.Marshal(Stats{Bundle: "other", Session: "s"})
			_ = writeFrame(c, frameStats, p)
		}, true},
		{"closes", func(c net.Conn) { _ = writeFrame(c, frameHello, []byte{protocolVersion}); _, _ = readFrame(c) }, false},
		{"other refusal", func(c net.Conn) {
			_ = writeFrame(c, frameHello, []byte{protocolVersion})
			_, _ = readFrame(c)
			_ = writeFrame(c, frameError, []byte("session terminal unavailable"))
		}, false},
		{"silent", func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }, false},
		{"wrong hello type", func(c net.Conn) { _ = writeFrame(c, frameError, []byte("busy")) }, false},
		{"empty hello", func(c net.Conn) { _ = writeFrame(c, frameHello, nil) }, false},
		{"overlong hello", func(c net.Conn) { _ = writeFrame(c, frameHello, []byte{protocolVersion, 0}) }, false},
		{"this bundle without a session", reply(frameStats, same), false},
		{"missing bundle", reply(frameStats, []byte(`{"session":"s"}`)), false},
		{"empty stats", reply(frameStats, []byte(`{}`)), false},
		{"null stats", reply(frameStats, []byte(`null`)), false},
		{"view refusal text", reply(frameError, []byte(holderMismatchReason+"; quit the old agent session")), false},
		// qa r2: invalid bytes removed for display must not complete the refusal.
		{"refusal with trailing invalid byte", reply(frameError, []byte(holderMismatchReason+"\xff")), false},
		{"refusal with an inner invalid byte", reply(frameError, []byte("session build/core mismatc\xffh")), false},
	} {
		if err := fake(c.serve); err == nil || errors.Is(err, ErrBuildMismatch) != c.mismatch {
			t.Errorf("%s: %v, mismatch want %v", c.name, err, c.mismatch)
		}
	}
}

// A holder incarnation is its PID and start time; an incomplete marker names
// none, so a restart cannot be bound to it.
func TestMarkerIdentityNeedsPIDAndStart(t *testing.T) {
	at := time.Date(2026, 10, 21, 12, 0, 0, 5, time.FixedZone("x", 3600))
	if got := (Marker{HolderPID: 42, StartedAt: at}).Identity(); got != "42@2026-10-21T11:00:00.000000005Z" {
		t.Fatalf("identity %q", got)
	}
	for _, m := range []Marker{{}, {HolderPID: 42}, {StartedAt: at}, {HolderPID: -1, StartedAt: at}} {
		if got := m.Identity(); got != "" {
			t.Errorf("%+v: %q", m, got)
		}
	}
}
