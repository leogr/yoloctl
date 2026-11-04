//go:build linux && amd64

package firecracker

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/sshtransport"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/crypto/ssh"
)

// inventoryGuest stands in for guest sshd: it counts authenticated
// connections, each of which sshd would log to vm.log.
type inventoryGuest struct {
	mu       sync.Mutex
	conns    []net.Conn
	accepted int
	commands []string
	reply    string
	exit     uint32
	hang     bool
}

func startInventoryGuest(t *testing.T) (*inventoryGuest, func(context.Context) (*ssh.Client, error)) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	g := &inventoryGuest{reply: `[{"protocol":"tcp","number":8080}]`}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); g.drop() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go g.serve(conn, config)
		}
	}()
	client := &ssh.ClientConfig{User: "yolo", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())}
	return g, func(ctx context.Context) (*ssh.Client, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		return sshtransport.Connect(ctx, conn, client)
	}
}

func (g *inventoryGuest) serve(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	g.mu.Lock()
	g.conns = append(g.conns, conn)
	g.accepted++
	g.mu.Unlock()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		c, reqs, err := channel.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			for req := range reqs {
				var exec struct{ Command string }
				if req.Type != "exec" || ssh.Unmarshal(req.Payload, &exec) != nil {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				g.mu.Lock()
				g.commands = append(g.commands, exec.Command)
				reply, exit, hang := g.reply, g.exit, g.hang
				g.mu.Unlock()
				if hang {
					for range reqs {
					}
					return
				}
				_, _ = io.WriteString(c, reply)
				_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
				return
			}
		}()
	}
}

func (g *inventoryGuest) drop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conns {
		_ = c.Close()
	}
	g.conns = nil
}

func (g *inventoryGuest) set(update func(*inventoryGuest)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	update(g)
}

func (g *inventoryGuest) connections() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.accepted
}

// Guest sshd logs each accepted connection to vm.log, so the once-per-second
// port poll must not open a connection per poll.
func TestPortInventoryReusesOneConnection(t *testing.T) {
	g, dial := startInventoryGuest(t)
	q := &portInventory{dial: dial}
	defer q.close()
	poll := func(timeout time.Duration) ([]vmwire.Port, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return q.query(ctx)
	}
	want := vmwire.Port{Protocol: "tcp", Number: 8080}
	for i := 0; i < 5; i++ {
		ports, err := poll(3 * time.Second)
		if err != nil || len(ports) != 1 || ports[0] != want {
			t.Fatalf("poll %d: %v %v", i, ports, err)
		}
	}
	if n := g.connections(); n != 1 {
		t.Fatalf("five polls used %d connections", n)
	}
	g.mu.Lock()
	for _, command := range g.commands {
		if command != "'sudo' '/sbin/yoloctl-init' 'ports'" {
			t.Errorf("remote command changed: %q", command)
		}
	}
	g.mu.Unlock()

	// A failed or oversized guest reply is skipped on the same connection.
	g.set(func(g *inventoryGuest) { g.exit = 1 })
	var exit *ssh.ExitError
	if _, err := poll(3 * time.Second); !errors.As(err, &exit) {
		t.Fatalf("remote failure: %v", err)
	}
	g.set(func(g *inventoryGuest) { g.exit, g.reply = 0, strings.Repeat(" ", maxPortInventory+1) })
	if _, err := poll(3 * time.Second); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized inventory: %v", err)
	}
	g.set(func(g *inventoryGuest) { g.reply = "[]" })
	if ports, err := poll(3 * time.Second); err != nil || len(ports) != 0 {
		t.Fatalf("empty inventory: %v %v", ports, err)
	}
	if n := g.connections(); n != 1 {
		t.Fatalf("remote failures reconnected: %d connections", n)
	}

	// Transport loss reconnects once, on the following poll.
	g.drop()
	if _, err := poll(3 * time.Second); err == nil {
		t.Fatal("poll over a dropped connection succeeded")
	}
	if _, err := poll(3 * time.Second); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if n := g.connections(); n != 2 {
		t.Fatalf("transport loss used %d connections", n)
	}

	// A hung session is bounded by the poll timeout and closes its connection.
	g.set(func(g *inventoryGuest) { g.hang = true })
	start := time.Now()
	if _, err := poll(100 * time.Millisecond); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("hung poll: %v after %v", err, time.Since(start))
	}
	if q.client != nil {
		t.Fatal("timed-out connection retained")
	}
	g.set(func(g *inventoryGuest) { g.hang = false })
	if _, err := poll(3 * time.Second); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if n := g.connections(); n != 3 {
		t.Fatalf("timeout recovery used %d connections", n)
	}
}

