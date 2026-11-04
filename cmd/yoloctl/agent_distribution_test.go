package main

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/nativerelease"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeLatestDispatchAndCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	calls := 0
	http.DefaultClient = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasPrefix(r.URL.String(), "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_") {
			t.Fatalf("wrong distribution endpoint: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.2.17"}`)), Header: http.Header{}}, nil
	})}
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
	want := []string{"sudo", "nerdctl", "exec", "--user", "sandbox", "native-box", "/usr/bin/python3", "-I", "-c"}
	if !slices.Equal(args[:len(want)], want) {
		t.Fatalf("unsafe native maintenance argv prefix: %v", args[:len(want)])
	}
	if slices.Contains(args, "agy") || slices.Contains(args, "update") || slices.Contains(args, "--version") {
		t.Fatal("agent execution leaked into outer maintenance")
	}
}
