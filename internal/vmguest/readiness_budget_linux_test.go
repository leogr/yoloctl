//go:build linux

package vmguest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestBuildkitControlReplyBudget(t *testing.T) {
	for _, mode := range []string{"slow-success", "work-timeout", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				server, client := net.Pipe()
				defer func() { _ = client.Close() }()
				done := make(chan struct{})
				calls := 0
				action := "buildkit"
				if mode == "ordinary" {
					action = "ports"
				}
				go func() {
					defer close(done)
					serveControl(server, func(string) (string, error) {
						calls++
						if mode == "work-timeout" {
							time.Sleep(vmapi.BuilderReadyTimeout)
							return "", context.DeadlineExceeded
						}
						time.Sleep(31 * time.Second)
						return "ready", nil
					})
				}()
				time.Sleep(31 * time.Second)
				if err := json.NewEncoder(client).Encode(vmwire.ControlRequest{Action: action}); err != nil {
					t.Fatal(err)
				}
				// Work finishes, then a live peer starts listening after the
				// old 30-second reply allowance has passed.
				delay := 31 * time.Second
				if mode == "work-timeout" {
					delay = vmapi.BuilderReadyTimeout
				}
				time.Sleep(delay + 31*time.Second)
				var reply vmwire.ControlReply
				err := json.NewDecoder(client).Decode(&reply)
				<-done
				switch mode {
				case "slow-success", "ordinary":
					if err != nil || reply.State != "ready" {
						t.Fatalf("late success lost: %+v %v", reply, err)
					}
				case "work-timeout":
					if err != nil || !strings.Contains(reply.Error, context.DeadlineExceeded.Error()) {
						t.Fatalf("full-budget result lost: %+v %v", reply, err)
					}
				}
				if calls != 1 {
					t.Fatalf("dispatch replayed: %d", calls)
				}
			})
		})
	}
}

func TestControlReadBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer func() { _ = client.Close() }()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveControl(server, func(string) (string, error) { t.Error("partial request dispatched"); return "", nil })
		}()
		if _, err := client.Write([]byte(`{"action":"buildkit"`)); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		var reply vmwire.ControlReply
		if err := json.NewDecoder(client).Decode(&reply); err == nil {
			t.Fatal("incomplete request accepted")
		}
		<-done
		if time.Since(start) != 2*time.Minute {
			t.Fatalf("intake budget: %v", time.Since(start))
		}
	})
}

func TestControlReplyReleasesSilentOrClosedPeer(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			server, client := net.Pipe()
			defer func() { _ = client.Close() }()
			dispatched, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				serveControl(server, func(string) (string, error) {
					close(dispatched)
					return "ready", nil
				})
			}()
			if err := json.NewEncoder(client).Encode(vmwire.ControlRequest{Action: "ports"}); err != nil {
				t.Fatal(err)
			}
			<-dispatched
			start := time.Now()
			want := 2 * time.Minute
			if disconnect {
				want = 0
				_ = client.Close()
			}
			<-done
			if got := time.Since(start); got != want {
				t.Fatalf("closed=%t: reply wait %v, want %v", disconnect, got, want)
			}
		})
	}
}

func TestContainerdReadinessBudgetAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if vmapi.ContainerdReadyTimeout != 2*time.Minute || vmapi.BuilderReadyTimeout != 2*time.Minute {
			t.Fatal("startup policies changed")
		}
		count := 0
		if err := waitForContainerd(t.Context(), func(context.Context) error { count++; time.Sleep(16 * time.Second); return nil }); err != nil || count != 1 {
			t.Fatalf("slow readiness %v calls%d", err, count)
		}
		parent, cancel := context.WithCancel(t.Context())
		if err := waitForContainerd(parent, func(context.Context) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
			t.Fatal("late success overrode cancel", err)
		}
		if err := waitForContainerd(t.Context(), func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
}

func TestContainerdCancelsInFlightCommand(t *testing.T) {
	dir := t.TempDir()
	entered := filepath.Join(dir, "entered")
	t.Setenv("CONTAINERD_TEST_ENTERED", entered)
	if err := os.WriteFile(filepath.Join(dir, "nerdctl"), []byte(`#!/bin/sh
: > "$CONTAINERD_TEST_ENTERED"
exec sleep 60
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := make(chan bool, 1)
	go func() {
		limit := time.NewTimer(10 * time.Second)
		defer limit.Stop()
		for {
			if _, err := os.Stat(entered); err == nil {
				observed <- true
				cancel()
				return
			}
			select {
			case <-limit.C:
				observed <- false
				cancel()
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	if err := waitForContainerd(ctx, probeContainerd); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !<-observed {
		t.Fatal("nerdctl command never entered")
	}
}

func TestLazyBuilderLateSuccessCannotOverrideCancel(t *testing.T) {
	b := lazyBuilder{process: &builderProcess{done: make(chan struct{})}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := b.ensure(ctx, nil, func(context.Context) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
