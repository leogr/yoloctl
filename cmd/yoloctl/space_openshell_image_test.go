package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestSpaceOpenShellImageChecksActualInitWithoutFallback(t *testing.T) {
	for _, mode := range []string{"old", "new", "failed", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "init")
			request := filepath.Join(dir, "request")
			body := `#!/bin/sh
cat > "$1"
case "$2" in
 old) echo 'microVM: invalid managed service identity' >&2; exit 1;;
 new) printf '%s\n' yoloctl-port-fixture-8000-80.service; exit 0;;
 failed) echo 'transport failed' >&2; exit 29;;
 overflow) i=0; while [ "$i" -lt 100 ]; do echo 'oversized init diagnostic' >&2; i=$((i+1)); done; exit 1;;
esac
`
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			old := spaceOpenShellInitCommand
			t.Cleanup(func() { spaceOpenShellInitCommand = old })
			calls := 0
			spaceOpenShellInitCommand = func(ctx context.Context, space string, args ...string) *exec.Cmd {
				calls++
				if space != "fixture" || strings.Join(args, " ") != "sudo /sbin/yoloctl-init unit" {
					t.Fatalf("unexpected effect/command: %s %q", space, args)
				}
				return exec.CommandContext(ctx, script, request, mode)
			}
			boot := &spaceconfig.Bootstrap{Name: "fixture", Bundle: "old-controller", Generation: "retained", VM: spaceconfig.VMSettings{Backend: "firecracker"}}
			err := checkSpaceOpenShellImage(t.Context(), boot)
			if mode == "new" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failed init observation was accepted")
			}
			if mode == "old" && !strings.Contains(err.Error(), "Run `yoloctl space recreate fixture --update-vm-image`") {
				t.Fatal(err)
			}
			if mode != "old" && err != nil && strings.Contains(err.Error(), "VM image is older") {
				t.Fatalf("unknown failure asserted old media: %v", err)
			}
			if calls != 1 {
				t.Fatalf("unexpected fallback/retry: %d", calls)
			}
			raw, readErr := os.ReadFile(request)
			if readErr != nil || string(raw) != `{"action":"inventory","unit":{"name":"yoloctl-port-"}}` {
				t.Fatalf("wrong read-only request: %s %v", raw, readErr)
			}
			if boot.Bundle != "old-controller" || boot.Generation != "retained" {
				t.Fatal("probe selected a new controller")
			}
		})
	}
}

func TestSpaceOpenShellImageCancellationAndOtherBackend(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := spaceOpenShellInitCommand
	t.Cleanup(func() { spaceOpenShellInitCommand = old })
	calls := 0
	spaceOpenShellInitCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: "lima"}}
	if err := checkSpaceOpenShellImage(t.Context(), boot); err != nil || calls != 0 {
		t.Fatalf("unexpected other-backend probe: %v %d", err, calls)
	}
	boot.VM.Backend = "firecracker"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checkSpaceOpenShellImage(ctx, boot); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSpaceOpenShellImageRunningLimaRequiresActualNft(t *testing.T) {
	for _, mode := range []string{"new", "missing", "broken", "failed", "overflow", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir, err := lima.InstanceDir("fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "lima.yaml"), lima.TemplateWithAccountDisk(nil, "", false), 0600); err != nil {
				t.Fatal(err)
			}
			oldCommand, oldInstance := spaceOpenShellInitCommand, spaceOpenShellInstance
			t.Cleanup(func() { spaceOpenShellInitCommand = oldCommand; spaceOpenShellInstance = oldInstance })
			spaceOpenShellInstance = func(context.Context, string) (vmhost.Instance, bool, error) {
				status := "Running"
				if mode == "stopped" {
					status = "Stopped"
				}
				return vmhost.Instance{Status: status}, true, nil
			}
			calls := 0
			spaceOpenShellInitCommand = func(ctx context.Context, space string, args ...string) *exec.Cmd {
				calls++
				if space != "fixture" || len(args) != 4 || strings.Join(args[:3], " ") != "sudo sh -c" || !strings.Contains(args[3], "test -x /usr/sbin/nft") || !strings.Contains(args[3], "/usr/sbin/nft --version") {
					t.Fatalf("unexpected guest effect/probe: %s %q", space, args)
				}
				code := "exit 0"
				switch mode {
				case "missing", "broken":
					code = "exit 78"
				case "failed":
					code = "exit 29"
				case "overflow":
					code = "i=0; while [ $i -lt 100 ]; do echo oversized-nft-diagnostic >&2; i=$((i+1)); done; exit 78"
				}
				return exec.CommandContext(ctx, "sh", "-c", code)
			}
			boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: "lima"}}
			err = checkSpaceOpenShellImage(t.Context(), boot)
			var mismatch *spaceVMCompatibilityError
			if mode == "missing" || mode == "broken" {
				if !errors.As(err, &mismatch) || mismatch.Backend != "lima" {
					t.Fatalf("running v2 without nft did not offer recreation: %v", err)
				}
			} else if mode == "failed" || mode == "overflow" {
				if err == nil || errors.As(err, &mismatch) {
					t.Fatalf("unknown transport outcome became VM mismatch: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "stopped" {
				expected = 0
			}
			if calls != expected {
				t.Fatalf("guest probe count=%d, want %d", calls, expected)
			}
		})
	}
}
