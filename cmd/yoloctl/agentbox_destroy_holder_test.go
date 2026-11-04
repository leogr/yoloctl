package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
)

func TestNativeDestroyHolderKeepsOriginalStopACKAndInitialAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "inventory failure", "stop failure", "settles", "post-ack failure"} {
		t.Run(mode, func(t *testing.T) {
			var events []string
			reads, stops := 0, 0
			cause := errors.New("original holder reply lost")
			unit := session.UnitName("coder")
			inventory := func(context.Context) ([]string, error) {
				events = append(events, "inventory")
				reads++
				if mode == "inventory failure" || mode == "post-ack failure" && reads > 1 {
					return nil, cause
				}
				if mode == "absent" || mode == "settles" && reads > 2 {
					return []string{session.UnitName("peer")}, nil
				}
				return []string{unit}, nil
			}
			stop := func(_ context.Context, selected string) error {
				events = append(events, "stop")
				stops++
				if selected != unit {
					t.Fatalf("stopped unrelated holder %q", selected)
				}
				if mode == "stop failure" {
					return cause
				}
				return nil
			}
			err := withdrawNativeHolderWith(t.Context(), unit, inventory, stop)
			var uncertain *agentbox.NativeEffectsUncertain
			switch mode {
			case "absent":
				if err != nil || stops != 0 || reads != 1 {
					t.Fatalf("initial holder absence lost: %v %v", err, events)
				}
			case "settles":
				if err != nil || stops != 1 || reads != 3 {
					t.Fatalf("settlement retried mutation or skipped inventory: %v %v", err, events)
				}
			case "inventory failure":
				if !errors.Is(err, cause) || stops != 0 || errors.As(err, &uncertain) {
					t.Fatalf("failed preflight became absence or stop: %v %v", err, events)
				}
			case "stop failure":
				if !errors.Is(err, cause) || !errors.As(err, &uncertain) || !reflect.DeepEqual(events, []string{"inventory", "stop"}) {
					t.Fatalf("lost Stop was settled by later state: %v %v", err, events)
				}
			case "post-ack failure":
				if !errors.Is(err, cause) || !errors.As(err, &uncertain) || stops != 1 || reads != 2 {
					t.Fatalf("post-ACK failure was hidden/replayed: %v %v", err, events)
				}
			}
		})
	}
}

// This executes the real Lima adapters using a local limactl fixture. It
// neither invokes a real VM/systemctl nor proves kernel consumer termination.
func TestNativeDestroyHolderActualLimaAdapterPreservesFailedStop(t *testing.T) {
	for _, mode := range []string{"absent", "ack", "stop failed after effect", "inventory failed", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			log := filepath.Join(dir, "argv")
			t.Setenv("YOLOCTL_TEST_HOLDER_ARGV", log)
			t.Setenv("YOLOCTL_TEST_HOLDER_STOPPED", filepath.Join(dir, "stopped"))
			t.Setenv("YOLOCTL_TEST_HOLDER_MODE", mode)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_HOLDER_ARGV"
case "$*" in
 *'systemctl list-units '*)
  if [ "$YOLOCTL_TEST_HOLDER_MODE" = 'inventory failed' ]; then exit 29; fi
  if [ "$YOLOCTL_TEST_HOLDER_MODE" = 'overflow' ]; then exec python3 -c 'import sys; sys.stdout.write("x" * 1048577)'; fi
  if [ "$YOLOCTL_TEST_HOLDER_MODE" != absent ] && [ ! -f "$YOLOCTL_TEST_HOLDER_STOPPED" ]; then
   printf '%s\n' 'yoloctl-session-coder.service loaded failed failed retained holder'
  fi
  exit 0;;
 *'systemctl stop yoloctl-session-coder.service')
  : > "$YOLOCTL_TEST_HOLDER_STOPPED"
  if [ "$YOLOCTL_TEST_HOLDER_MODE" = 'stop failed after effect' ]; then exit 23; fi
  exit 0;;
 *'systemctl is-active '*) printf '%s\n' inactive; exit 3;;
 *) exit 97;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := withdrawNativeHolder(t.Context(), container.New("fixture"), "coder")
			var uncertain *agentbox.NativeEffectsUncertain
			var exit *exec.ExitError
			if mode == "stop failed after effect" {
				if !errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
					t.Fatalf("actual Stop failure was cleared: %v", err)
				}
			} else if mode == "inventory failed" {
				if !errors.As(err, &exit) || exit.ExitCode() != 29 {
					t.Fatalf("failed complete inventory was ignored: %v", err)
				}
			} else if mode == "overflow" {
				if err == nil {
					t.Fatal("oversized initial inventory became holder absence")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			stops, reads := strings.Count(string(commands), "systemctl stop "), strings.Count(string(commands), "systemctl list-units ")
			wantStops, wantReads := 1, 2
			switch mode {
			case "absent", "inventory failed", "overflow":
				wantStops, wantReads = 0, 1
			case "stop failed after effect":
				wantReads = 1
			}
			if stops != wantStops || reads != wantReads || strings.Contains(string(commands), "is-active") {
				t.Fatalf("actual command selected fallback/retry: %s", commands)
			}
		})
	}
}
