package firecracker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type apiTransportFunc func(*http.Request) (*http.Response, error)

func (f apiTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type apiBodyFunc func([]byte) (int, error)

func (f apiBodyFunc) Read(p []byte) (int, error) { return f(p) }
func (apiBodyFunc) Close() error                 { return nil }

func TestAPIUsesRequestDeadline(t *testing.T) {
	for _, scenario := range []string{"no deadline", "late deadline", "early header", "cancel body", "error reply"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch scenario {
				case "late deadline", "early header":
					delay := 90 * time.Second
					if scenario == "early header" {
						delay = 2 * time.Second
					}
					child, end := context.WithTimeout(ctx, delay)
					defer end()
					ctx = child
				}
				calls := 0
				wait := func(ctx context.Context) error {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(31 * time.Second):
						return nil
					}
				}
				transport := apiTransportFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					raw, err := io.ReadAll(req.Body)
					_ = req.Body.Close()
					if err != nil || string(raw) != `{"count":2}` || req.Method != "PUT" || req.URL.Path != "/config" {
						t.Errorf("request changed: %s %s %s %v", req.Method, req.URL, raw, err)
					}
					if scenario == "cancel body" {
						cancel()
						return &http.Response{StatusCode: 400, Body: apiBodyFunc(func([]byte) (int, error) {
							return 0, wait(req.Context())
						})}, nil
					}
					if err := wait(req.Context()); err != nil {
						return nil, err
					}
					if scenario == "error reply" {
						return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader("bad config"))}, nil
					}
					return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
				})
				err := apiRequest(ctx, transport, "PUT", "/config", map[string]int{"count": 2})
				switch scenario {
				case "early header":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
				case "cancel body":
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case "error reply":
					if err == nil || err.Error() != "firecracker PUT /config: 400 Bad Request: bad config" {
						t.Fatal(err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
				if calls != 1 {
					t.Fatalf("%d PUTs", calls)
				}
			})
		})
	}
}

func TestAPISerialRequestsGetSeparateWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := apiTransportFunc(func(req *http.Request) (*http.Response, error) {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(90 * time.Second):
				return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
			}
		})
		for _, route := range []string{"/machine-config", "/actions"} {
			if err := apiRequest(t.Context(), transport, "PUT", route, nil); err != nil {
				t.Fatal(err)
			}
		}
	})
}
