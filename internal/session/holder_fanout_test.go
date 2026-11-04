package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
)

// Pipes make backpressure deterministic without relying on an OS socket buffer
// size. An unread frame blocks Write immediately.
type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (ln *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-ln.connections:
		return c, nil
	case <-ln.closed:
		return nil, net.ErrClosed
	}
}

func (ln *pipeListener) Close() error {
	ln.once.Do(func() { close(ln.closed) })
	return nil
}

func (ln *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

type observedWriteConn struct {
	net.Conn
	writing chan byte
}

func (c *observedWriteConn) Write(p []byte) (int, error) {
	select {
	case c.writing <- p[0]:
	default:
	}
	return c.Conn.Write(p)
}

func fanoutHolder(t *testing.T) (*holder, *pipeListener) {
	t.Helper()
	h := &holder{ring: NewRing(1 << 16), terminalInput: &terminalInputState{}, logf: t.Logf}
	if err := h.terminalInput.Init(); err != nil {
		t.Fatal(err)
	}
	if err := h.initView(24, 80); err != nil {
		t.Fatal(err)
	}
	h.input = make(chan []byte, 128)
	inputDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-inputDone:
				return
			case p := <-h.input:
				if h.stdin != nil {
					_, _ = h.stdin.Write(p)
				}
			}
		}
	}()
	t.Cleanup(func() { close(inputDone) })
	ln := &pipeListener{connections: make(chan net.Conn, 8), closed: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); h.acceptLoop(ln) }()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		h.admissions.Wait()
		h.closeClients(nil)
		h.terminalInput.Close()
		_ = h.vt.Close()
	})
	return h, ln
}

func pipeAttach(t *testing.T, ln *pipeListener) (net.Conn, <-chan byte) {
	t.Helper()
	server, peer := net.Pipe()
	observed := &observedWriteConn{Conn: server, writing: make(chan byte, 4)}
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	ln.connections <- observed
	return &admittedTestConn{peer}, observed.writing
}

// Observe the frame type rather than counting writes: admission adds STATE
// between HELLO and replay, and the writer may reach replay before we resume.
// Four slots retain that prelude without blocking the transport under test.
func waitForWrite(t *testing.T, writing <-chan byte, kind byte) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-writing:
			if got == kind {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for frame %q write", kind)
		}
	}
}

func expectAttachFrame(t *testing.T, c net.Conn, kind byte, data []byte) {
	t.Helper()
	// Below the holder's five-second write timeout: a blocked peer must not
	// need to time out before another peer receives its frame.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := readFrame(c)
	if err != nil {
		t.Fatalf("read attach frame: %v", err)
	}
	if f.Type != kind || !bytes.Equal(f.Payload, data) {
		t.Fatalf("frame = %q %q, want %q %q", f.Type, f.Payload, kind, data)
	}
	if kind == frameHello {
		// Standalone holderClient writer fixtures have no admission reader.
		if _, ok := c.(*admittedTestConn); ok {
			rawAdmission(t, c)
		}
	}

}

type admittedTestConn struct{ net.Conn }

func rawAdmission(t *testing.T, c net.Conn) {
	t.Helper()
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(subscription{Passthrough: true, Bundle: bundle, Core: terminal.CoreIdentity()})
	if err := writeFrame(c, frameSubscribe, p); err != nil {
		t.Fatal(err)
	}
	f, err := readFrame(c)
	if err != nil || f.Type != frameState {
		t.Fatalf("admission: %q %s %v", f.Type, f.Payload, err)
	}
}

func TestHolderSlowBootstrapDoesNotBlockOtherAttaches(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.broadcast([]byte("replay"))
	_, slowWriting := pipeAttach(t, ln)
	waitForWrite(t, slowWriting, frameHello) // first client is blocked writing HELLO
	healthy, _ := pipeAttach(t, ln)
	expectAttachFrame(t, healthy, frameHello, []byte{protocolVersion})
	expectAttachFrame(t, healthy, frameData, []byte("replay"))
	h.broadcast([]byte("live"))
	expectAttachFrame(t, healthy, frameData, []byte("live"))
}

func TestHolderSlowMirrorDoesNotDelayHealthyClient(t *testing.T) {
	h, ln := fanoutHolder(t)
	slow, slowWriting := pipeAttach(t, ln)
	expectAttachFrame(t, slow, frameHello, []byte{protocolVersion})
	waitForWrite(t, slowWriting, frameHello)
	healthy, _ := pipeAttach(t, ln)
	expectAttachFrame(t, healthy, frameHello, []byte{protocolVersion})
	done := make(chan struct{})
	go func() { h.broadcast([]byte("live")); close(done) }()
	waitForWrite(t, slowWriting, frameData) // unread DATA is blocked at the first client
	expectAttachFrame(t, healthy, frameData, []byte("live"))
	<-done
	// The ring and subsequent attaches advance too, while slow remains blocked.
	late, _ := pipeAttach(t, ln)
	expectAttachFrame(t, late, frameHello, []byte{protocolVersion})
	expectAttachFrame(t, late, frameData, []byte("live"))
}

