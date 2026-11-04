package openshell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ForwardTarget is selected by the native controller from one declared port
// and the verified generation's SSH host key. Every accepted TCP connection
// gets its own SSH transport and one fixed loopback direct-tcpip channel. There
// is no peer-selected address, command, authentication or forwarding request.
// Socket ownership, incarnation and generation checks belong to the caller.
type ForwardTarget struct {
	SocketPath string
	Port       uint16
	HostKey    ssh.PublicKey
}

// Validate checks only the fixed transport's data shape. It is not native
// authority, endpoint readiness or a replacement for per-connection checks.
func (t ForwardTarget) Validate() error { return t.validate() }

func (t ForwardTarget) validate() error {
	if !path.IsAbs(t.SocketPath) || path.Clean(t.SocketPath) != t.SocketPath || t.SocketPath == "/" || t.Port == 0 || t.HostKey == nil || t.HostKey.Type() != ssh.KeyAlgoED25519 {
		return errors.New("forward requires a protected socket, declared TCP port and pinned Ed25519 runtime key")
	}
	return nil
}

// ServeForward owns listener until ctx ends or Accept fails. It drains all
// connection handlers before returning. A broken target affects only its
// incoming connection; a later connection establishes a fresh SSH transport.
// validateLive must recheck the root-owned generation/incarnation binding on
// every connection before it can open the protected endpoint.
func ServeForward(ctx context.Context, listener net.Listener, target ForwardTarget, validateLive func(context.Context) error) error {
	if err := target.validate(); err != nil {
		return err
	}
	if listener == nil || validateLive == nil {
		return errors.New("forward requires a listener and live runtime verifier")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(serveCtx, func() { _ = listener.Close() })
	defer stop()
	defer func() { _ = listener.Close() }()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	// Bound unauthenticated inbound resource use without serializing active
	// streams or holding any controller/session state lock.
	slots := make(chan struct{}, 128)
	for {
		conn, err := listener.Accept()
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-slots }()
			defer func() { _ = conn.Close() }()
			closeOnCancel := context.AfterFunc(serveCtx, func() { _ = conn.Close() })
			defer closeOnCancel()
			setup, setupCancel := context.WithTimeout(serveCtx, 5*time.Second)
			defer setupCancel()
			if validateLive(setup) != nil {
				return
			}
			client, channel, err := target.connect(setup)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()
			defer func() { _ = channel.Close() }()
			stopSSH := context.AfterFunc(serveCtx, func() { _ = client.Close() })
			defer stopSSH()
			setupCancel()
			_ = relayForward(conn, channel)
		}()
	}
}

func (t ForwardTarget) connect(ctx context.Context) (*ssh.Client, ssh.Channel, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", t.SocketPath)
	if err != nil {
		return nil, nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		_ = conn.Close()
		return nil, nil, errors.New("forward setup requires a deadline")
	}
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	config := &ssh.ClientConfig{User: "sandbox", HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), t.HostKey.Marshal()) {
			return errors.New("forward endpoint does not match the pinned runtime SSH key")
		}
		return nil
	}}
	transport, channels, requests, err := ssh.NewClientConn(conn, t.SocketPath, config)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	client := ssh.NewClient(transport, channels, requests)
	payload := struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}{"127.0.0.1", uint32(t.Port), "127.0.0.1", 0}
	channel, reqs, err := client.OpenChannel("direct-tcpip", ssh.Marshal(payload))
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	go ssh.DiscardRequests(reqs)
	if err = ctx.Err(); err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, channel, nil
}

func relayForward(conn net.Conn, channel ssh.Channel) error {
	// Closing one writing half sends EOF and still permits a response in the
	// other direction. Closing the entire SSH channel here would truncate
	// servers that read the request to EOF before producing their response.
	results := make(chan error, 2)
	go func() {
		_, err := io.Copy(channel, conn)
		if closeErr := channel.CloseWrite(); err == nil {
			err = closeErr
		}
		results <- err
	}()
	go func() {
		_, err := io.Copy(conn, channel)
		if tcp, ok := conn.(*net.TCPConn); ok {
			if closeErr := tcp.CloseWrite(); err == nil {
				err = closeErr
			}
		} else if err == nil {
			err = errors.New("forward listener did not produce a TCP connection")
		}
		results <- err
	}()
	first := <-results
	if first != nil {
		_ = channel.Close()
		_ = conn.Close()
	}
	second := <-results
	if first != nil || second != nil {
		return fmt.Errorf("forward stream: %w", errors.Join(first, second))
	}
	return nil
}
