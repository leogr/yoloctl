package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/nativerelease"
)

func TestNativeLatestDispatchAndCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	previous := doAgentRegistryRequest
	t.Cleanup(func() { doAgentRegistryRequest = previous })
	calls := 0
	doAgentRegistryRequest = func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasPrefix(r.URL.String(), "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_") {
			t.Fatalf("wrong distribution endpoint: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.2.17"}`)), Header: http.Header{}}, nil
	}
	// A copied profile must select the same provider without its builtin name.
	p := &agentprofile.Profile{Name: "my-copy", NativeRelease: "antigravity"}
	for range 2 {
		got, err := fetchAgentLatestCached(context.Background(), p, time.Hour)
		if err != nil || got.Latest != "1.2.17" {
			t.Fatalf("latest = %+v, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("cache made %d requests, want one", calls)
	}
}

func TestNativeMaintenanceUsesSandboxAndNeverRunsAgent(t *testing.T) {
	r, err := nativerelease.Lookup("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	args := nativeInstallArgs("native-box", r)
	want := []string{"sudo", "nerdctl", "exec", "--user", "sandbox", "--env", wantAgentMaintenancePath, "native-box", "/usr/bin/python3", "-I", "-c"}
	if !slices.Equal(args[:len(want)], want) {
		t.Fatalf("unsafe native maintenance argv prefix: %v", args[:len(want)])
	}
	if slices.Contains(args, "agy") || slices.Contains(args, "update") || slices.Contains(args, "--version") {
		t.Fatal("agent execution leaked into outer maintenance")
	}
}

func TestAgentRegistryDelayedTLSFollowsCaller(t *testing.T) {
	// Use httptest's trusted certificate with a net.Pipe so the transport
	// performs a real TLS handshake on the virtual clock.
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificate.Close)
	clientTLS := certificate.Client().Transport.(*http.Transport).TLSClientConfig
	for _, scenario := range []string{"late handshake", "cancel handshake"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				conn, peer := net.Pipe()
				defer func() { _ = conn.Close(); _ = peer.Close() }()
				transport := agentRegistryTransport(ctx, func(dialCtx context.Context, _, _ string) (net.Conn, error) {
					if dialCtx != ctx {
						t.Error("registry dial lost the caller context")
					}
					return conn, nil
				})
				transport.Proxy = nil
				transport.TLSClientConfig = clientTLS.Clone()
				defer transport.CloseIdleConnections()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, certificate.URL, nil)
				if err != nil {
					t.Fatal(err)
				}

				done := make(chan error, 1)
				go func() {
					defer func() { _ = peer.Close() }()
					if scenario == "cancel handshake" {
						n, err := io.Copy(io.Discard, peer)
						if n == 0 && err == nil {
							err = errors.New("the peer saw no TLS handshake")
						}
						done <- err
						return
					}
					time.Sleep(12 * time.Second)
					server := tls.Server(peer, certificate.TLS)
					if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
						done <- err
						return
					}
					_, err := io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK")
					done <- err
				}()

				start := time.Now()
				if scenario == "cancel handshake" {
					go func() {
						time.Sleep(2 * time.Second)
						cancel()
					}()
				}
				response, err := (&http.Client{Transport: transport}).Do(req)
				if scenario == "cancel handshake" {
					if !errors.Is(err, context.Canceled) || time.Since(start) != 2*time.Second {
						t.Fatal("TLS ignored caller cancellation", time.Since(start), err)
					}
					synctest.Wait()
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					default:
						t.Fatal("the transport kept the TLS connection open after cancellation")
					}
					return
				}
				if err != nil {
					t.Fatal("late TLS handshake failed", time.Since(start), err)
				}
				defer func() { _ = response.Body.Close() }()
				raw, err := io.ReadAll(response.Body)
				if err != nil || string(raw) != "OK" || time.Since(start) != 12*time.Second {
					t.Fatal(string(raw), time.Since(start), err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestAgentRegistryCancellationStopsDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started, stopped := make(chan struct{}), make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		transport := agentRegistryTransport(ctx, func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			close(started)
			defer close(stopped)
			select {
			case <-dialCtx.Done():
				return nil, dialCtx.Err()
			case <-release:
				return nil, errors.New("dial released by test cleanup")
			}
		})
		transport.Proxy = nil
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.example", nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			<-started
			cancel()
		}()
		if _, err := (&http.Client{Transport: transport}).Do(req); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("dial continued after the request was canceled")
		}
	})
}
