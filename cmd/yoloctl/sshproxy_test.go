package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHProxyCancellationIsQuietButRealFailuresRemain(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	marker := filepath.Join(dir, "started")
	t.Setenv("YOLOCTL_TEST_PROXY_MARKER", marker)
	for _, cancellation := range []bool{true, false} {
		script := "#!/bin/sh\nexit 23\n"
		if cancellation {
			script = "#!/bin/sh\nprintf started >\"$YOLOCTL_TEST_PROXY_MARKER\"\nexec sleep 30\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := newSSHProxyCmd()
		cmd.SetArgs([]string{"probe", "--space", "work"})
		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(ctx) }()
		if cancellation {
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("proxy never started")
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
		}
		err := <-done
		cancel()
		if cancellation && err != nil {
			t.Fatalf("SSH's proxy cancellation reported a command failure: %v", err)
		}
		if !cancellation && (err == nil || !strings.Contains(err.Error(), "23")) {
			t.Fatalf("real proxy failure lost: %v", err)
		}
	}
}
