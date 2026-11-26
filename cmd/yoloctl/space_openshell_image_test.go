package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestSpaceOpenShellCheckTimeoutReportsGuestCommand(t *testing.T) {
	const stdout = "fixture-inventory-stdout"
	const warning = "guest startup warning\x1b[31m\n"
	const excess = "beyond capture bound"
	const nftCommand = "'sudo' 'sh' '-c' 'if ! test -x /usr/sbin/nft || ! /usr/sbin/nft --version >/dev/null 2>&1; then exit 78; fi'"
	first := strings.Repeat("x", 512)
	for _, tc := range []struct {
		name       string
		backend    string
		check      func(context.Context, *spaceconfig.Bootstrap) error
		command    string
		stderr     string
		wantOutput string
	}{
		{"init diagnostic", "firecracker", checkSpaceOpenShellImage,
			"'sudo' '/sbin/yoloctl-init' 'unit'", warning, "stderr " + strconv.Quote(warning)},
		{"nft silent", "lima", checkRunningLimaNFT, nftCommand, "", "no stderr output"},
		{"nft truncated", "lima", checkRunningLimaNFT, nftCommand, first + excess, "stderr (first 512 bytes) " + strconv.Quote(first)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := spaceOpenShellInitCommand
			t.Cleanup(func() { spaceOpenShellInitCommand = old })
			spaceOpenShellInitCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1" >&2; printf '%s' "$2"; exec sleep 60`, "probe", tc.stderr, stdout)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: tc.backend}}
			err := tc.check(ctx, boot)
			var mismatch *spaceVMCompatibilityError
			if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &mismatch) {
				t.Fatalf("wanted deadline error, got %v", err)
			}
			text := err.Error()
			if !strings.Contains(text, "guest command "+tc.command) || !strings.Contains(text, tc.wantOutput) {
				t.Fatalf("timeout is missing the command or diagnostic: %v", err)
			}
			for _, unexpected := range []string{stdout, excess, "exec sleep", "\x1b"} {
				if strings.Contains(text, unexpected) {
					t.Fatalf("timeout exposed %q: %v", unexpected, err)
				}
			}
		})
	}
}

func TestSpaceOpenShellCheckCancellationKeepsContextError(t *testing.T) {
	capture := spaceOpenShellInitError{data: []byte("guest diagnostic")}
	err := spaceOpenShellCheckContextError("check guest", context.Canceled, []string{"sudo", "probe"}, capture)
	if !errors.Is(err, context.Canceled) || err.Error() != "check guest: context canceled" {
		t.Fatal(err)
	}
}

func TestSpaceOpenShellChecksInheritCommandDeadline(t *testing.T) {
	for _, backend := range []struct {
		name  string
		check func(context.Context, *spaceconfig.Bootstrap) error
	}{
		{"firecracker", checkSpaceOpenShellImage},
		{"lima", checkRunningLimaNFT},
	} {
		t.Run(backend.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				budget time.Duration
			}{
				{"no deadline", 0},
				{"short deadline", 3 * time.Second},
				{"long deadline", 30 * time.Second},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := t.Context()
					if tc.budget != 0 {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, tc.budget)
						defer cancel()
					}
					want, wantSet := ctx.Deadline()
					old := spaceOpenShellInitCommand
					t.Cleanup(func() { spaceOpenShellInitCommand = old })
					spaceOpenShellInitCommand = func(probe context.Context, _ string, _ ...string) *exec.Cmd {
						got, gotSet := probe.Deadline()
						if gotSet != wantSet || !got.Equal(want) {
							t.Fatalf("probe deadline %v (%t), want %v (%t)", got, gotSet, want, wantSet)
						}
						return exec.CommandContext(probe, "sh", "-c", "exit 0")
					}
					boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: backend.name}}
					if err := backend.check(ctx, boot); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestSpaceOpenShellLimaCheckCanCancelWithoutDeadline(t *testing.T) {
	started, signalStart, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = started.Close() }()
	defer func() { _ = signalStart.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signalResult := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := started.Read(signal[:])
		cancel()
		signalResult <- err
	}()
	old := spaceOpenShellInitCommand
	t.Cleanup(func() { spaceOpenShellInitCommand = old })
	spaceOpenShellInitCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "printf x >&3; exec sleep 30")
		cmd.ExtraFiles = []*os.File{signalStart}
		return cmd
	}
	boot := &spaceconfig.Bootstrap{Name: "fixture", VM: spaceconfig.VMSettings{Backend: "lima"}}
	err = checkRunningLimaNFT(ctx, boot)
	_ = signalStart.Close()
	if startErr := <-signalResult; startErr != nil {
		t.Fatalf("probe did not start: %v", startErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wanted cancellation, got %v", err)
	}
}
