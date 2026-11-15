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
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
)

const diskRefusal = "VM system disk has 28.3 GiB free after cleanup; at least 28.9 GiB (20%) is required before a build or pull; retained images and box data were preserved; free space or increase the VM system disk"

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
	fmt.Fprintln(os.Stderr, "Error:", diskRefusal)
	switch mode {
	case "refusal":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": diskRefusal})
	case "malformed":
		_, _ = fmt.Fprintln(os.Stdout, `{"error":`)
	case "oversized":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": diskRefusal})
		_, _ = io.WriteString(os.Stdout, strings.Repeat(" ", controller.MaxDocument))
	case "signal":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": diskRefusal})
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Kill()
	case "success":
		os.Exit(0)
	}
	os.Exit(2)
}

func TestImagePreflightReturnedRefusalReachesCaller(t *testing.T) {
	for _, mode := range []string{"refusal", "stderr-only", "malformed", "oversized", "signal", "success", "worker"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImagePreflightResultHelper$")
			cmd.Env = append(os.Environ(), "YOLO_TEST_PREFLIGHT_RESULT="+mode)
			cmd.Stderr = io.Discard
			cmd.Stdin = strings.NewReader("{")
			err := runImagePreflightCommand(t.Context(), cmd, nil)
			var failed *controller.ImageBuildFailure
			if mode == "refusal" || mode == "worker" {
				want := diskRefusal
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
	if err == nil || errors.As(err, &failed) || !strings.Contains(stderr.String(), diskRefusal) {
		t.Fatalf("stderr/result boundary changed: stderr=%q err=%v", stderr.String(), err)
	}
}
