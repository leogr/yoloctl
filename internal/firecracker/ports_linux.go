//go:build linux && amd64

package firecracker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/crypto/ssh"
)

func dialPort(ctx context.Context, runtime string, p vmwire.Port) (net.Conn, error) {
	c, e := DialGuest(ctx, filepath.Join(runtime, "vsock"), vmwire.ForwardPort)
	if e != nil {
		return nil, e
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if e = json.NewEncoder(c).Encode(vmwire.ForwardRequest{Port: p, TCPStream: p.Protocol == "tcp"}); e != nil {
		_ = c.Close()
		return nil, e
	}
	var ack [1]byte
	if _, e = io.ReadFull(c, ack[:]); e != nil || (ack[0] != vmwire.ForwardOK && (ack[0] != vmwire.ForwardStreamOK || p.Protocol != "tcp")) {
		_ = c.Close()
		return nil, errors.New("guest loopback port is unavailable")
	}
	_ = c.SetDeadline(time.Time{})
	if ack[0] == vmwire.ForwardStreamOK {
		return vmwire.NewTCPStream(c, c), nil
	}
	return c, nil
}

// portInventory polls the guest's listening ports for the helper. Guest sshd
// logs every accepted connection on the VM console, which the helper appends
// to vm.log; a connection per one-second poll flooded that log and repeated a
// full handshake each time. Polls therefore share one authenticated connection
// and open a session each. Only a transport failure or timeout reconnects; a
// failed remote command keeps the connection.
type portInventory struct {
	dial   func(context.Context) (*ssh.Client, error)
	client *ssh.Client
}

var portInventoryCommand = remoteCommand("", []string{"sudo", "/sbin/yoloctl-init", "ports"})

// Every TCP and UDP port together encode to under 5 MiB.
const maxPortInventory = 8 << 20

func (q *portInventory) query(ctx context.Context) ([]vmwire.Port, error) {
	if q.client == nil {
		client, err := q.dial(ctx)
		if err != nil {
			return nil, err
		}
		q.client = client
	}
	client := q.client
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	out := &boundedOutput{limit: maxPortInventory}
	err := runPortInventory(client, out)
	var exit *ssh.ExitError
	if !stop() || (err != nil && !errors.As(err, &exit)) {
		q.close()
	}
	if err != nil {
		return nil, err
	}
	if out.overflow {
		return nil, errors.New("guest port inventory is too large")
	}
	var ports []vmwire.Port
	if err = json.Unmarshal(out.data, &ports); err != nil {
		return nil, err
	}
	return ports, nil
}

func (q *portInventory) close() {
	if q.client != nil {
		_ = q.client.Close()
		q.client = nil
	}
}

func runPortInventory(client *ssh.Client, out io.Writer) error {
	s, err := client.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	s.Stdout = out
	return s.Run(portInventoryCommand)
}

// boundedOutput keeps draining the session past its limit, so an oversized
// reply cannot stall the channel; the result is rejected instead.
type boundedOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.overflow || len(b.data)+len(p) > b.limit {
		b.overflow, b.data = true, nil
	} else {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

// forwardPorts never polls a VM paused for host storage pressure.
func forwardPorts(ctx context.Context, dir, runtime string, paused func() bool) {
	inventory := &portInventory{dial: func(ctx context.Context) (*ssh.Client, error) {
		return dialGuestSSH(ctx, dir, runtime)
	}}
	defer inventory.close()
	active := map[vmwire.Port]io.Closer{}
	warned := map[vmwire.Port]bool{}
	defer func() {
		for _, c := range active {
			_ = c.Close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if paused() {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		ports, e := inventory.query(probe)
		cancel()
		if e != nil {
			continue
		}
		wanted := map[vmwire.Port]bool{}
		for _, p := range ports {
			if p.Number == 0 || (p.Protocol != "tcp" && p.Protocol != "udp") {
				continue
			}
			wanted[p] = true
			if active[p] != nil {
				continue
			}
			closer, e := exposePort(ctx, runtime, p)
			if e != nil {
				if !warned[p] {
					slog.Warn(fmt.Sprintf("Cannot forward guest %s/%d on host loopback", p.Protocol, p.Number), "actor", "vm", "executor", "firecracker", "err", e)
					warned[p] = true
				}
				continue
			}
			active[p] = closer
			delete(warned, p)
		}
		for p, c := range active {
			if !wanted[p] {
				_ = c.Close()
				delete(active, p)
				delete(warned, p)
			}
		}
	}
}

type portBinding struct {
	io.Closer
	cancel context.CancelFunc
}

func (p portBinding) Close() error {
	p.cancel()
	return p.Closer.Close()
}

func exposePort(ctx context.Context, runtime string, p vmwire.Port) (io.Closer, error) {
	ctx, cancel := context.WithCancel(ctx)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(p.Number)))
	if p.Protocol == "tcp" {
		ln, e := net.Listen("tcp", addr)
		if e != nil {
			cancel()
			return nil, e
		}
		context.AfterFunc(ctx, func() { _ = ln.Close() })
		go func() {
			for {
				host, e := ln.Accept()
				if e != nil {
					return
				}
				go func() {
					defer func() { _ = host.Close() }()
					stop := context.AfterFunc(ctx, func() { _ = host.Close() })
					defer stop()
					guest, e := dialPort(ctx, runtime, p)
					if e != nil {
						return
					}
					defer func() { _ = guest.Close() }()
					stopGuest := context.AfterFunc(ctx, func() { _ = guest.Close() })
					defer stopGuest()
					vmwire.BridgeTCP(host, guest)
				}()
			}
		}()
		return portBinding{Closer: ln, cancel: cancel}, nil
	}
	local, e := net.ResolveUDPAddr("udp", addr)
	if e != nil {
		cancel()
		return nil, e
	}
	ln, e := net.ListenUDP("udp", local)
	if e != nil {
		cancel()
		return nil, e
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	go func() {
		var mu sync.Mutex
		peers := map[string]net.Conn{}
		defer func() {
			mu.Lock()
			defer mu.Unlock()
			for _, c := range peers {
				_ = c.Close()
			}
		}()
		buf := make([]byte, 65535)
		for {
			n, peer, e := ln.ReadFromUDP(buf[2:])
			if e != nil {
				return
			}
			key := peer.String()
			mu.Lock()
			c := peers[key]
			if c == nil && len(peers) < 128 {
				c, e = dialPort(ctx, runtime, p)
				if e == nil {
					peers[key] = c
					go func(c net.Conn, peer *net.UDPAddr, key string) {
						defer func() { _ = c.Close(); mu.Lock(); delete(peers, key); mu.Unlock() }()
						var header [2]byte
						data := make([]byte, 65535)
						for {
							_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
							if _, e := io.ReadFull(c, header[:]); e != nil {
								return
							}
							n := int(binary.BigEndian.Uint16(header[:]))
							if n > 65507 {
								return
							}
							if _, e := io.ReadFull(c, data[:n]); e != nil {
								return
							}
							if _, e := ln.WriteToUDP(data[:n], peer); e != nil {
								return
							}
						}
					}(c, peer, key)
				}
			}
			mu.Unlock()
			if c == nil {
				continue
			}
			binary.BigEndian.PutUint16(buf[:2], uint16(n))
			_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, e = c.Write(buf[:n+2]); e != nil {
				_ = c.Close()
			}
		}
	}()
	return portBinding{Closer: ln, cancel: cancel}, nil
}
