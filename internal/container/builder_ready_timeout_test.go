package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func builderTimeoutFixture(t *testing.T, mode string) (*Client, string, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	entered := filepath.Join(dir, "entered")
	t.Setenv("BUILDER_TEST_CALLS", calls)
	t.Setenv("BUILDER_TEST_ENTERED", entered)
	t.Setenv("BUILDER_TEST_MODE", mode)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	budget, _ := resources.Workloads(4 * resources.GiB)
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$BUILDER_TEST_CALLS"
case "$*" in
*'cat /sys/fs/cgroup/'*) printf '%d\nmax\n0\nMemTotal: 4194304 kB\nMemAvailable: 100 kB\n';;
*'systemctl start buildkit.service'*) if [ "$BUILDER_TEST_MODE" = slow ]; then exec sleep 16; fi;;
*'buildctl debug workers'*) if [ "$BUILDER_TEST_MODE" = cancel ]; then : > "$BUILDER_TEST_ENTERED"; exec sleep 60; fi;;
*) exit 37;;
esac
`, budget.Max)
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := NewWithOutput("builder-timeout", io.Discard, io.Discard)
	return c, calls, entered
}

func TestBuilderReadinessExceedsFormer15Seconds(t *testing.T) {
	c, calls, _ := builderTimeoutFixture(t, "slow")
	budgets := []time.Duration{}
	c.newTimeout = func(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
		budgets = append(budgets, budget)
		return context.WithTimeout(ctx, budget)
	}
	if err := c.EnsureBuilder(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "systemctl start buildkit.service") != 1 || strings.Count(string(raw), "buildctl debug workers") != 1 {
		t.Fatalf("startup/probe retried: %s", raw)
	}
	count := 0
	for _, budget := range budgets {
		if budget == vmapi.BuilderReadyTimeout {
			count++
		}
	}
	if vmapi.BuilderReadyTimeout != 2*time.Minute || count != 1 {
		t.Fatalf("readiness budget not shared: %v", budgets)
	}
}

func TestBuilderReadinessCallerCancellationWins(t *testing.T) {
	c, calls, entered := builderTimeoutFixture(t, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := make(chan bool, 1)
	go func() {
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		for {
			if _, err := os.Stat(entered); err == nil {
				observed <- true
				cancel()
				return
			}
			select {
			case <-timeout.C:
				observed <- false
				cancel()
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	err := c.EnsureBuilder(ctx)
	if !errors.Is(err, context.Canceled) || !<-observed {
		t.Fatalf("cancellation lost: %v", err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "systemctl start buildkit.service") != 1 || strings.Count(string(raw), "buildctl debug workers") != 1 {
		t.Fatalf("cancelled startup replayed: %s", raw)
	}
}

func TestBuilderReadinessEarlierDeadlineWins(t *testing.T) {
	c, calls, _ := builderTimeoutFixture(t, "")
	c.newTimeout = func(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
		if budget == vmapi.BuilderReadyTimeout {
			return context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}
		return context.WithTimeout(ctx, budget)
	}
	if err := c.EnsureBuilder(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "systemctl start") || strings.Contains(string(raw), "buildctl debug") {
		t.Fatalf("effects after deadline: %s", raw)
	}
}
