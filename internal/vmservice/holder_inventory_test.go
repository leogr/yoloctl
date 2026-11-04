package vmservice

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
)

func TestHolderInventoryIncludesRetainedStatesAndRejectsIncompleteRows(t *testing.T) {
	want := []string{"yoloctl-session-coder.service", "yoloctl-session-other.service"}
	for _, native := range []bool{true, false} {
		raw := strings.Join(want, "\n")
		if !native {
			raw = want[0] + " loaded failed failed stopped holder\n" + want[1] + " loaded inactive dead retained holder\n"
		}
		got, err := parseHolderInventory(raw, native)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("complete holder inventory native=%v: got=%v err=%v", native, got, err)
		}
		if got, err := parseHolderInventory("\n", native); err != nil || len(got) != 0 {
			t.Fatalf("genuine empty inventory refused: %v %v", got, err)
		}
		for _, bad := range []string{
			"yoloctl-session-coder.service loaded failed failed\nyoloctl-session-coder.service loaded failed failed",
			"yoloctl-session-../coder.service loaded inactive dead",
			"yoloctl-session-.service loaded inactive dead",
			"yoloctl-session-coder.service.extra loaded inactive dead",
			"unrelated.service loaded active running",
			strings.Repeat("x", (1<<20)+1),
		} {
			if _, err := parseHolderInventory(bad, native); err == nil {
				t.Fatalf("unsafe holder inventory accepted native=%v: %.100q", native, bad)
			}
		}
	}
	for _, bad := range []string{"yoloctl-session-coder.service", "yoloctl-session-coder.service loaded inactive"} {
		if _, err := parseHolderInventory(bad, false); err == nil {
			t.Fatalf("truncated systemd row accepted: %q", bad)
		}
	}
	if _, err := parseHolderInventory("yoloctl-session-coder.service unexpected", true); err == nil {
		t.Fatal("unexpected native row fields accepted")
	}
}

func TestHolderInventoryUsesCompleteLimaCommandAndPreservesFailedReply(t *testing.T) {
	for _, exit := range []string{"0", "23"} {
		t.Run(exit, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			log := filepath.Join(dir, "argv")
			t.Setenv("YOLOCTL_TEST_HOLDER_ARGV", log)
			t.Setenv("YOLOCTL_TEST_HOLDER_EXIT", exit)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_HOLDER_ARGV"
printf '%s\n' 'yoloctl-session-coder.service loaded failed failed retained holder'
exit "$YOLOCTL_TEST_HOLDER_EXIT"
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := InventoryHolders(t.Context(), container.New("fixture"))
			if exit == "0" {
				if err != nil || !reflect.DeepEqual(got, []string{"yoloctl-session-coder.service"}) {
					t.Fatal("retained holder lost", got, err)
				}
			} else {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != 23 || len(got) != 0 {
					t.Fatalf("failed inventory became absence/partial proof: %v %v", got, err)
				}
			}
			argv, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(argv), "systemctl list-units --all --no-legend --plain --no-pager --full --type=service yoloctl-session-*") || strings.Contains(string(argv), "is-active") {
				t.Fatalf("holder inventory used partial/coarse command: %s %v", argv, err)
			}
		})
	}
}

func TestHolderInventoryCaptureCannotBypassWriteWithIOCopy(t *testing.T) {
	canceled := false
	capture := &serviceInventoryCapture{data: make([]byte, 0, serviceInventoryMaxBytes), cancel: func() { canceled = true }}
	if _, bypass := any(capture).(io.ReaderFrom); bypass {
		t.Fatal("optimized ReaderFrom bypasses bounded Write")
	}
	_, err := io.Copy(capture, strings.NewReader(strings.Repeat("x", serviceInventoryMaxBytes+1)))
	if !errors.Is(err, errServiceInventoryOverflow) || !capture.overflow || !canceled || len(capture.data) != serviceInventoryMaxBytes {
		t.Fatalf("actual copy exceeded capture bound or lost cancellation: bytes=%d overflow=%v canceled=%v err=%v", len(capture.data), capture.overflow, canceled, err)
	}
	if _, err := capture.Write([]byte("later")); !errors.Is(err, errServiceInventoryOverflow) || len(capture.data) != serviceInventoryMaxBytes {
		t.Fatal("overflow was not sticky", err)
	}
}

func TestHolderInventoryActualCaptureBoundsBothCommandBuilders(t *testing.T) {
	// The real Lima and native-RPC command builders share this capture. A local
	// limactl fixture supplies their bytes; no VM or init daemon is contacted.
	for _, native := range []bool{false, true} {
		for _, mode := range []string{"valid", "overflow", "unfinished"} {
			t.Run(map[bool]string{false: "Lima", true: "init RPC"}[native]+"/"+mode, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				dir := t.TempDir()
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("YOLOCTL_TEST_HOLDER_MODE", mode)
				input := filepath.Join(dir, "rpc-input")
				t.Setenv("YOLOCTL_TEST_HOLDER_INPUT", input)
				script := `#!/bin/sh
case "$*" in
 *'/sbin/yoloctl-init unit') cat > "$YOLOCTL_TEST_HOLDER_INPUT";;
 *'systemctl list-units '*) ;;
 *) exit 97;;
esac
case "$YOLOCTL_TEST_HOLDER_MODE" in
 valid) printf '%s\n' yoloctl-session-coder.service;;
 overflow) exec python3 -c 'import sys; sys.stdout.write("x" * 1048577)' ;;
 unfinished) printf '%s\n' 'yoloctl-session-coder.service loaded inactive dead'; exec sleep 30;;
 *) exit 98;;
esac
`
				if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				raw, err := readHolderInventory(t.Context(), container.New("fixture"), native)
				if native {
					request, readErr := os.ReadFile(input)
					if readErr != nil || !strings.Contains(string(request), `"action":"inventory"`) || !strings.Contains(string(request), `"name":"yoloctl-session-"`) {
						t.Fatalf("native-RPC builder not exercised: %s %v", request, readErr)
					}
				}
				switch mode {
				case "valid":
					if err != nil || raw != "yoloctl-session-coder.service\n" {
						t.Fatalf("complete response changed: %q %v", raw, err)
					}
				case "overflow":
					if !errors.Is(err, errServiceInventoryOverflow) || raw != "" {
						t.Fatalf("overflow became partial/absence proof: %d bytes %v", len(raw), err)
					}
				case "unfinished":
					if !errors.Is(err, context.DeadlineExceeded) || raw != "" || time.Since(started) > serviceInventoryReadTimeout+2*time.Second {
						t.Fatalf("unfinished response not bounded/refused: %q %v after %v", raw, err, time.Since(started))
					}
				}
			})
		}
	}
}
