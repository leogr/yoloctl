package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
)

// An opaque fixture result, never a measurement of the runner or its reserve.
const preflightRefusalFixture = "synthetic preflight refusal; not a VM disk measurement"

func TestImagePreflightResultHelper(t *testing.T) {
	mode := os.Getenv("YOLO_TEST_PREFLIGHT_RESULT")
	if mode == "" {
		return
	}
	if mode == "worker" {
		os.Exit(classifyError(imagePreflightWorker(context.Background())))
	}
	if os.Getenv("YOLO_TEST_PREFLIGHT_CHECKPOINT") == "1" {
		ready := os.NewFile(3, "ready")
		permit := os.NewFile(4, "permit")
		_, _ = ready.Write([]byte{1})
		var signal [1]byte
		_, _ = io.ReadFull(permit, signal[:])
	}
	fmt.Fprintln(os.Stderr, "Error:", preflightRefusalFixture)
	switch mode {
	case "refusal":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": preflightRefusalFixture})
	case "malformed":
		_, _ = fmt.Fprintln(os.Stdout, `{"error":`)
	case "oversized":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": preflightRefusalFixture})
		_, _ = io.WriteString(os.Stdout, strings.Repeat(" ", controller.MaxDocument))
	case "signal", "signal-delayed":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": preflightRefusalFixture})
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		killPreflightTestHelper(t, func() error {
			if mode == "signal-delayed" {
				// A successful send can return before asynchronous process death.
				go func() {
					time.Sleep(100 * time.Millisecond)
					if err := p.Kill(); err != nil {
						fmt.Fprintln(os.Stderr, "delayed fixture self-kill:", err)
						os.Exit(3)
					}
				}()
				return nil
			}
			return p.Kill()
		})
	case "success":
		os.Exit(0)
	}
	os.Exit(2)
}

func TestImagePreflightReturnedRefusalReachesCaller(t *testing.T) {
	for _, mode := range []string{"refusal", "stderr-only", "malformed", "oversized", "signal", "signal-delayed", "success", "worker"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImagePreflightResultHelper$")
			cmd.Env = append(os.Environ(), "YOLO_TEST_PREFLIGHT_RESULT="+mode)
			cmd.Stderr = io.Discard
			cmd.Stdin = strings.NewReader("{")
			err := runImagePreflightCommand(t.Context(), cmd, nil)
			var failed *controller.ImageBuildFailure
			if mode == "refusal" || mode == "worker" {
				want := preflightRefusalFixture
				if mode == "worker" {
					want = "unexpected EOF"
				}
				if !errors.As(err, &failed) || err.Error() != want {
					t.Fatalf("returned refusal lost: %v", err)
				}
				return
			}
			if errors.As(err, &failed) || (err == nil) != (mode == "success") {
				t.Fatalf("%s misclassified: %v", mode, err)
			}
			if mode == "signal" || mode == "signal-delayed" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("signal did not return a process exit: %v", err)
				}
				status, ok := exit.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatalf("fixture must actually die by SIGKILL, not merely fail: %v", err)
				}
			}
		})
	}
}

func TestImagePreflightRefusalPreservesCleanupPermissionFailure(t *testing.T) {
	for _, refused := range []bool{false, true} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImagePreflightResultHelper$")
		cmd.Env = append(os.Environ(), "YOLO_TEST_PREFLIGHT_RESULT=refusal", "YOLO_TEST_PREFLIGHT_CHECKPOINT=1")
		cmd.Stderr = io.Discard
		denied := errors.New("cleanup permission unavailable")
		err := runImagePreflightCommand(t.Context(), cmd, func() error {
			if refused {
				return denied
			}
			return nil
		})
		var failed *controller.ImageBuildFailure
		if errors.As(err, &failed) == refused || errors.Is(err, denied) != refused {
			t.Fatalf("refused=%v: permission/refusal lost: %v", refused, err)
		}
	}
}

func TestImagePreflightCancellationIsNotReturnedRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestImagePreflightResultHelper$")
	cmd.Env = append(os.Environ(), "YOLO_TEST_PREFLIGHT_RESULT=refusal")
	err := runImagePreflightCommand(ctx, cmd, nil)
	var failed *controller.ImageBuildFailure
	if !errors.Is(err, context.Canceled) || errors.As(err, &failed) {
		t.Fatalf("cancellation misclassified: %v", err)
	}
}

func TestImagePreflightDoesNotUseStderrAsResult(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImagePreflightResultHelper$")
	cmd.Env = append(os.Environ(), "YOLO_TEST_PREFLIGHT_RESULT=stderr-only")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := runImagePreflightCommand(t.Context(), cmd, nil)
	var failed *controller.ImageBuildFailure
	if err == nil || errors.As(err, &failed) || !strings.Contains(stderr.String(), preflightRefusalFixture) {
		t.Fatalf("stderr/result boundary changed: stderr=%q err=%v", stderr.String(), err)
	}
}

// Signal submission is not process settlement. Stay out of the helper's normal
// exit2 path until SIGKILL actually ends it; the parent verifies WaitStatus.
func killPreflightTestHelper(t *testing.T, send func() error) {
	t.Helper()
	if err := send(); err != nil {
		t.Fatal("fixture self-kill:", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