func TestHolderReplayPrecedesOwnedLiveOutputAndExit(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.broadcast([]byte("replay"))
	peer, writing := pipeAttach(t, ln)
	expectAttachFrame(t, peer, frameHello, []byte{protocolVersion})
	waitForWrite(t, writing, frameData) // replay is blocked before any live output is emitted
	live := []byte("live")
	h.broadcast(live)
	copy(live, "xxxx") // the SSH output pump reuses its read buffer too
	done := make(chan struct{})
	go func() { h.closeClients(encodeExit(7)); close(done) }()
	expectAttachFrame(t, peer, frameData, []byte("replay"))
	expectAttachFrame(t, peer, frameData, []byte("live"))
	expectAttachFrame(t, peer, frameExit, encodeExit(7))
	if _, err := readFrame(peer); err != io.EOF {
		t.Fatalf("after EXIT: %v, want EOF", err)
	}
	<-done
}

type closedAgentInput struct {
	attempted chan struct{}
	once      sync.Once
}

func (w *closedAgentInput) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.attempted) })
	return 0, io.ErrClosedPipe
}

func (*closedAgentInput) Close() error { return nil }

func TestHolderInputClosureKeepsFinalOutputAndExit(t *testing.T) {
	h, ln := fanoutHolder(t)
	input := &closedAgentInput{attempted: make(chan struct{})}
	h.stdin = input
	peer, _ := pipeAttach(t, ln)
	expectAttachFrame(t, peer, frameHello, []byte{protocolVersion})
	// The SSH stdin may close before final output/status reaches this client.
	// Typing in that window must not turn a known agent exit into a lost attach.
	if err := writeFrame(peer, frameData, []byte("late key")); err != nil {
		t.Fatal(err)
	}
	<-input.attempted
	if err := writeFrame(peer, frameData, []byte("another late key")); err != nil {
		t.Fatal(err)
	}
	h.broadcast([]byte("final output"))
	done := make(chan struct{})
	go func() { h.closeClients(encodeExit(7)); close(done) }()
	expectAttachFrame(t, peer, frameData, []byte("final output"))
	expectAttachFrame(t, peer, frameExit, encodeExit(7))
	<-done
}

func TestHolderOverflowDisconnectsOnlySlowClient(t *testing.T) {
	h, ln := fanoutHolder(t)
	slow, writing := pipeAttach(t, ln)
	expectAttachFrame(t, slow, frameHello, []byte{protocolVersion})
	waitForWrite(t, writing, frameHello)
	h.mu.Lock()
	slowClient := h.clients[0]
	h.mu.Unlock()
	// Fill just this outbound queue. The holder's VT need not parse 4 MiB to
	// establish the queue/transport isolation contract.
	if !slowClient.enqueue(make([]byte, maxClientOutputBytes)) {
		t.Fatal("empty queue rejected its byte limit")
	}
	waitForWrite(t, writing, frameData) // first DATA is in flight and still counts toward the limit
	healthy, _ := pipeAttach(t, ln)
	expectAttachFrame(t, healthy, frameHello, []byte{protocolVersion})
	h.broadcast([]byte("after overflow"))
	expectAttachFrame(t, healthy, frameData, []byte("after overflow"))
	_ = slow.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := readFrame(slow); err != io.EOF {
		t.Fatalf("overflowed transport = %v, want EOF without invented EXIT", err)
	}
	<-slowClient.done
	h.broadcast([]byte("still live"))
	expectAttachFrame(t, healthy, frameData, []byte("still live"))
}

func TestHolderShutdownDrainsHealthyClientWithSharedDeadline(t *testing.T) {
	h, ln := fanoutHolder(t)
	for range 2 {
		_, writing := pipeAttach(t, ln)
		waitForWrite(t, writing, frameHello) // two clients stalled at HELLO
	}
	healthy, _ := pipeAttach(t, ln)
	expectAttachFrame(t, healthy, frameHello, []byte{protocolVersion})
	h.mu.Lock()
	clients := append([]*holderClient(nil), h.clients...)
	h.mu.Unlock()
	h.broadcast([]byte("final output"))
	done := make(chan struct{})
	go func() { h.closeClients(encodeExit(7)); close(done) }()
	expectAttachFrame(t, healthy, frameData, []byte("final output"))
	expectAttachFrame(t, healthy, frameExit, encodeExit(7))
	// Receiving EXIT proves finish visited every client. All writers, including
	// the blocked bootstraps, received exactly the same shutdown budget.
	var deadline time.Time
	for _, c := range clients {
		c.mu.Lock()
		drainBy := c.drainBy
		c.mu.Unlock()
		if drainBy.IsZero() || (!deadline.IsZero() && !drainBy.Equal(deadline)) {
			t.Fatalf("shutdown deadlines differ: %v and %v", deadline, drainBy)
		}
		deadline = drainBy
	}
	// Abort the stalled peers instead of spending five seconds waiting here.
	// The deadline itself is exercised below with real pipe writes.
	for _, c := range clients {
		c.abort()
	}
	<-done
}

