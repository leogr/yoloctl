//go:build linux

package vmguest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestForwardSetupAllowsSlowRequestDialAndAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		guest, host := net.Pipe()
		target, app := net.Pipe()
		defer func() { _ = host.Close(); _ = app.Close() }()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveForward(guest, func(ctx context.Context, network, address string) (net.Conn, error) {
				if network != "udp" || address != "127.0.0.1:1234" {
					t.Errorf("dial %s %s", network, address)
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(6 * time.Second):
					return target, nil
				}
			})
		}()
		time.Sleep(50 * time.Second)
		// The first frame shares the request packet, so it must survive the
		// buffered setup read and reach the target exactly once.
		if _, err := io.WriteString(host, "{\"protocol\":\"udp\",\"number\":1234}\n\x00\x03one"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(80 * time.Second)
		raw := make([]byte, 1)
		if _, err := io.ReadFull(host, raw); err != nil || raw[0] != vmwire.ForwardOK {
			t.Fatalf("late ACK %v: %v", raw, err)
		}
		payload := make([]byte, 3)
		if _, err := io.ReadFull(app, payload); err != nil || string(payload) != "one" {
			t.Fatalf("first datagram %q: %v", payload, err)
		}
		// Move past the setup write deadline with live UDP activity.
		for range 2 {
			time.Sleep(25 * time.Second)
			go func() { _, _ = io.WriteString(app, "ok") }()
			frame := make([]byte, 4)
			if _, err := io.ReadFull(host, frame); err != nil || string(frame) != "\x00\x02ok" {
				t.Fatalf("datagram frame %q: %v", frame, err)
			}
		}
		_ = host.Close()
		<-done
	})
}

func TestForwardSetupExpiryAndPeerClose(t *testing.T) {
	for _, phase := range []string{"intake", "dial", "refused", "ack", "closed", "invalid"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				guest, host := net.Pipe()
				target, app := net.Pipe()
				defer func() { _ = host.Close(); _ = app.Close(); _ = target.Close() }()
				calls := 0
				done := make(chan struct{})
				go func() {
					defer close(done)
					serveForward(guest, func(ctx context.Context, _, _ string) (net.Conn, error) {
						calls++
						switch phase {
						case "dial":
							<-ctx.Done()
							return nil, ctx.Err()
						case "refused":
							return nil, errors.New("refused")
						default:
							return target, nil
						}
					})
				}()
				start := time.Now()
				switch phase {
				case "intake":
					_, _ = io.WriteString(host, "{")
				case "invalid":
					_, _ = io.WriteString(host, "{\"protocol\":\"udp\",\"number\":0}\n")
				default:
					_, _ = io.WriteString(host, "{\"protocol\":\"udp\",\"number\":1234}\n")
				}
				switch phase {
				case "closed":
					_ = host.Close()
				case "dial", "refused":
					raw := make([]byte, 1)
					if _, err := io.ReadFull(host, raw); err != nil || raw[0] != 1 {
						t.Fatalf("refusal %v: %v", raw, err)
					}
				}
				<-done
				want := time.Duration(0)
				if phase == "intake" || phase == "dial" || phase == "ack" {
					want = 2 * time.Minute
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Fatalf("elapsed %v, want %v", elapsed, want)
				}
				noDial := phase == "intake" || phase == "invalid"
				if noDial && calls != 0 || !noDial && calls != 1 {
					t.Fatalf("%d dials", calls)
				}
				if phase == "ack" || phase == "closed" {
					if _, err := app.Read(make([]byte, 1)); err != io.EOF {
						t.Fatalf("target retained: %v", err)
					}
				}
			})
		})
	}
}

func TestForwardTCPHalfClosePreservesReply(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_ = ln.SetDeadline(time.Now().Add(30 * time.Second))
	result := make(chan error, 1)
	go func() {
		conn, err := ln.AcceptTCP()
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		if raw, err := io.ReadAll(conn); err != nil || string(raw) != "request" {
			t.Errorf("app received %q: %v", raw, err)
			result <- err
			return
		}
		_, err = io.WriteString(conn, "reply after EOF")
		result <- err
	}()
	guest, host := net.Pipe()
	defer func() { _ = host.Close() }()
	_ = host.SetDeadline(time.Now().Add(30 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveForward(guest, (&net.Dialer{}).DialContext)
	}()
	if err := json.NewEncoder(host).Encode(vmwire.ForwardRequest{
		Port: vmwire.Port{Protocol: "tcp", Number: uint16(ln.Addr().(*net.TCPAddr).Port)}, TCPStream: true,
	}); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 1)
	if _, err := io.ReadFull(host, ack); err != nil || ack[0] != vmwire.ForwardStreamOK {
		t.Fatalf("stream ACK %v: %v", ack, err)
	}
	stream := vmwire.NewTCPStream(host, host)
	if _, err := io.WriteString(stream, "request"); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(stream)
	if err != nil || string(raw) != "reply after EOF" {
		t.Fatalf("host received %q: %v", raw, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	<-done
}
