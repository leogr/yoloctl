package ocifeature

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryFailSafeIsTwoMinutes(t *testing.T) {
	if registryFailSafe != 2*time.Minute {
		t.Fatalf("fail-safe %s", registryFailSafe)
	}
}

func withRegistryFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	old := registryFailSafe
	registryFailSafe = d
	t.Cleanup(func() { registryFailSafe = old })
}

// trickle sends one byte per step; with stall it then waits for its request
// to end.
type trickle struct {
	ctx   context.Context
	left  string
	step  time.Duration
	stall bool
}

func (b *trickle) Read(p []byte) (int, error) {
	if b.left == "" {
		if b.stall {
			<-b.ctx.Done()
			return 0, b.ctx.Err()
		}
		return 0, io.EOF
	}
	select {
	case <-time.After(b.step):
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	p[0], b.left = b.left[0], b.left[1:]
	return 1, nil
}

func (b *trickle) Close() error { return nil }

// The client had 30 s for a whole request, body included; now a body that
// keeps moving completes however long it takes, and one that stops sending
// ends at the fail-safe: on the token reply as on the content.
func TestRegistryBodiesHaveAProgressFailSafe(t *testing.T) {
	withRegistryFailSafe(t, 400*time.Millisecond)
	const token = `{"token":"t"}`
	for _, tc := range []struct {
		name         string
		token, stall bool
	}{
		{"slow content", false, false}, {"stalled content", false, true},
		{"slow token", true, false}, {"stalled token", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Repeat("x", 12)
			r := &Registry{Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				h := make(http.Header)
				switch {
				case req.URL.Host == "auth.example":
					body := &trickle{ctx: req.Context(), left: token, step: 50 * time.Millisecond, stall: tc.stall}
					if !tc.token {
						body.step, body.stall = 0, false
					}
					return &http.Response{StatusCode: 200, Body: body, Header: h}, nil
				case req.Header.Get("Authorization") == "" && tc.token:
					h.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token",service="s",scope="x"`)
					return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: h}, nil
				default:
					body := &trickle{ctx: req.Context(), left: content, step: 50 * time.Millisecond, stall: tc.stall && !tc.token}
					return &http.Response{StatusCode: 200, Body: body, Header: h}, nil
				}
			})}}
			start := time.Now()
			data, err := r.get(context.Background(), "https://registry.example/v2/x/blobs/y", 1<<20)
			if tc.stall {
				if err == nil || !strings.Contains(err.Error(), "registry sent nothing for") {
					t.Fatalf("stalled body: %q, %v", data, err)
				}
				if took := time.Since(start); took > 5*time.Second {
					t.Fatalf("fail-safe took %v", took)
				}
				return
			}
			// 12 or 13 one-byte steps of 50 ms each: longer than the fail-safe overall.
			if err != nil || string(data) != content {
				t.Fatalf("slow body: %q, %v", data, err)
			}
			if took := time.Since(start); took < 500*time.Millisecond {
				t.Fatalf("slow body took only %v", took)
			}
		})
	}
}