func TestForwardRoundTripAndCancellation(t *testing.T) {
	for _, mode := range []string{"tcp", "tcp-stream", "udp"} {
		t.Run(mode, func(t *testing.T) {
			protocol := mode
			if mode == "tcp-stream" {
				protocol = "tcp"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := t.TempDir()
			guest, err := net.Listen("unix", filepath.Join(runtime, "vsock"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = guest.Close() }()
			// Reserve a host port without relying on a global fixed fixture port.
			reservation, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := vmwire.Port{Protocol: protocol, Number: uint16(reservation.Addr().(*net.TCPAddr).Port)}
			_ = reservation.Close()
			done := make(chan error, 1)
			go func() {
				c, e := guest.Accept()
				if e != nil {
					done <- e
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				r := bufio.NewReader(c)
				line, e := r.ReadString('\n')
				if e != nil || line != "CONNECT 5254\n" {
					done <- fmt.Errorf("vsock request: %q %v", line, e)
					return
				}
				_, _ = io.WriteString(c, "OK 1\n")
				line, e = r.ReadString('\n')
				var request vmwire.ForwardRequest
				if e != nil || json.Unmarshal([]byte(line), &request) != nil || request.Port != port || request.TCPStream != (protocol == "tcp") {
					done <- fmt.Errorf("forward request: %q %v", line, e)
					return
				}
				if mode == "tcp-stream" {
					_, _ = c.Write([]byte{vmwire.ForwardStreamOK})
					stream := vmwire.NewTCPStream(c, r)
					payload, err := io.ReadAll(stream)
					if err != nil || string(payload) != "ping" {
						done <- fmt.Errorf("framed payload/EOF: %q %v", payload, err)
						return
					}
					if _, err = stream.Write([]byte("pong")); err == nil {
						err = stream.CloseWrite()
					}
					done <- err
					return
				}
				_, _ = c.Write([]byte{vmwire.ForwardOK})
				payload := make([]byte, 4)
				if protocol == "udp" {
					var header [2]byte
					if _, e = io.ReadFull(r, header[:]); e != nil || binary.BigEndian.Uint16(header[:]) != 4 {
						done <- fmt.Errorf("datagram framing: %v", e)
						return
					}
				}
				if _, e = io.ReadFull(r, payload); e != nil || string(payload) != "ping" {
					done <- fmt.Errorf("payload: %q %v", payload, e)
					return
				}
				if protocol == "udp" {
					_, _ = c.Write([]byte{0, 4})
				}
				_, _ = io.WriteString(c, "pong")
				_, e = r.ReadByte()
				if e != io.EOF {
					done <- fmt.Errorf("unforward left connection open: %v", e)
					return
				}
				done <- nil
			}()
			binding, err := exposePort(ctx, runtime, port)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = binding.Close() }()
			client, err := net.Dial(protocol, fmt.Sprintf("127.0.0.1:%d", port.Number))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = io.WriteString(client, "ping"); err != nil {
				t.Fatal(err)
			}
			if mode == "tcp-stream" {
				if err := client.(*net.TCPConn).CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			response := make([]byte, 4)
			if _, err = io.ReadFull(client, response); err != nil || string(response) != "pong" {
				t.Fatalf("response: %q %v", response, err)
			}
			_ = binding.Close()
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
