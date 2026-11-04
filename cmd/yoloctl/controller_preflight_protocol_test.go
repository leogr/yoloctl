package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCleanupPrivateCheckpointHelper(t *testing.T) {
	if os.Getenv("YOLO_TEST_CLEANUP_CHECKPOINT") != "1" {
		return
	}
	ready := os.NewFile(3, "ready")
	permit := os.NewFile(4, "permit")
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(2)
	}
	var signal [1]byte
	if _, err := io.ReadFull(permit, signal[:]); err != nil || signal[0] != 1 {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestCleanupPrivateCheckpointGrantsOnlyAfterLockAnnouncement(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "permit", true: "refuse"}[refuse], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCleanupPrivateCheckpointHelper$")
			cmd.Env = append(os.Environ(), "YOLO_TEST_CLEANUP_CHECKPOINT=1")
			called := 0
			denied := errors.New("cleanup permission denied")
			err := runImagePreflightCommand(ctx, cmd, func() error {
				called++
				if refuse {
					return denied
				}
				return nil
			})
			if called != 1 || refuse != errors.Is(err, denied) || !refuse && err != nil {
				t.Fatal(called, err)
			}
		})
	}
}
