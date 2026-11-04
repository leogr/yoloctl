package openshell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func forwardTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// A deliberately small SSH peer reads each request through SSH EOF before
// writing its answer. It checks the on-wire target and doesn't reuse the
// production relay. Both directions must preserve independent EOFs to pass.
func forwardTestPeer(t *testing.T, port uint16) (ForwardTarget, *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("", "yof-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	signer := forwardTestSigner(t)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	var accepted atomic.Int32
	var workers sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					_ = conn.Close()
					mu.Lock()
					delete(connections, conn)
					mu.Unlock()
				}()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for next := range channels {
					var payload struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if next.ChannelType() != "direct-tcpip" || ssh.Unmarshal(next.ExtraData(), &payload) != nil || payload.Host != "127.0.0.1" || payload.Port != uint32(port) || payload.OriginHost != "127.0.0.1" || payload.OriginPort != 0 {
						_ = next.Reject(ssh.Prohibited, "target is not the declared loopback port")
						continue
					}
					channel, requests, err := next.Accept()
					if err != nil {
						return
					}
					accepted.Add(1)
					go ssh.DiscardRequests(requests)
					request, err := io.ReadAll(channel)
					if err == nil {
						_, _ = channel.Write(append([]byte("response:"), request...))
					}
					_ = channel.CloseWrite()
					_ = channel.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return ForwardTarget{socket, port, signer.PublicKey()}, &accepted
}

func startTestForward(t *testing.T, target ForwardTarget, verify func(context.Context) error) (net.Listener, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeForward(ctx, listener, target, verify) }()
	t.Cleanup(cancel)
	return listener, cancel, done
}

func forwardRequest(address, input string) (string, error) {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if _, err = io.WriteString(conn, input); err != nil {
		return "", err
	}
	if err = conn.(*net.TCPConn).CloseWrite(); err != nil {
		return "", err
	}
	response, err := io.ReadAll(conn)
	return string(response), err
}

func waitForwardStopped(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forward leaked an accept/connection handler after cancellation")
	}
}

func TestForwardParallelConnectionsPreserveHalfCloseAndReconnect(t *testing.T) {
	target, accepted := forwardTestPeer(t, 32000)
	var verified atomic.Int32
	listener, cancel, done := startTestForward(t, target, func(context.Context) error { verified.Add(1); return nil })
	var workers sync.WaitGroup
	for n := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			input := fmt.Sprintf("request-%d", n)
			response, err := forwardRequest(listener.Addr().String(), input)
			if err != nil || response != "response:"+input {
				t.Errorf("response %q, error %v", response, err)
			}
		}()
	}
	workers.Wait()
	if verified.Load() != 8 || accepted.Load() != 8 {
		t.Fatal("connections did not use separately verified SSH transports")
	}
	response, err := forwardRequest(listener.Addr().String(), "later")
	if err != nil || response != "response:later" {
		t.Fatal("later connection failed", response, err)
	}
	cancel()
	waitForwardStopped(t, done)
}

func TestForwardRejectsChangedRuntimeOrHostKeyBeforeOpeningChannel(t *testing.T) {
	for _, changed := range []string{"generation", "host-key"} {
		t.Run(changed, func(t *testing.T) {
			target, accepted := forwardTestPeer(t, 32000)
			if changed == "host-key" {
				target.HostKey = forwardTestSigner(t).PublicKey()
			}
			verify := func(context.Context) error {
				if changed == "generation" {
					return errors.New("incarnation changed")
				}
				return nil
			}
			listener, cancel, done := startTestForward(t, target, verify)
			response, _ := forwardRequest(listener.Addr().String(), "refuse")
			if response != "" || accepted.Load() != 0 {
				t.Fatal("unverified endpoint received a channel")
			}
			cancel()
			waitForwardStopped(t, done)
		})
	}
}

func TestForwardCancellationClosesActiveStreams(t *testing.T) {
	target, accepted := forwardTestPeer(t, 32000)
	listener, cancel, done := startTestForward(t, target, func(context.Context) error { return nil })
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "no-EOF")
	limit := time.Now().Add(5 * time.Second)
	for accepted.Load() == 0 && time.Now().Before(limit) {
		time.Sleep(time.Millisecond)
	}
	if accepted.Load() == 0 {
		t.Fatal("active stream did not reach SSH peer")
	}
	cancel()
	waitForwardStopped(t, done)
	response, err := io.ReadAll(conn)
	if len(response) != 0 || err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("canceled stream: %q, %v", response, err)
	}
}

func TestForwardRejectsMissingOrPeerSelectedTargets(t *testing.T) {
	key := forwardTestSigner(t).PublicKey()
	for _, target := range []ForwardTarget{{}, {"relative", 80, key}, {"/a/../b", 80, key}, {"/", 80, key}, {"/private.sock", 0, key}, {"/private.sock", 80, nil}} {
		if target.validate() == nil {
			t.Fatal("invalid forward target accepted")
		}
	}
}