func TestHolderConcurrentAttachHasNoOutputGapOrDuplicate(t *testing.T) {
	h, ln := fanoutHolder(t)
	type result struct {
		output []byte
		err    error
	}
	var readers []<-chan result
	var want bytes.Buffer
	for i := range 128 {
		p := fmt.Appendf(nil, "output-%03d;", i)
		want.Write(p)
		h.broadcast(p)
		if i%16 == 0 {
			peer, _ := pipeAttach(t, ln)
			out := make(chan result, 1)
			readers = append(readers, out)
			go func() {
				_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
				if f, err := readFrame(peer); err != nil || f.Type != frameHello {
					out <- result{err: fmt.Errorf("HELLO = %+v: %v", f, err)}
					return
				}
				rawAdmission(t, peer)
				var data []byte
				for {
					f, err := readFrame(peer)
					if err != nil {
						out <- result{err: err}
						return
					}
					switch f.Type {
					case frameData:
						data = append(data, f.Payload...)
					case frameExit:
						if !bytes.Equal(f.Payload, encodeExit(0)) {
							out <- result{err: fmt.Errorf("EXIT = %v", f.Payload)}
						} else {
							out <- result{output: data}
						}
						return
					default:
						out <- result{err: fmt.Errorf("unexpected frame: %q", f.Type)}
						return
					}
				}
			}()
		}
	}
	h.closeClients(encodeExit(0))
	for i, ch := range readers {
		r := <-ch
		if r.err != nil || !bytes.Equal(r.output, want.Bytes()) {
			t.Errorf("client %d: %v; got %q, want %q", i, r.err, r.output, want.Bytes())
		}
	}
}

func TestHolderClientBoundsIncludeInFlightOutput(t *testing.T) {
	for _, kind := range []string{"bytes", "packets"} {
		t.Run(kind, func(t *testing.T) {
			conn, peer := net.Pipe()
			observed := &observedWriteConn{Conn: conn, writing: make(chan byte, 4)}
			c := newHolderClient(observed)
			go func() { defer close(c.done); _ = c.writeLoop(nil) }()
			t.Cleanup(func() { c.abort(); _ = peer.Close(); <-c.done })
			expectAttachFrame(t, peer, frameHello, []byte{protocolVersion})
			waitForWrite(t, observed.writing, frameHello)
			p := []byte("x")
			if kind == "bytes" {
				p = make([]byte, maxClientOutputBytes)
			}
			if !c.enqueue(p) {
				t.Fatal("first packet rejected")
			}
			waitForWrite(t, observed.writing, frameData)
			if kind == "packets" {
				for range maxClientOutputPackets - 1 {
					if !c.enqueue(p) {
						t.Fatal("packet rejected before the limit")
					}
				}
			}
			if c.enqueue([]byte("overflow")) {
				t.Fatal("accepted output over the bound; in-flight data must count")
			}
		})
	}
}

func TestHolderClientFinishBoundsStalledWrite(t *testing.T) {
	for _, phase := range []string{"bootstrap", "live", "exit"} {
		t.Run(phase, func(t *testing.T) {
			conn, peer := net.Pipe()
			observed := &observedWriteConn{Conn: conn, writing: make(chan byte, 4)}
			c := newHolderClient(observed)
			go func() { defer close(c.done); _ = c.writeLoop(nil) }()
			t.Cleanup(func() { c.abort(); _ = peer.Close(); <-c.done })
			if phase == "bootstrap" {
				waitForWrite(t, observed.writing, frameHello)
			} else {
				expectAttachFrame(t, peer, frameHello, []byte{protocolVersion})
				waitForWrite(t, observed.writing, frameHello)
				if phase == "live" {
					if !c.enqueue([]byte("live")) {
						t.Fatal("live packet rejected")
					}
					waitForWrite(t, observed.writing, frameData)
				}
			}
			c.finish(encodeExit(0), time.Now().Add(100*time.Millisecond))
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
				t.Fatal("writer extended its shutdown deadline")
			}
		})
	}
}
