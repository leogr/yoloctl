package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// After a failed graceful stop, the forced VM stop follows the caller: it adds
// no fixed bound (a slow forced stop is not refused after 30 s), it carries the
// caller's deadline unchanged, and the caller's cancellation ends it.
func TestForcedVMStopFollowsTheCaller(t *testing.T) {
	for _, mode := range []string{"no-deadline", "deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			fakeLima(t)
			createTestSpace(t, "work")
			t.Setenv("FAKE_LIMA_FAIL", "stop")
			stop, reap, forced := lifecycleStopAgentboxContainer, lifecycleReapOrphanedNetns, forceVMStop
			t.Cleanup(func() { lifecycleStopAgentboxContainer = stop; lifecycleReapOrphanedNetns = reap; forceVMStop = forced })
			lifecycleStopAgentboxContainer = func(context.Context, *container.Client, string) error { return nil }
			lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error { return nil }
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				caller, cancel = context.WithTimeout(caller, time.Hour)
				defer cancel()
			}
			want, bounded := caller.Deadline()
			calls := 0
			forceVMStop = func(ctx context.Context, name string) error {
				calls++
				got, has := ctx.Deadline()
				if name != "work" || has != bounded || (has && !got.Equal(want)) {
					t.Errorf("forced stop of %q got deadline %v/%v, want %v/%v", name, got, has, want, bounded)
				}
				if mode == "cancel" {
					cancel()
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			err := stopSpaceRuntime(caller, "work", io.Discard)
			if calls != 1 {
				t.Fatalf("forced stop ran %d times", calls)
			}
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("caller cancellation did not end the forced stop: %v", err)
				}
			} else if err != nil {
				t.Fatalf("forced stop under the caller failed: %v", err)
			}
		})
	}
}
