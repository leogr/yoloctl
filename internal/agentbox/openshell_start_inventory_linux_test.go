package agentbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/vmservice"
)

func startInventoryFixture(t *testing.T) (*container.Client, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("START_INVENTORY_FIXTURE", dir)
	const script = `#!/bin/sh
set -eu
case "$*" in
 *'systemctl list-units'*'yoloctl-port-'*'yoloctl-openshell-port-'*) ;;
 *) exit 42;;
esac
printf '%s\n' "$*" >> "$START_INVENTORY_FIXTURE/argv"
: > "$START_INVENTORY_FIXTURE/started"
case "$START_INVENTORY_MODE" in
 silent) exec sleep 3;;
 held-pipe)
  (exec 2>/dev/null; exec sleep 3) &
  cat "$START_INVENTORY_FIXTURE/rows";;
 slow-foreground) sleep 1; cat "$START_INVENTORY_FIXTURE/rows";;
 slow-owned) sleep 0.45; cat "$START_INVENTORY_FIXTURE/rows";;
 empty) ;;
 *) exit 43;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	// This box retains a legacy forward, and another box owns a conflicting
	// OpenShell port. Qualification must receive both before filtering.
	local := port.Spec{Agentbox: "fixture", HostPort: 9000, ContainerPort: 7000}
	foreign := port.Spec{Agentbox: "other", HostPort: 9200, ContainerPort: 7200}
	foreignUnit, err := port.OpenShellUnitName(foreign)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Join([]string{
		port.UnitName(local) + " loaded inactive dead",
		foreignUnit + " loaded failed failed",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rows"), []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	return container.New("fixture"), dir
}

func setStartInventoryFailSafe(t *testing.T, duration time.Duration) {
	t.Helper()
	previous := vmservice.InventoryFailSafe
	vmservice.InventoryFailSafe = duration
	t.Cleanup(func() { vmservice.InventoryFailSafe = previous })
}

func TestNativeStartForwardInventoryDiscardsIncompleteOwnedReply(t *testing.T) {
	const failSafe = 750 * time.Millisecond
	setStartInventoryFailSafe(t, failSafe)
	for _, mode := range []string{"silent", "held-pipe"} {
		t.Run(mode, func(t *testing.T) {
			client, dir := startInventoryFixture(t)
			t.Setenv("START_INVENTORY_MODE", mode)
			caller, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			start := time.Now()
			services, err := nativeStartForwardInventory(WithOwnedStartInventory(caller), client)
			if !errors.Is(err, context.DeadlineExceeded) || services != nil || caller.Err() != nil {
				t.Fatalf("incomplete inventory: services=%v error=%v caller=%v", services, err, caller.Err())
			}
			if time.Since(start) < failSafe {
				t.Fatal("inventory did not wait for its deadline")
			}
			argv, err := os.ReadFile(filepath.Join(dir, "argv"))
			if err != nil || strings.Count(string(argv), "--no-pager --full") != 1 {
				t.Fatalf("wanted one bounded complete inventory command, got %q (%v)", argv, err)
			}
		})
	}
}

func TestNativeStartForwardInventoryFollowsCallerCancellation(t *testing.T) {
	setStartInventoryFailSafe(t, 5*time.Second)
	t.Run("owned", func(t *testing.T) { testStartInventoryCallerCancellation(t, true) })
	t.Run("foreground", func(t *testing.T) { testStartInventoryCallerCancellation(t, false) })
}

func testStartInventoryCallerCancellation(t *testing.T, owned bool) {
	t.Helper()
	client, dir := startInventoryFixture(t)
	t.Setenv("START_INVENTORY_MODE", "silent")
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	waitForRead, stopWaiting := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopWaiting()
	commandStarted := make(chan bool, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
				commandStarted <- true
				cancel()
				return
			}
			select {
			case <-waitForRead.Done():
				commandStarted <- false
				cancel()
				return
			case <-ticker.C:
			}
		}
	}()

	ctx := caller
	if owned {
		ctx = WithOwnedStartInventory(ctx)
	}
	services, err := nativeStartForwardInventory(ctx, client)
	if !<-commandStarted || caller.Err() != context.Canceled || err == nil || services != nil {
		t.Fatalf("inventory ignored caller cancellation: services=%v error=%v", services, err)
	}
	if owned && !errors.Is(err, context.Canceled) {
		t.Fatalf("owned inventory lost the caller's cancellation cause: %v", err)
	}
}

func TestNativeStartForwardInventoryRenewsOwnedWaitAndKeepsAllBoxes(t *testing.T) {
	const failSafe = 750 * time.Millisecond
	setStartInventoryFailSafe(t, failSafe)
	client, _ := startInventoryFixture(t)
	caller, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg := &Config{Name: "fixture"}
	conflicting := port.Spec{Agentbox: cfg.Name, HostPort: 9200, ContainerPort: 8200}

	assertComplete := func(ctx context.Context) {
		t.Helper()
		services, err := nativeStartForwardInventory(ctx, client)
		if err != nil || len(services) != 2 || services[0].Spec.Agentbox != cfg.Name || services[0].OpenShell ||
			services[1].Spec.Agentbox != "other" || !services[1].OpenShell {
			t.Fatalf("incomplete forward roster: %v (%v)", services, err)
		}
		if err := noNativeForwardConflict(services, conflicting); !errors.Is(err, port.ErrPortInUse) {
			t.Fatalf("qualification did not report another box's conflicting port: %v", err)
		}
	}

	owned := WithOwnedStartInventory(caller)
	t.Setenv("START_INVENTORY_MODE", "slow-owned")
	start := time.Now()
	assertComplete(owned)
	assertComplete(owned)
	if time.Since(start) <= failSafe {
		t.Fatal("the two replies did not exercise independent waits")
	}
	t.Setenv("START_INVENTORY_MODE", "slow-foreground")
	assertComplete(caller)
	t.Setenv("START_INVENTORY_MODE", "empty")
	if services, err := nativeStartForwardInventory(owned, client); err != nil || len(services) != 0 {
		t.Fatalf("successful empty inventory: services=%v error=%v", services, err)
	}
}
