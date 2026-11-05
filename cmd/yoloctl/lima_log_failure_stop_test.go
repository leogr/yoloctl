package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
)

// A Lima stop that succeeds while host.log cannot be written stays a
// success: stopSpaceRuntime never takes its ForceStop fallback.
func TestSuccessfulStopWithUnwritableHostLogNeverForces(t *testing.T) {
	home := fakeLima(t)
	createTestSpace(t, "work")
	t.Setenv("FAKE_LIMA_STDERR", "level=warning msg=\"launcher warning\"")
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	stop, reap := lifecycleStopAgentboxContainer, lifecycleReapOrphanedNetns
	t.Cleanup(func() { lifecycleStopAgentboxContainer = stop; lifecycleReapOrphanedNetns = reap })
	lifecycleStopAgentboxContainer = func(context.Context, *container.Client, string) error { return nil }
	lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error { return nil }
	if err := os.Remove(filepath.Join(home, "calls")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var notice strings.Builder
	if err := stopSpaceRuntime(t.Context(), "work", &notice); err != nil {
		t.Fatalf("log failure failed a successful stop: %v", err)
	}
	calls, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "stop ") || strings.Contains(string(calls), "--force") || strings.Contains(notice.String(), "forcing VM stop") {
		t.Fatalf("stop forced: calls %q notice %q", calls, notice.String())
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "unchanged" {
		t.Fatalf("followed the planted log: %q %v", raw, err)
	}
}
