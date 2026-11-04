package vmservice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestBoundedPortInventoryRetainsBothTransportsAndFailedReplies(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "failed", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			log := filepath.Join(dir, "argv")
			t.Setenv("YOLOCTL_TEST_PORT_ARGV", log)
			t.Setenv("YOLOCTL_TEST_PORT_MODE", mode)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_PORT_ARGV"
case "$YOLOCTL_TEST_PORT_MODE" in
 valid) printf '%s\n' 'yoloctl-port-old-8000-80.service loaded failed failed retained' 'yoloctl-openshell-port-new-9000-90.service loaded inactive dead retained';;
 empty) exit 0;;
 failed) printf '%s\n' 'yoloctl-port-old-8000-80.service loaded failed failed retained'; exit 29;;
 overflow) exec python3 -c 'import sys; sys.stdout.write("x" * 1048577)';;
 *) exit 98;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			raw, err := InventoryPortsBounded(t.Context(), container.New("fixture"))
			switch mode {
			case "valid":
				if err != nil || !strings.Contains(raw, "yoloctl-port-old-") || !strings.Contains(raw, "yoloctl-openshell-port-new-") {
					t.Fatalf("complete transports lost: %q %v", raw, err)
				}
			case "empty":
				if err != nil || raw != "" {
					t.Fatalf("genuine empty roster refused: %q %v", raw, err)
				}
			case "failed":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 29 || raw != "" {
					t.Fatalf("failed command supplied partial roster: %q %v", raw, err)
				}
			case "overflow":
				if !errors.Is(err, errServiceInventoryOverflow) || raw != "" {
					t.Fatalf("overflow supplied partial roster: %q %v", raw, err)
				}
			}
			argv, readErr := os.ReadFile(log)
			if readErr != nil || !strings.Contains(string(argv), "systemctl list-units --all --no-legend --plain --no-pager --full --type=service yoloctl-port-* yoloctl-openshell-port-*") {
				t.Fatalf("wrong actual inventory scope: %s %v", argv, readErr)
			}
		})
	}
}

func TestBoundedPortInventoryRPCUsesCompleteLogicalPortPrefix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	input := filepath.Join(dir, "request")
	t.Setenv("YOLOCTL_TEST_PORT_REQUEST", input)
	script := `#!/bin/sh
case "$*" in *'/sbin/yoloctl-init unit') ;; *) exit 97;; esac
cat > "$YOLOCTL_TEST_PORT_REQUEST"
printf '%s\n' yoloctl-port-old-8000-80.service yoloctl-openshell-port-new-9000-90.service
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Real RPC builder/capture, supplied through a local limactl fixture; not a
	// claim of Firecracker transport or native service-manager qualification.
	raw, err := readServiceInventory(t.Context(), container.New("fixture"), true, vmwire.LegacyPortUnitPrefix)
	if err != nil || !strings.Contains(raw, "yoloctl-port-old-") || !strings.Contains(raw, "yoloctl-openshell-port-new-") {
		t.Fatalf("RPC roster lost transport: %q %v", raw, err)
	}
	request, err := os.ReadFile(input)
	if err != nil || string(request) != `{"action":"inventory","unit":{"name":"yoloctl-port-"}}` {
		t.Fatalf("wrong RPC inventory opcode/scope: %s %v", request, err)
	}
}