func TestRegistryCallerCancelStopsAStalledBody(t *testing.T) {
	r := &Registry{Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &trickle{ctx: req.Context(), stall: true}, Header: make(http.Header)}, nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if _, err := r.get(ctx, "https://registry.example/v2/x/blobs/y", 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

// NewRegistry has no whole-request timeout; the connection, TLS handshake and
// response headers each wait up to the fail-safe (the default transport's
// 30 s dial and 10 s handshake are gone).
func TestNewRegistryBoundsEachWaitByTheFailSafe(t *testing.T) {
	r := NewRegistry()
	tr, ok := r.Client.Transport.(*http.Transport)
	if r.Client.Timeout != 0 || !ok || tr.TLSHandshakeTimeout != registryFailSafe || tr.ResponseHeaderTimeout != registryFailSafe || tr.DialContext == nil || tr.Proxy == nil {
		t.Fatalf("client timeout %v, transport %+v", r.Client.Timeout, tr)
	}
}

func TestRegistryRedirectDoesNotWaitForUnusedBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token bool
		stall bool
	}{
		{name: "empty content redirect"},
		{name: "stalled content redirect", stall: true},
		{name: "stalled token redirect", token: true, stall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/content":
					if tc.token && req.Header.Get("Authorization") == "" {
						w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+req.Host+`/token",service="test",scope="content"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if tc.token {
						_, _ = io.WriteString(w, "valid content")
						return
					}
					w.Header().Set("Location", "/loaded")
				case "/token":
					w.Header().Set("Location", "/issued")
				case "/loaded":
					_, _ = io.WriteString(w, "valid content")
					return
				case "/issued":
					_, _ = io.WriteString(w, `{"token":"fixture"}`)
					return
				default:
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if tc.stall {
					w.Header().Set("Content-Length", "16")
				}
				w.WriteHeader(http.StatusFound)
				w.(http.Flusher).Flush()
				if tc.stall {
					<-req.Context().Done()
				}
			}))
			t.Cleanup(server.Close)
			registry := NewRegistry()
			transport := registry.Client.Transport.(*http.Transport)
			transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			t.Cleanup(transport.CloseIdleConnections)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			body, err := registry.get(ctx, server.URL+"/content", 100)
			if err != nil || string(body) != "valid content" {
				t.Fatalf("redirect did not reach its final response: %q, %v", body, err)
			}
		})
	}
}

func TestRegistryRefusalDoesNotWaitForStalledRedirect(t *testing.T) {
	for _, kind := range []string{"scheme", "hop limit"} {
		t.Run(kind, func(t *testing.T) {
			var refused, hops atomic.Int32
			untrusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				refused.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(untrusted.Close)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/refused" {
					refused.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				if kind == "hop limit" && hops.Add(1) < 5 {
					w.Header().Set("Location", "/hop")
					w.Header().Set("Content-Length", "0")
					w.WriteHeader(http.StatusFound)
					return
				}
				location := "/refused"
				if kind == "scheme" {
					location = untrusted.URL
				}
				w.Header().Set("Location", location)
				w.Header().Set("Content-Length", "16")
				w.WriteHeader(http.StatusFound)
				w.(http.Flusher).Flush()
				<-req.Context().Done()
			}))
			t.Cleanup(server.Close)
			registry := NewRegistry()
			transport := registry.Client.Transport.(*http.Transport)
			transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			t.Cleanup(transport.CloseIdleConnections)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err := registry.get(ctx, server.URL+"/hop", 100)
			if err == nil || !strings.Contains(err.Error(), "unsafe registry redirect") || ctx.Err() != nil {
				t.Fatalf("redirect refusal waited for the stalled body: %v (caller: %v)", err, ctx.Err())
			}
			if refused.Load() != 0 {
				t.Fatal("registry contacted the rejected redirect destination")
			}
		})
	}
}

func TestRegistryHandshakeStopsOnFailSafeOrCaller(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		name := "TLS fail-safe"
		if callerCancels {
			name = "earlier caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			finished := make(chan struct{})
			defer close(finished)
			clientHello := make(chan byte, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				deadline, _ := ctx.Deadline()
				_ = conn.SetReadDeadline(deadline)
				var first [1]byte
				if _, err := io.ReadFull(conn, first[:]); err != nil {
					return
				}
				clientHello <- first[0]
				if callerCancels {
					cancel()
				}
				<-finished
			}()
			wait := 250 * time.Millisecond
			if callerCancels {
				wait = time.Minute
			}
			withRegistryFailSafe(t, wait)
			r := NewRegistry()
			_, err = r.get(ctx, "https://"+listener.Addr().String(), 100)
			select {
			case first := <-clientHello:
				if first != 22 { // TLS handshake record type
					t.Fatalf("expected a ClientHello, got record type %d", first)
				}
			default:
				t.Fatal("the transport never started the TLS handshake")
			}
			if callerCancels {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("caller did not end the handshake: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "TLS handshake timeout") {
				t.Fatalf("TLS fail-safe did not end the handshake: %v", err)
			}
		})
	}
}
