package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

// Exercise the callback actually supplied by native destroy, using the shared
// local argv fixture. This runs no VM, systemctl or container runtime.
func TestNativeDestroyForwardCallbackRequiresAcknowledgedCollection(t *testing.T) {
	for _, failure := range []string{"", "stop", "inventory"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			raw, err := os.ReadFile("../../internal/port/testdata/limactl-forward-fixture.sh")
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(raw), `cat "$YOLOCTL_TEST_FORWARD_DIR/units"`, `if [ "$count" -lt 3 ]; then
     cat "$YOLOCTL_TEST_FORWARD_DIR/units"
    else
     cat "$YOLOCTL_TEST_FORWARD_DIR/remaining"
    fi`, 1)
			native := "yoloctl-openshell-port-web-8080-80.service"
			legacy := "yoloctl-port-web-8081-81.service"
			other := "yoloctl-openshell-port-other-9090-90.service"
			for name, data := range map[string]string{"limactl": script, "units": native + " loaded inactive dead retained\n" + legacy + " loaded failed failed retained\n" + other + " loaded active running peer\n", "remaining": other + " loaded active running peer\n", "commands.log": ""} {
				mode := os.FileMode(0600)
				if name == "limactl" {
					mode = 0700
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("YOLOCTL_TEST_FORWARD_DIR", dir)
			if failure == "stop" {
				t.Setenv("YOLOCTL_TEST_FORWARD_STOP_EXIT", "23")
			}
			if failure == "inventory" {
				t.Setenv("YOLOCTL_TEST_FORWARD_LIST_FAIL_AT", "2")
			}
			removed, closed := false, false
			removal := destroyRemovalFixture{remove: func(context.Context) error {
				count, err := os.ReadFile(filepath.Join(dir, "list-count"))
				if err != nil || strings.TrimSpace(string(count)) != "3" {
					t.Fatal("removal preceded complete collection", string(count), err)
				}
				removed = true
				return nil
			}, close: func() error { closed = true; return nil }}
			client := container.NewWithOutput("fixture", io.Discard, io.Discard)
			err = removePreparedNativeBox(t.Context(), removal, func(context.Context) error { return nil }, nativeDestroyForwardWithdrawal(client, "web"))
			commandsRaw, readErr := os.ReadFile(filepath.Join(dir, "commands.log"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			commands := string(commandsRaw)
			if !closed || strings.Contains(commands, "is-active") || strings.Contains(commands, "nerdctl") || strings.Count(commands, "list-units") != strings.Count(commands, "list-units --all ") || strings.Contains(commands, "systemctl stop "+other) {
				t.Fatal("wrong native callback/cleanup", closed, commands)
			}
			if failure == "" {
				if err != nil || !removed || strings.Count(commands, "list-units --all ") != 3 || strings.Count(commands, "systemctl stop "+native+"\n") != 1 || strings.Count(commands, "systemctl stop "+legacy+"\n") != 1 {
					t.Fatal("retained collection not acknowledged exactly once", err, removed, commands)
				}
			} else {
				// A failed withdrawal still stops the destroy before removal, as
				// a plain error (old path), not an uncertain outcome.
				var uncertain *agentbox.NativeEffectsUncertain
				if err == nil || removed || errors.As(err, &uncertain) {
					t.Fatal("failed withdrawal authorized removal or became uncertain", err, removed)
				}
				wantStops, wantLists := 1, 1
				if failure == "inventory" {
					wantStops, wantLists = 2, 2
				}
				if strings.Count(commands, "systemctl stop ") != wantStops || strings.Count(commands, "list-units --all ") != wantLists {
					t.Fatal("failed command replayed or continued", commands)
				}
			}
		})
	}
}
